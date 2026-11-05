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

func TestCredentialConversionMembershipPreflight(t *testing.T) {
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
	defer admin.Close(context.Background())
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatal(err)
	}
	suffix := hex.EncodeToString(random[:])
	old, bridge, other := "rg140_old_"+suffix, "rg140_bridge_"+suffix, "rg140_other_"+suffix
	ident := func(s string) string { return pgx.Identifier{s}.Sanitize() }
	exec := func(sql string) {
		t.Helper()
		if _, err := admin.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{old, bridge, other} {
		exec("CREATE ROLE " + ident(name) + " NOLOGIN NOINHERIT")
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
	// Prove read-only execution and no catalog repair on every admission/refusal.
	check := func(wantBlocked bool) {
		t.Helper()
		tx, err := admin.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(context.Background())
		snapshot := func() string {
			t.Helper()
			var state string
			if err := tx.QueryRow(ctx, `SELECT jsonb_build_array(
 (SELECT jsonb_agg(to_jsonb(r) ORDER BY r.oid) FROM pg_catalog.pg_roles r WHERE rolname=ANY($1::text[])),
 (SELECT jsonb_agg(to_jsonb(m) ORDER BY roleid,member,grantor) FROM pg_catalog.pg_auth_members m))::text`, []string{old, bridge, other}).Scan(&state); err != nil {
				t.Fatal(err)
			}
			return state
		}
		before := snapshot()
		err = RequireNoCredentialConversionMembers(ctx, tx, oid, old)
		if wantBlocked {
			if !errors.Is(err, ErrCredentialConversionMembers) {
				t.Fatalf("membership not refused: %v", err)
			}
		} else if err != nil {
			t.Fatalf("isolated role refused: %v", err)
		}
		if after := snapshot(); after != before {
			t.Fatal("preflight changed role attributes or memberships")
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}
	check(false)
	// Outgoing membership is not a path into the old identity.
	exec("GRANT " + ident(bridge) + " TO " + ident(old))
	check(false)
	exec("REVOKE " + ident(bridge) + " FROM " + ident(old))

	for _, options := range []string{
		"WITH INHERIT FALSE, SET TRUE",
		"WITH INHERIT TRUE, SET FALSE",
		"WITH INHERIT FALSE, SET FALSE, ADMIN TRUE",
		"WITH INHERIT FALSE, SET FALSE, ADMIN FALSE",
	} {
		exec("GRANT " + ident(old) + " TO " + ident(other) + " " + options)
		check(true)
		exec("REVOKE " + ident(old) + " FROM " + ident(other))
		check(false)
	}

	// NOLOGIN/NOINHERIT intermediates must not hide a reachable login.
	exec("ALTER ROLE " + ident(other) + " LOGIN PASSWORD '" + suffix + "'")
	exec("GRANT " + ident(old) + " TO " + ident(bridge) + " WITH INHERIT FALSE, SET TRUE")
	exec("GRANT " + ident(bridge) + " TO " + ident(other) + " WITH INHERIT FALSE, SET TRUE")
	check(true)
	cfg, err := pgx.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.User, cfg.Password = other, suffix
	client, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close(context.Background())
	if _, err := client.Exec(ctx, "SET ROLE "+ident(old)); err != nil {
		t.Fatalf("indirect SET ROLE counterexample: %v", err)
	}
	var effective, session string
	if err := client.QueryRow(ctx, "SELECT current_user, session_user").Scan(&effective, &session); err != nil || effective != old || session != other {
		t.Fatalf("unexpected role identities: %v", err)
	}
	if _, err := client.Exec(ctx, "RESET ROLE"); err != nil {
		t.Fatal(err)
	}
	exec("REVOKE " + ident(old) + " FROM " + ident(bridge))
	check(false)

	// A stale OID/name pair cannot accidentally authorize a replacement role.
	for _, target := range []struct {
		oid  uint32
		name string
	}{{0, old}, {oid, ""}, {oid, other}, {oid, old + "_missing"}} {
		if err := RequireNoCredentialConversionMembers(ctx, admin, target.oid, target.name); err == nil {
			t.Fatal("invalid role identity admitted")
		}
	}
	exec("DROP ROLE " + ident(old))
	exec("CREATE ROLE " + ident(old) + " NOLOGIN")
	if err := RequireNoCredentialConversionMembers(ctx, admin, oid, old); err == nil {
		t.Fatal("replacement role admitted under stale OID")
	}
	cancelled, stop := context.WithCancel(ctx)
	stop()
	if err := RequireNoCredentialConversionMembers(cancelled, admin, oid, old); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled inspection did not propagate cancellation: %v", err)
	}
	t.Log("direct and indirect members refused read-only, including NOLOGIN and disabled-option memberships; stale identity and cancellation refused")
}
