package db

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"
)

func TestClientProfilePostRestoreRepairRestoresObservedInvariants(t *testing.T) {
	databaseURL := os.Getenv("ROUTEGATE_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("ROUTEGATE_TEST_DATABASE_URL is not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	pool, err := Connect(ctx, databaseURL, logger)
	if err != nil {
		t.Fatalf("connect to test PostgreSQL: %v", err)
	}
	defer pool.Close()

	resetPublicSchema(t, ctx, pool)
	preRepairDir := copyMigrationsBefore(t, "../../migrations", "000150b_client_profile_schema_invariant_repair.up.sql")
	if err := Migrate(ctx, pool, preRepairDir, logger); err != nil {
		t.Fatalf("apply migrations before 000150b: %v", err)
	}

	if _, err := pool.Exec(ctx, `
		ALTER TABLE vpn_client_profiles
		  DROP CONSTRAINT IF EXISTS vpn_client_profiles_vpn_account_id_key;
		DROP TRIGGER IF EXISTS trg_vpn_client_profiles_mark_server_dirty ON vpn_client_profiles;
	`); err != nil {
		t.Fatalf("simulate partial restore drift: %v", err)
	}

	var uniquePresent, triggerPresent bool
	if err := pool.QueryRow(ctx, `
		SELECT
		  EXISTS (
		    SELECT 1
		    FROM pg_index AS index_row
		    JOIN pg_attribute AS attribute_row
		      ON attribute_row.attrelid = index_row.indrelid
		     AND attribute_row.attnum = index_row.indkey[0]
		    WHERE index_row.indrelid = 'vpn_client_profiles'::regclass
		      AND index_row.indisunique
		      AND index_row.indisvalid
		      AND index_row.indpred IS NULL
		      AND index_row.indexprs IS NULL
		      AND index_row.indnkeyatts = 1
		      AND attribute_row.attname = 'vpn_account_id'
		  ),
		  EXISTS (
		    SELECT 1
		    FROM pg_trigger
		    WHERE tgrelid = 'vpn_client_profiles'::regclass
		      AND tgname = 'trg_vpn_client_profiles_mark_server_dirty'
		      AND NOT tgisinternal
		  )
	`).Scan(&uniquePresent, &triggerPresent); err != nil {
		t.Fatalf("read drifted invariants: %v", err)
	}
	if uniquePresent || triggerPresent {
		t.Fatalf("fixture must reproduce missing uniqueness+trigger, got unique=%v trigger=%v", uniquePresent, triggerPresent)
	}

	if err := Migrate(ctx, pool, "../../migrations", logger); err != nil {
		t.Fatalf("apply post-restore repair and remaining migrations: %v", err)
	}

	if err := pool.QueryRow(ctx, `
		SELECT
		  EXISTS (
		    SELECT 1
		    FROM pg_index AS index_row
		    JOIN pg_attribute AS attribute_row
		      ON attribute_row.attrelid = index_row.indrelid
		     AND attribute_row.attnum = index_row.indkey[0]
		    WHERE index_row.indrelid = 'vpn_client_profiles'::regclass
		      AND index_row.indisunique
		      AND index_row.indisvalid
		      AND index_row.indpred IS NULL
		      AND index_row.indexprs IS NULL
		      AND index_row.indnkeyatts = 1
		      AND attribute_row.attname = 'vpn_account_id'
		  ),
		  EXISTS (
		    SELECT 1
		    FROM pg_trigger
		    WHERE tgrelid = 'vpn_client_profiles'::regclass
		      AND tgname = 'trg_vpn_client_profiles_mark_server_dirty'
		      AND NOT tgisinternal
		  )
	`).Scan(&uniquePresent, &triggerPresent); err != nil {
		t.Fatalf("read repaired invariants: %v", err)
	}
	if !uniquePresent || !triggerPresent {
		t.Fatalf("repair did not restore invariants: unique=%v trigger=%v", uniquePresent, triggerPresent)
	}

	version, err := NewSchemaVersionRepository(pool).AppliedSchemaVersion(ctx)
	if err != nil {
		t.Fatalf("read applied schema version: %v", err)
	}
	if version != "000163_runtime_recovery_challenge_ledger" {
		t.Fatalf("applied schema version = %q, want 000163_runtime_recovery_challenge_ledger", version)
	}
}

func TestClientProfilePostRestoreRepairFailsSafeOnDuplicates(t *testing.T) {
	databaseURL := os.Getenv("ROUTEGATE_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("ROUTEGATE_TEST_DATABASE_URL is not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	pool, err := Connect(ctx, databaseURL, logger)
	if err != nil {
		t.Fatalf("connect to test PostgreSQL: %v", err)
	}
	defer pool.Close()

	resetPublicSchema(t, ctx, pool)
	preRepairDir := copyMigrationsBefore(t, "../../migrations", "000150b_client_profile_schema_invariant_repair.up.sql")
	if err := Migrate(ctx, pool, preRepairDir, logger); err != nil {
		t.Fatalf("apply migrations before 000150b: %v", err)
	}

	if _, err := pool.Exec(ctx, `
		ALTER TABLE vpn_client_profiles
		  DROP CONSTRAINT IF EXISTS vpn_client_profiles_vpn_account_id_key;
	`); err != nil {
		t.Fatalf("drop client-profile uniqueness: %v", err)
	}

	var accountID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO vpn_accounts (username, protocol, display_name, status)
		VALUES ('rg116-duplicate-profile-fixture', 'sing-box', 'RG-116 duplicate profile fixture', 'active')
		RETURNING id::text
	`).Scan(&accountID); err != nil {
		t.Fatalf("create VPN account fixture: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO vpn_client_profiles (vpn_account_id, name)
		VALUES ($1::uuid, 'first'), ($1::uuid, 'second')
	`, accountID); err != nil {
		t.Fatalf("create duplicate client profiles: %v", err)
	}

	err = Migrate(ctx, pool, "../../migrations", logger)
	if err == nil {
		t.Fatal("expected repair to refuse duplicate vpn_client_profiles rows")
	}
	if !strings.Contains(err.Error(), "cannot repair vpn_client_profiles uniqueness") {
		t.Fatalf("unexpected repair error: %v", err)
	}
}
