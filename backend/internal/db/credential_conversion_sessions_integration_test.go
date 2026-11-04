package db

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// This is a disposable SQL rehearsal, not an installed-host conversion helper.
// No existing application role, password, schema or service is changed.
func TestCredentialConversionSessionFence(t *testing.T) {
	url := os.Getenv("ROUTEGATE_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("ROUTEGATE_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	admin, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { admin.Close(context.Background()) }()
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatal(err)
	}
	suffix := hex.EncodeToString(random[:])
	old, other := "rg140_old_"+suffix, "rg140_other_"+suffix
	password := suffix // Disposable credentials, never logged or persisted.
	exec := func(c *pgx.Conn, sql string, args ...any) {
		t.Helper()
		if _, err := c.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{old, other} {
		exec(admin, "CREATE ROLE "+pgx.Identifier{name}.Sanitize()+" LOGIN PASSWORD '"+password+"'")
		defer func(name string) {
			cleanup, done := context.WithTimeout(context.Background(), 10*time.Second)
			defer done()
			if _, err := admin.Exec(cleanup, "DROP ROLE "+pgx.Identifier{name}.Sanitize()); err != nil {
				t.Errorf("remove disposable role: %v", err)
			}
		}(name)
	}
	connect := func(name string) (*pgx.Conn, error) {
		cfg, err := pgx.ParseConfig(url)
		if err != nil {
			return nil, err
		}
		cfg.User, cfg.Password = name, password
		return pgx.ConnectConfig(ctx, cfg)
	}
	mustConnect := func(name string) *pgx.Conn {
		t.Helper()
		c, err := connect(name)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	idle := mustConnect(old)
	defer idle.Close(context.Background())
	transaction := mustConnect(old)
	defer transaction.Close(context.Background())
	control := mustConnect(other)
	defer control.Close(context.Background())
	oldIdent := pgx.Identifier{old}.Sanitize()

	// An uncommitted or rolled-back NOLOGIN is not an admission fence.
	exec(admin, "BEGIN")
	exec(admin, "ALTER ROLE "+oldIdent+" NOLOGIN")
	beforeCommit := mustConnect(old)
	beforeCommit.Close(ctx)
	exec(admin, "ROLLBACK")
	afterRollback := mustConnect(old)
	afterRollback.Close(ctx)

	// Hold actual transactional work: termination must roll it back. TEMP objects
	// require no persistent schema grants or application-table changes.
	exec(transaction, "CREATE TEMP TABLE conversion_probe (id integer)")
	exec(transaction, "BEGIN")
	exec(transaction, "INSERT INTO conversion_probe VALUES (1)")
	var lockKey int64 = int64(idle.PgConn().PID())
	exec(transaction, "SELECT pg_advisory_xact_lock($1)", lockKey)
	exec(admin, "ALTER ROLE "+oldIdent+" NOLOGIN") // Autocommit before session drain.

	denied := func() {
		t.Helper()
		c, err := connect(old)
		if c != nil {
			c.Close(ctx)
		}
		var pgerr *pgconn.PgError
		if !errors.As(err, &pgerr) || pgerr.Code != "28000" {
			t.Fatalf("expected committed NOLOGIN denial (28000), got %v", err)
		}
	}
	denied()
	// Existing sessions survive NOLOGIN and can still execute SQL.
	exec(idle, "SELECT 1")
	exec(transaction, "INSERT INTO conversion_probe VALUES (2)")
	t.Log("committed NOLOGIN rejects fresh login but existing sessions remain usable")

	// Losing the administrative connection must not implicitly restore LOGIN.
	admin.Close(ctx)
	admin, err = pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	denied()
	var login bool
	if err := admin.QueryRow(ctx, "SELECT rolcanlogin FROM pg_roles WHERE rolname=$1", old).Scan(&login); err != nil || login {
		t.Fatalf("NOLOGIN did not persist: login=%v err=%v", login, err)
	}

	// Scope by role OID across the cluster, not database or process name. The
	// positive timeout waits for exit; a successful signal alone is insufficient.
	rows, err := admin.Query(ctx, `SELECT pid FROM pg_stat_activity
 WHERE usesysid=(SELECT oid FROM pg_roles WHERE rolname=$1)`, old)
	if err != nil {
		t.Fatal(err)
	}
	var pids []int32
	for rows.Next() {
		var pid int32
		if err := rows.Scan(&pid); err != nil {
			t.Fatal(err)
		}
		pids = append(pids, pid)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(pids) != 2 {
		t.Fatalf("expected two old-role backends, got %d", len(pids))
	}
	for _, pid := range pids {
		var terminated bool
		if err := admin.QueryRow(ctx, "SELECT pg_terminate_backend($1, 5000)", pid).Scan(&terminated); err != nil || !terminated {
			t.Fatalf("backend termination unconfirmed: %v", err)
		}
	}
	var remaining int
	if err := admin.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity
 WHERE usesysid=(SELECT oid FROM pg_roles WHERE rolname=$1)`, old).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("old sessions remain: count=%d err=%v", remaining, err)
	}
	for _, c := range []*pgx.Conn{idle, transaction} {
		if _, err := c.Exec(ctx, "SELECT 1"); err == nil {
			t.Fatal("terminated session still executes SQL")
		}
	}
	var released bool
	if err := control.QueryRow(ctx, "SELECT pg_try_advisory_xact_lock($1)", lockKey).Scan(&released); err != nil || !released {
		t.Fatalf("terminated transaction retains its lock: released=%v err=%v", released, err)
	}
	denied()
	exec(control, "SELECT 1")
	freshControl := mustConnect(other)
	freshControl.Close(ctx)
	t.Log("old backends exited and released transaction lock; unrelated login and session survive")

	// Boundary counterexample: NOLOGIN does not revoke membership/SET ROLE.
	// A usesysid scan alone cannot discover this alternate authenticated identity.
	exec(admin, "GRANT "+oldIdent+" TO "+pgx.Identifier{other}.Sanitize())
	exec(control, "SET ROLE "+oldIdent)
	var effective, session string
	if err := control.QueryRow(ctx, "SELECT current_user, session_user").Scan(&effective, &session); err != nil || effective != old || session != other {
		t.Fatalf("membership boundary counterexample failed: %v", err)
	}
	exec(control, "RESET ROLE")
	exec(admin, "REVOKE "+oldIdent+" FROM "+pgx.Identifier{other}.Sanitize())
	t.Log("NOLOGIN is not an authority fence: another login with membership can SET ROLE")
}
