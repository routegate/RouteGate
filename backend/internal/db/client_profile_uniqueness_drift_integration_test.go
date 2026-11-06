package db

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"
)

func TestClientProfileSchemaInvariantRepairRepairsAlreadyAppliedHistoricalDrift(t *testing.T) {
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
	if err := Migrate(ctx, pool, "../../migrations", logger); err != nil {
		t.Fatalf("apply current migrations before simulating drift: %v", err)
	}

	var accountID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO vpn_accounts (username, protocol, display_name, status)
		VALUES ('rg114-drift-fixture', 'sing-box', 'RG-114 drift fixture', 'active')
		RETURNING id::text
	`).Scan(&accountID); err != nil {
		t.Fatalf("create drift fixture account: %v", err)
	}

	// Simulate a historical host where later versions such as 000131 are already
	// recorded, but the new invariant-repair migration has never run and the
	// physical client-profile schema has drifted.
	if _, err := pool.Exec(ctx, `
		DELETE FROM schema_migrations
		WHERE version = '000130z_client_profile_schema_invariant_repair';

		DROP TRIGGER IF EXISTS trg_vpn_client_profiles_mark_server_dirty ON vpn_client_profiles;
		ALTER TABLE vpn_client_profiles DROP CONSTRAINT IF EXISTS vpn_client_profiles_protocol_check;
		ALTER TABLE vpn_client_profiles DROP CONSTRAINT IF EXISTS vpn_client_profiles_vpn_account_id_key;
		ALTER TABLE vpn_client_profiles DROP COLUMN IF EXISTS protocol;
	`); err != nil {
		t.Fatalf("create historical client-profile schema drift: %v", err)
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO vpn_client_profiles (
			vpn_account_id, name, created_at, updated_at
		) VALUES
			($1::uuid, 'Older profile', now() - interval '2 hours', now() - interval '1 hour'),
			($1::uuid, 'Newest profile', now() - interval '1 hour', now())
	`, accountID); err != nil {
		t.Fatalf("create duplicate historical client profiles: %v", err)
	}

	if err := Migrate(ctx, pool, "../../migrations", logger); err != nil {
		t.Fatalf("reapply missing client-profile invariant repair migration: %v", err)
	}

	var profileCount int
	var preservedName, protocol string
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*), MAX(name), MAX(protocol)
		FROM vpn_client_profiles
		WHERE vpn_account_id = $1::uuid
	`, accountID).Scan(&profileCount, &preservedName, &protocol); err != nil {
		t.Fatalf("read repaired client profiles: %v", err)
	}
	if profileCount != 1 {
		t.Fatalf("client profile count = %d, want 1", profileCount)
	}
	if preservedName != "Newest profile" {
		t.Fatalf("preserved profile = %q, want Newest profile", preservedName)
	}
	if protocol != "auto" {
		t.Fatalf("repaired protocol = %q, want auto", protocol)
	}

	var uniqueIndexPresent bool
	if err := pool.QueryRow(ctx, `
		SELECT EXISTS (
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
		)
	`).Scan(&uniqueIndexPresent); err != nil {
		t.Fatalf("check repaired uniqueness: %v", err)
	}
	if !uniqueIndexPresent {
		t.Fatal("vpn_client_profiles.vpn_account_id uniqueness was not restored")
	}

	var protocolConstraintPresent, dirtyTriggerPresent bool
	if err := pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM pg_constraint
			WHERE conrelid = 'vpn_client_profiles'::regclass
			  AND conname = 'vpn_client_profiles_protocol_check'
		), EXISTS (
			SELECT 1
			FROM pg_trigger
			WHERE tgrelid = 'vpn_client_profiles'::regclass
			  AND tgname = 'trg_vpn_client_profiles_mark_server_dirty'
			  AND NOT tgisinternal
		)
	`).Scan(&protocolConstraintPresent, &dirtyTriggerPresent); err != nil {
		t.Fatalf("check repaired protocol invariants: %v", err)
	}
	if !protocolConstraintPresent {
		t.Fatal("vpn_client_profiles protocol constraint was not restored")
	}
	if !dirtyTriggerPresent {
		t.Fatal("vpn_client_profiles dirty-state trigger was not restored")
	}

	var upsertedName string
	if err := pool.QueryRow(ctx, `
		INSERT INTO vpn_client_profiles (vpn_account_id, name)
		VALUES ($1::uuid, 'Should not replace preserved profile')
		ON CONFLICT (vpn_account_id) DO UPDATE
		SET vpn_account_id = EXCLUDED.vpn_account_id
		RETURNING name
	`, accountID).Scan(&upsertedName); err != nil {
		t.Fatalf("ON CONFLICT client-profile upsert after repair: %v", err)
	}
	if upsertedName != "Newest profile" {
		t.Fatalf("upsert returned profile = %q, want Newest profile", upsertedName)
	}

	var repairApplied bool
	if err := pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM schema_migrations
			WHERE version = '000130z_client_profile_schema_invariant_repair'
		)
	`).Scan(&repairApplied); err != nil {
		t.Fatalf("check invariant repair migration record: %v", err)
	}
	if !repairApplied {
		t.Fatal("client-profile invariant repair migration was not recorded")
	}

	version, err := NewSchemaVersionRepository(pool).AppliedSchemaVersion(ctx)
	if err != nil {
		t.Fatalf("read applied schema version: %v", err)
	}
	if version != "000176_credential_removal_held_dispatch" {
		t.Fatalf("applied schema version = %q, want 000176_credential_removal_held_dispatch", version)
	}
}
