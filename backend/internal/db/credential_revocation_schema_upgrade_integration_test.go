package db

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"
)

// This is an UPGRADE test: current-main schema 160 and its user/traffic data
// already exist before we introduce the previously unmerged RG-140 ledger.
// It must not require a reset, rename the old 000160, produce a pending
// credential removal, or prevent ordinary account updates.
func TestCredentialRevocationPreparationUpgradesCurrent160WithoutDataLoss(t *testing.T) {
	databaseURL := os.Getenv("ROUTEGATE_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("ROUTEGATE_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	pool, err := Connect(ctx, databaseURL, logger)
	if err != nil {
		t.Fatalf("connect isolated PostgreSQL: %v", err)
	}
	defer pool.Close()
	resetPublicSchema(t, ctx, pool)

	prior := copyMigrationsBefore(t, "../../migrations", "000161_credential_revocation_preparations.up.sql")
	if err := Migrate(ctx, pool, prior, logger); err != nil {
		t.Fatalf("apply current production schema 160: %v", err)
	}
	version, err := NewSchemaVersionRepository(pool).AppliedSchemaVersion(ctx)
	if err != nil || version != "000160_traffic_report_checkpoints" {
		t.Fatalf("base schema = %q (%v), need exact current 160", version, err)
	}

	var serverID, agentID, accountID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO servers (name, status) VALUES ('pre-revocation production fixture', 'active')
		RETURNING id::text
	`).Scan(&serverID); err != nil {
		t.Fatalf("create existing server: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO agents (server_id, agent_version, token_hash, status)
		VALUES ($1::uuid, 'pre-upgrade', 'pre-upgrade-token-hash', 'online')
		RETURNING id::text
	`, serverID).Scan(&agentID); err != nil {
		t.Fatalf("create existing Agent: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO vpn_accounts (username, protocol, display_name, status, server_id)
		VALUES ('pre-upgrade-account', 'sing-box', 'Existing user', 'active', $1::uuid)
		RETURNING id::text
	`, serverID).Scan(&accountID); err != nil {
		t.Fatalf("create existing VPN account: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO traffic_report_checkpoints (agent_id, stream_id, sequence, payload_hash)
		VALUES ($1::uuid, '11111111-1111-4111-8111-111111111111'::uuid, 123, decode('aabb', 'hex'))
	`, agentID); err != nil {
		t.Fatalf("create existing traffic checkpoint: %v", err)
	}

	if err := Migrate(ctx, pool, "../../migrations", logger); err != nil {
		t.Fatalf("upgrade existing 160 to safe RG-140 161: %v", err)
	}
	if err := Migrate(ctx, pool, "../../migrations", logger); err != nil {
		t.Fatalf("re-run upgraded migration idempotently: %v", err)
	}
	version, err = NewSchemaVersionRepository(pool).AppliedSchemaVersion(ctx)
	if err != nil || version != "000167_runtime_recovery_enrollment_preparation" {
		t.Fatalf("latest version = %q (%v), expected 167 dormant enrollment preparation ledger", version, err)
	}

	var previousCount, newCount, legacyCollision int
	if err := pool.QueryRow(ctx, `
		SELECT
		  count(*) FILTER (WHERE version = '000160_traffic_report_checkpoints'),
		  count(*) FILTER (WHERE version = '000161_credential_revocation_preparations'),
		  count(*) FILTER (WHERE version = '000160_credential_revocation_preparations')
		FROM schema_migrations
	`).Scan(&previousCount, &newCount, &legacyCollision); err != nil {
		t.Fatalf("read migration identities: %v", err)
	}
	if previousCount != 1 || newCount != 1 || legacyCollision != 0 {
		t.Fatalf("historical migration was changed or duplicated: previous=%d next=%d conflict=%d", previousCount, newCount, legacyCollision)
	}
	var savedName string
	var sequence int64
	var payloadHex string
	if err := pool.QueryRow(ctx, `SELECT display_name FROM vpn_accounts WHERE id=$1::uuid`, accountID).Scan(&savedName); err != nil {
		t.Fatalf("existing account was lost: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT sequence, encode(payload_hash, 'hex')
		FROM traffic_report_checkpoints WHERE agent_id=$1::uuid
	`, agentID).Scan(&sequence, &payloadHex); err != nil {
		t.Fatalf("existing checkpoint was lost: %v", err)
	}
	if savedName != "Existing user" || sequence != 123 || payloadHex != "aabb" {
		t.Fatalf("upgrade altered existing account/checkpoint: %q, %d, %q", savedName, sequence, payloadHex)
	}
	var ops int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM vpn_credential_revocations`).Scan(&ops); err != nil {
		t.Fatalf("preparation table unavailable: %v", err)
	}
	if ops != 0 {
		t.Fatalf("schema upgrade must not request credential removal: %d operations", ops)
	}
	// The protective triggers must remain inert until an explicitly prepared
	// operation exists, even if the user is already deployed.
	if _, err := pool.Exec(ctx, `UPDATE vpn_accounts SET display_name='Still connected' WHERE id=$1::uuid`, accountID); err != nil {
		t.Fatalf("unused revocation gate blocked ordinary account update: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT display_name FROM vpn_accounts WHERE id=$1::uuid`, accountID).Scan(&savedName); err != nil || savedName != "Still connected" {
		t.Fatalf("ordinary account update failed after migration: %q (%v)", savedName, err)
	}
}
