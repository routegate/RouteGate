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
)

func TestCredentialConversionPreparedPreflight(t *testing.T) {
	url := os.Getenv("ROUTEGATE_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("ROUTEGATE_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	admin, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(context.Background())
	var capacity int
	if err := admin.QueryRow(ctx, "SELECT current_setting('max_prepared_transactions')::int").Scan(&capacity); err != nil || capacity < 3 {
		t.Fatalf("fixture requires at least three prepared-transaction slots: capacity=%d err=%v", capacity, err)
	}
	var random [12]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatal(err)
	}
	suffix := hex.EncodeToString(random[:])
	old, other := "rg140_prepared_old_"+suffix, "rg140_prepared_other_"+suffix
	ident := func(s string) string { return pgx.Identifier{s}.Sanitize() }
	exec := func(c *pgx.Conn, sql string) {
		t.Helper()
		if _, err := c.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{old, other} {
		exec(admin, "CREATE ROLE "+ident(name)+" LOGIN PASSWORD '"+suffix+"'")
		defer func(name string) {
			cleanup, done := context.WithTimeout(context.Background(), 5*time.Second)
			defer done()
			if _, err := admin.Exec(cleanup, "DROP ROLE "+ident(name)); err != nil {
				t.Errorf("remove fixture role: %v", err)
			}
		}(name)
	}
	var oid uint32
	if err := admin.QueryRow(ctx, "SELECT oid FROM pg_catalog.pg_roles WHERE rolname=$1", old).Scan(&oid); err != nil {
		t.Fatal(err)
	}
	connect := func(database, role string) *pgx.Conn {
		t.Helper()
		cfg, err := pgx.ParseConfig(url)
		if err != nil {
			t.Fatal(err)
		}
		cfg.Database = database
		if role != "" {
			cfg.User, cfg.Password = role, suffix
		}
		c, err := pgx.ConnectConfig(ctx, cfg)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	var databases []string
	var owners []*pgx.Conn
	for _, prefix := range []string{"rg140_prepared_a_", "rg140_prepared_b_"} {
		name := prefix + suffix
		exec(admin, "CREATE DATABASE "+ident(name))
		defer func(name string) {
			cleanup, done := context.WithTimeout(context.Background(), 5*time.Second)
			defer done()
			if _, err := admin.Exec(cleanup, "DROP DATABASE "+ident(name)); err != nil {
				t.Errorf("remove fixture database: %v", err)
			}
		}(name)
		c := connect(name, "")
		defer c.Close(context.Background())
		exec(c, "CREATE TABLE public.probe (id integer)")
		exec(c, "GRANT INSERT ON public.probe TO "+ident(old)+", "+ident(other))
		databases, owners = append(databases, name), append(owners, c)
	}
	targetDatabase := databases[1]
	var databaseOID uint32
	if err := admin.QueryRow(ctx, "SELECT oid FROM pg_catalog.pg_database WHERE datname=$1", targetDatabase).Scan(&databaseOID); err != nil {
		t.Fatal(err)
	}
	// Clean up only fixture GIDs, including after an assertion failure. This is
	// test teardown, not production recovery policy.
	pending := map[string]*pgx.Conn{}
	defer func() {
		for gid, owner := range pending {
			cleanup, done := context.WithTimeout(context.Background(), 5*time.Second)
			_, err := owner.Exec(cleanup, "ROLLBACK PREPARED '"+gid+"'")
			done()
			if err != nil {
				t.Errorf("remove fixture prepared transaction: %v", err)
			}
		}
	}()
	prepare := func(database int, role, gid string) {
		t.Helper()
		c := connect(databases[database], role)
		defer c.Close(context.Background())
		exec(c, "BEGIN")
		exec(c, "INSERT INTO public.probe VALUES (1)")
		exec(c, "PREPARE TRANSACTION '"+gid+"'")
		pending[gid] = owners[database]
		var terminated bool
		if err := admin.QueryRow(ctx, "SELECT pg_terminate_backend($1, 5000)", c.PgConn().PID()).Scan(&terminated); err != nil || !terminated {
			t.Fatalf("fixture session exit unconfirmed: %v", err)
		}
	}
	check := func(wantBlocked bool) {
		t.Helper()
		tx, err := admin.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(context.Background())
		snapshot := func() string {
			var state string
			if err := tx.QueryRow(ctx, `SELECT coalesce(jsonb_agg(to_jsonb(p) ORDER BY gid),'[]'::jsonb)::text
 FROM pg_catalog.pg_prepared_xacts p WHERE owner=ANY($1::text[])`, []string{old, other}).Scan(&state); err != nil {
				t.Fatal(err)
			}
			return state
		}
		before := snapshot()
		err = RequireNoCredentialConversionPrepared(ctx, tx, oid, old, databaseOID, targetDatabase)
		if wantBlocked && !errors.Is(err, ErrCredentialConversionPrepared) {
			t.Fatalf("prepared transaction not refused: %v", err)
		}
		if !wantBlocked && err != nil {
			t.Fatalf("empty target refused: %v", err)
		}
		if snapshot() != before {
			t.Fatal("preflight resolved or changed a prepared transaction")
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}
	check(false)
	controlGID, firstGID, secondGID := "rg140_control_"+suffix, "rg140_first_"+suffix, "rg140_second_"+suffix
	prepare(0, other, controlGID)
	check(false) // A different owner in a different database is out of scope.
	prepare(0, old, firstGID)
	check(true) // Old-role work blocks even outside the target database.
	prepare(1, old, secondGID)
	exec(admin, "ALTER ROLE "+ident(old)+" NOLOGIN")
	var sessions int
	if err := admin.QueryRow(ctx, "SELECT count(*) FROM pg_catalog.pg_stat_activity WHERE usesysid=$1::oid", oid).Scan(&sessions); err != nil || sessions != 0 {
		t.Fatalf("expected no old-role sessions: count=%d err=%v", sessions, err)
	}
	check(true)
	// Explicit fixture resolution demonstrates both outcomes and admission only
	// after the last target transaction, including one in a different database.
	exec(owners[0], "COMMIT PREPARED '"+firstGID+"'")
	delete(pending, firstGID)
	check(true)
	exec(owners[1], "ROLLBACK PREPARED '"+secondGID+"'")
	delete(pending, secondGID)
	check(false)
	alternateGID := "rg140_alternate_" + suffix
	prepare(1, other, alternateGID)
	check(true) // A different owner can still have prepared writes in the target.
	exec(owners[1], "ROLLBACK PREPARED '"+alternateGID+"'")
	delete(pending, alternateGID)
	check(false)
	for i, want := range []int{1, 0} {
		var count int
		if err := owners[i].QueryRow(ctx, "SELECT count(*) FROM public.probe").Scan(&count); err != nil || count != want {
			t.Fatalf("explicit resolution effects: database=%d rows=%d err=%v", i, count, err)
		}
	}
	var controlPresent bool
	if err := admin.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_catalog.pg_prepared_xacts WHERE gid=$1)", controlGID).Scan(&controlPresent); err != nil || !controlPresent {
		t.Fatalf("unrelated prepared transaction lost: %v", err)
	}
	for _, target := range []struct {
		oid  uint32
		name string
	}{{0, old}, {oid, ""}, {oid, other}, {oid, old + "_missing"}} {
		if err := RequireNoCredentialConversionPrepared(ctx, admin, target.oid, target.name, databaseOID, targetDatabase); err == nil {
			t.Fatal("invalid role admitted")
		}
	}
	for _, target := range []struct {
		oid  uint32
		name string
	}{{0, targetDatabase}, {databaseOID, ""}, {databaseOID, databases[0]}, {databaseOID, targetDatabase + "_missing"}} {
		if err := RequireNoCredentialConversionPrepared(ctx, admin, oid, old, target.oid, target.name); err == nil {
			t.Fatal("invalid database admitted")
		}
	}
	cancelled, stop := context.WithCancel(ctx)
	stop()
	if err := RequireNoCredentialConversionPrepared(cancelled, admin, oid, old, databaseOID, targetDatabase); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation not propagated: %v", err)
	}
	t.Log("read-only preflight refuses old-role prepared work across databases and alternate-role prepared work in the target database")
}
