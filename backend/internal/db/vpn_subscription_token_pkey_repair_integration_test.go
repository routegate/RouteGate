package db

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"
)

// TestVPNSubscriptionTokenPrimaryKeyRepair reproduces the schema damage left
// by the first RG-116 production-like rollback: migration history is through
// 000150, but vpn_subscription_tokens has lost its primary key. The 000150a
// repair must restore the canonical PK before 000151 adds its delivery FK.
func TestVPNSubscriptionTokenPrimaryKeyRepair(t *testing.T) {
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
	preRepairDir := copyMigrationsBefore(t, "../../migrations", "000150a_vpn_subscription_token_pkey_repair.up.sql")
	if err := Migrate(ctx, pool, preRepairDir, logger); err != nil {
		t.Fatalf("apply migrations through 000150: %v", err)
	}

	if _, err := pool.Exec(ctx, `ALTER TABLE vpn_subscription_tokens DROP CONSTRAINT vpn_subscription_tokens_pkey`); err != nil {
		t.Fatalf("simulate missing vpn_subscription_tokens primary key: %v", err)
	}

	var hasPrimaryKey bool
	if err := pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM pg_constraint
			WHERE conrelid = 'vpn_subscription_tokens'::regclass
			  AND contype = 'p'
		)
	`).Scan(&hasPrimaryKey); err != nil {
		t.Fatalf("check simulated primary-key drift: %v", err)
	}
	if hasPrimaryKey {
		t.Fatal("expected simulated vpn_subscription_tokens primary key to be absent")
	}

	if err := Migrate(ctx, pool, "../../migrations", logger); err != nil {
		t.Fatalf("apply 000150a repair and 000151: %v", err)
	}

	var primaryKeyOnID bool
	if err := pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM pg_constraint c
			JOIN pg_attribute a
			  ON a.attrelid = c.conrelid
			 AND a.attnum = c.conkey[1]
			WHERE c.conrelid = 'vpn_subscription_tokens'::regclass
			  AND c.contype = 'p'
			  AND cardinality(c.conkey) = 1
			  AND a.attname = 'id'
		)
	`).Scan(&primaryKeyOnID); err != nil {
		t.Fatalf("check repaired primary key: %v", err)
	}
	if !primaryKeyOnID {
		t.Fatal("expected vpn_subscription_tokens(id) primary key to be repaired")
	}

	var deliveryTokenFK bool
	if err := pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM pg_constraint
			WHERE conrelid = 'deliveries'::regclass
			  AND contype = 'f'
			  AND conname = 'deliveries_subscription_token_id_fkey'
		)
	`).Scan(&deliveryTokenFK); err != nil {
		t.Fatalf("check 000151 delivery token foreign key: %v", err)
	}
	if !deliveryTokenFK {
		t.Fatal("expected 000151 delivery token foreign key after primary-key repair")
	}

	var latestSchema string
	if err := pool.QueryRow(ctx, `SELECT version FROM schema_migrations ORDER BY version DESC LIMIT 1`).Scan(&latestSchema); err != nil {
		t.Fatalf("read latest schema: %v", err)
	}
	if latestSchema != "000165_runtime_recovery_observation_nonce" {
		t.Fatalf("latest schema = %q, want 000165_runtime_recovery_observation_nonce", latestSchema)
	}
}
