package db

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ikaevus/routegate/backend/internal/agents"
)

func TestMigrationsApplyFromScratchOnPostgreSQL(t *testing.T) {
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
		t.Fatalf("apply migrations from scratch: %v", err)
	}
	if err := Migrate(ctx, pool, "../../migrations", logger); err != nil {
		t.Fatalf("re-run migrations idempotently: %v", err)
	}

	version, err := NewSchemaVersionRepository(pool).AppliedSchemaVersion(ctx)
	if err != nil {
		t.Fatalf("read applied schema version: %v", err)
	}
	if version != "000178_retired_vless_credentials" {
		t.Fatalf("applied schema version = %q, want 000178_retired_vless_credentials", version)
	}
	assertRawTrafficArchivalPreservesDailyRollup(t, ctx, pool)
	assertAgentMaintenanceJobContract(t, ctx, pool)

	var defaultRoleServerID, deploymentRoleDefault string
	if err := pool.QueryRow(ctx, `
		INSERT INTO servers (name, status)
		VALUES ('RG-114 role default fixture', 'pending')
		RETURNING id::text, deployment_role
	`).Scan(&defaultRoleServerID, &deploymentRoleDefault); err != nil {
		t.Fatalf("create default-role server: %v", err)
	}
	if deploymentRoleDefault != "vpn" {
		t.Fatalf("new server deployment role = %q, want vpn", deploymentRoleDefault)
	}

	var nodeGroupID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO node_groups (name, selection_strategy)
		VALUES ('RG-114H integration group', 'weighted')
		RETURNING id::text
	`).Scan(&nodeGroupID); err != nil {
		t.Fatalf("create node group member: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO node_group_members (node_group_id, server_id, priority, weight)
		VALUES ($1::uuid, $2::uuid, 10, 250)
	`, nodeGroupID, defaultRoleServerID); err != nil {
		t.Fatalf("create node group member: %v", err)
	}

	var routingProfileID, vpnAccountID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO routing_profiles (name, description)
		VALUES ('RG-114H account profile', 'integration fixture')
		RETURNING id::text
	`).Scan(&routingProfileID); err != nil {
		t.Fatalf("create account routing profile: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO vpn_accounts (username, protocol, display_name, status, server_id)
		VALUES ('rg114h-fixture', 'sing-box', 'RG-114H fixture', 'active', $1::uuid)
		RETURNING id::text
	`, defaultRoleServerID).Scan(&vpnAccountID); err != nil {
		t.Fatalf("create VPN account routing fixture: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO vpn_account_routing_profiles (vpn_account_id, routing_profile_id)
		VALUES ($1::uuid, $2::uuid)
	`, vpnAccountID, routingProfileID); err != nil {
		t.Fatalf("create VPN account routing profile assignment: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO vpn_account_node_groups (vpn_account_id, node_group_id)
		VALUES ($1::uuid, $2::uuid)
	`, vpnAccountID, nodeGroupID); err != nil {
		t.Fatalf("create VPN account routing policy: %v", err)
	}
	var automaticSelectionEnabled, allowDegraded bool
	var cooldownSeconds int
	if err := pool.QueryRow(ctx, `
		INSERT INTO vpn_account_automatic_selection_policies (vpn_account_id, enabled)
		VALUES ($1::uuid, TRUE)
		RETURNING enabled, allow_degraded, cooldown_seconds
	`, vpnAccountID).Scan(&automaticSelectionEnabled, &allowDegraded, &cooldownSeconds); err != nil {
		t.Fatalf("create automatic selection policy: %v", err)
	}
	if !automaticSelectionEnabled || allowDegraded || cooldownSeconds != 300 {
		t.Fatalf("unexpected automatic selection defaults: enabled=%v allow_degraded=%v cooldown=%d", automaticSelectionEnabled, allowDegraded, cooldownSeconds)
	}

	rows, err := pool.Query(ctx, `
		SELECT column_name
		FROM information_schema.columns
		WHERE table_schema = 'public'
		  AND table_name = 'deliveries'
	`)
	if err != nil {
		t.Fatalf("list deliveries columns: %v", err)
	}
	defer rows.Close()

	forbidden := map[string]struct{}{
		"message_body":          {},
		"html_body":             {},
		"vless_uri":             {},
		"vless_link":            {},
		"connect_url":           {},
		"qr_payload":            {},
		"qr_image":              {},
		"credentials":           {},
		"provider_raw_response": {},
		"provider_raw_error":    {},
	}
	for rows.Next() {
		var column string
		if err := rows.Scan(&column); err != nil {
			t.Fatalf("scan deliveries column: %v", err)
		}
		if _, disallowed := forbidden[column]; disallowed {
			t.Fatalf("deliveries contains forbidden sensitive payload column %q", column)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate deliveries columns: %v", err)
	}

	providerRows, err := pool.Query(ctx, `
		SELECT column_name
		FROM information_schema.columns
		WHERE table_schema = 'public'
		  AND table_name = 'delivery_provider_settings'
	`)
	if err != nil {
		t.Fatalf("list delivery_provider_settings columns: %v", err)
	}
	defer providerRows.Close()

	allowedSecretStorage := map[string]struct{}{
		"secret_ciphertext":  {},
		"secret_nonce":       {},
		"secret_key_version": {},
	}
	for providerRows.Next() {
		var column string
		if err := providerRows.Scan(&column); err != nil {
			t.Fatalf("scan delivery_provider_settings column: %v", err)
		}
		if column == "password" || column == "token" || column == "access_token" || column == "credentials" || column == "secret" {
			t.Fatalf("delivery_provider_settings contains plaintext secret column %q", column)
		}
		if len(column) >= 7 && column[:7] == "secret_" {
			if _, allowed := allowedSecretStorage[column]; !allowed {
				t.Fatalf("delivery_provider_settings contains unexpected secret column %q", column)
			}
		}
	}
	if err := providerRows.Err(); err != nil {
		t.Fatalf("iterate delivery_provider_settings columns: %v", err)
	}

	pairingRows, err := pool.Query(ctx, `
		SELECT column_name
		FROM information_schema.columns
		WHERE table_schema='public'
		  AND table_name='telegram_pairing_sessions'
	`)
	if err != nil {
		t.Fatalf("list telegram_pairing_sessions columns: %v", err)
	}
	defer pairingRows.Close()
	pairingColumns := map[string]bool{}
	for pairingRows.Next() {
		var column string
		if err := pairingRows.Scan(&column); err != nil {
			t.Fatalf("scan telegram_pairing_sessions column: %v", err)
		}
		pairingColumns[column] = true
		if column == "start_parameter" || column == "token" || column == "pairing_token" || column == "secret" {
			t.Fatalf("telegram_pairing_sessions contains plaintext pairing secret column %q", column)
		}
	}
	if err := pairingRows.Err(); err != nil {
		t.Fatalf("iterate telegram_pairing_sessions columns: %v", err)
	}
	if !pairingColumns["start_parameter_hash"] {
		t.Fatal("telegram_pairing_sessions must store only the start parameter hash")
	}
}

func assertRawTrafficArchivalPreservesDailyRollup(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	var serverID, agentID, accountID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO servers (name, status)
		VALUES ('raw retention fixture', 'active')
		RETURNING id::text
	`).Scan(&serverID); err != nil {
		t.Fatalf("create raw retention server: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO agents (server_id, agent_version, token_hash, status)
		VALUES ($1::uuid, 'test', 'raw-retention-agent', 'online')
		RETURNING id::text
	`, serverID).Scan(&agentID); err != nil {
		t.Fatalf("create raw retention agent: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO vpn_accounts (username, protocol, display_name, status, server_id)
		VALUES ('raw-retention', 'sing-box', 'Raw retention fixture', 'active', $1::uuid)
		RETURNING id::text
	`, serverID).Scan(&accountID); err != nil {
		t.Fatalf("create raw retention account: %v", err)
	}
	observedAt := time.Date(2026, 1, 10, 12, 0, 0, 0, time.UTC)
	if _, err := pool.Exec(ctx, `
		INSERT INTO traffic_usage_events (server_id, agent_id, vpn_account_id, rx_bytes, tx_bytes, observed_at)
		VALUES ($1::uuid, $2::uuid, $3::uuid, 100, 50, $4)
	`, serverID, agentID, accountID, observedAt); err != nil {
		t.Fatalf("create raw retention event: %v", err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin raw retention archival: %v", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT set_config('routegate.preserve_traffic_rollup', 'on', true)`); err != nil {
		t.Fatalf("mark archival transaction: %v", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM traffic_usage_events WHERE vpn_account_id = $1::uuid`, accountID); err != nil {
		t.Fatalf("archive raw retention event: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit raw retention archival: %v", err)
	}

	var eventCount, rxBytes, txBytes int64
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM traffic_usage_events WHERE vpn_account_id = $1::uuid`, accountID).Scan(&eventCount); err != nil {
		t.Fatalf("count archived raw events: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT rx_bytes, tx_bytes
		FROM traffic_usage_daily
		WHERE server_id = $1::uuid AND usage_date = $2::date
	`, serverID, observedAt).Scan(&rxBytes, &txBytes); err != nil {
		t.Fatalf("read preserved daily rollup: %v", err)
	}
	if eventCount != 0 || rxBytes != 100 || txBytes != 50 {
		t.Fatalf("raw archival result events=%d daily=(%d,%d), want 0 and (100,50)", eventCount, rxBytes, txBytes)
	}

	var ordinaryEventID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO traffic_usage_events (server_id, agent_id, vpn_account_id, rx_bytes, tx_bytes, observed_at)
		VALUES ($1::uuid, $2::uuid, $3::uuid, 30, 20, $4)
		RETURNING id::text
	`, serverID, agentID, accountID, observedAt).Scan(&ordinaryEventID); err != nil {
		t.Fatalf("create ordinary-delete traffic event: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM traffic_usage_events WHERE id = $1::uuid`, ordinaryEventID); err != nil {
		t.Fatalf("delete ordinary traffic event: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT rx_bytes, tx_bytes
		FROM traffic_usage_daily
		WHERE server_id = $1::uuid AND usage_date = $2::date
	`, serverID, observedAt).Scan(&rxBytes, &txBytes); err != nil {
		t.Fatalf("read daily rollup after ordinary delete: %v", err)
	}
	if rxBytes != 100 || txBytes != 50 {
		t.Fatalf("transaction-local archival marker leaked: daily=(%d,%d), want (100,50)", rxBytes, txBytes)
	}
}

func assertAgentMaintenanceJobContract(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	var serverID, agentID string
	if err := pool.QueryRow(ctx, `
		SELECT server_id::text, id::text
		FROM agents
		WHERE token_hash = 'raw-retention-agent'
	`).Scan(&serverID, &agentID); err != nil {
		t.Fatalf("read Agent maintenance fixture: %v", err)
	}
	var schemaVersion int
	var jobID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO agent_operation_jobs (server_id, agent_id, kind, operation, request_payload)
		VALUES (
			$1::uuid, $2::uuid, 'maintenance', 'analyze_runtime_artifacts',
			'{"schemaVersion":1,"cutoff":"2026-01-01T00:00:00Z"}'::jsonb
		)
		RETURNING (request_payload ->> 'schemaVersion')::int, id::text
	`, serverID, agentID).Scan(&schemaVersion, &jobID); err != nil {
		t.Fatalf("create typed Agent maintenance job: %v", err)
	}
	if schemaVersion != 1 {
		t.Fatalf("Agent maintenance request schema=%d, want 1", schemaVersion)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE agent_operation_jobs
		SET status = 'succeeded', started_at = now(), completed_at = now(), updated_at = now()
		WHERE id = $1::uuid
	`, jobID); err != nil {
		t.Fatalf("complete typed Agent maintenance fixture: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO agent_operation_jobs (server_id, agent_id, kind, operation)
		VALUES ($1::uuid, $2::uuid, 'maintenance', 'shell')
	`, serverID, agentID); err == nil {
		t.Fatal("Agent maintenance job constraint accepted an arbitrary operation")
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO agent_operation_jobs (server_id, agent_id, kind, operation, request_payload)
		VALUES ($1::uuid, $2::uuid, 'vpn_core_service', 'restart', '{"path":"/tmp"}'::jsonb)
	`, serverID, agentID); err == nil {
		t.Fatal("non-maintenance Agent operation accepted a request payload")
	}
}

func TestRuntimeMetricsBackfillMigrationRepairsAppliedSchemaDrift(t *testing.T) {
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
	preRuntimeDir := copyMigrationsBefore(t, "../../migrations", "000112_agent_runtime_metrics.up.sql")
	if err := Migrate(ctx, pool, preRuntimeDir, logger); err != nil {
		t.Fatalf("apply pre-runtime migrations: %v", err)
	}

	var serverID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO servers (name, status)
		VALUES ('Legacy runtime fixture', 'active')
		RETURNING id::text
	`).Scan(&serverID); err != nil {
		t.Fatalf("create legacy server: %v", err)
	}

	now := time.Now().UTC()
	collectedAt := "2026-08-11T20:00:00Z"
	if _, err := pool.Exec(ctx, `
		INSERT INTO agents (
			server_id,
			hostname,
			os,
			arch,
			agent_version,
			token_hash,
			capabilities,
			status,
			registered_at,
			last_seen_at
		)
		VALUES (
			$1::uuid,
			'legacy-runtime-agent',
			'linux',
			'amd64',
			'legacy-test',
			'legacy-runtime-token-hash',
			jsonb_build_object(
				'vpnCore', true,
				'runtimeMetrics', jsonb_build_object(
					'load1', 1.25,
					'load5', 0.75,
					'load15', 0.5,
					'logicalCpus', 4,
					'collectedAt', $2::text
				)
			),
			$3,
			$4,
			$4
		)
	`, serverID, collectedAt, agents.StatusOnline, now); err != nil {
		t.Fatalf("create legacy agent: %v", err)
	}

	var legacyRuntimeBlock bool
	if err := pool.QueryRow(ctx, `
		SELECT capabilities ? 'runtimeMetrics'
		FROM agents
		WHERE server_id = $1::uuid
	`, serverID).Scan(&legacyRuntimeBlock); err != nil {
		t.Fatalf("read legacy capabilities: %v", err)
	}
	if !legacyRuntimeBlock {
		t.Fatal("fixture must contain legacy runtimeMetrics before migration 112")
	}

	preBackfillDir := copyMigrationsBefore(t, "../../migrations", "000114_agent_runtime_metrics_backfill.up.sql")
	if err := Migrate(ctx, pool, preBackfillDir, logger); err != nil {
		t.Fatalf("apply migrations through 113: %v", err)
	}

	var versionBeforeBackfill string
	if err := pool.QueryRow(ctx, `SELECT version FROM schema_migrations ORDER BY applied_at DESC, version DESC LIMIT 1`).Scan(&versionBeforeBackfill); err != nil {
		t.Fatalf("read schema version before backfill: %v", err)
	}
	if versionBeforeBackfill != "000113_delivery_foundation" {
		t.Fatalf("schema version before backfill = %q, want 000113_delivery_foundation", versionBeforeBackfill)
	}
	if err := pool.QueryRow(ctx, `
		SELECT capabilities ? 'runtimeMetrics'
		FROM agents
		WHERE server_id = $1::uuid
	`, serverID).Scan(&legacyRuntimeBlock); err != nil {
		t.Fatalf("read capabilities after migration 113: %v", err)
	}
	if !legacyRuntimeBlock {
		t.Fatal("legacy row must remain untouched before migration 114")
	}

	if _, err := pool.Exec(ctx, `
		DROP TRIGGER IF EXISTS agents_extract_runtime_metrics ON agents;
		DROP FUNCTION IF EXISTS routegate_extract_agent_runtime_metrics();
	`); err != nil {
		t.Fatalf("simulate applied-112 extractor drift: %v", err)
	}

	if err := Migrate(ctx, pool, "../../migrations", logger); err != nil {
		t.Fatalf("apply self-contained runtime backfill migration: %v", err)
	}

	var (
		hasRuntimeBlock bool
		staticVPNCore   bool
		load1           float64
		load5           float64
		load15          float64
		logicalCPUs     int
		runtimeTime     time.Time
		triggerPresent  bool
	)
	if err := pool.QueryRow(ctx, `
		SELECT
			capabilities ? 'runtimeMetrics',
			COALESCE((capabilities ->> 'vpnCore')::boolean, false),
			runtime_load_1,
			runtime_load_5,
			runtime_load_15,
			runtime_logical_cpus,
			runtime_collected_at,
			EXISTS (
				SELECT 1
				FROM pg_trigger
				WHERE tgrelid = 'agents'::regclass
				  AND tgname = 'agents_extract_runtime_metrics'
				  AND NOT tgisinternal
			)
		FROM agents
		WHERE server_id = $1::uuid
	`, serverID).Scan(
		&hasRuntimeBlock,
		&staticVPNCore,
		&load1,
		&load5,
		&load15,
		&logicalCPUs,
		&runtimeTime,
		&triggerPresent,
	); err != nil {
		t.Fatalf("read repaired runtime metrics: %v", err)
	}

	if hasRuntimeBlock {
		t.Fatal("runtimeMetrics must be removed from durable capabilities")
	}
	if !staticVPNCore {
		t.Fatal("static capabilities must survive runtime backfill")
	}
	if !triggerPresent {
		t.Fatal("migration 114 must restore the canonical runtime extractor trigger")
	}
	if load1 != 1.25 || load5 != 0.75 || load15 != 0.5 || logicalCPUs != 4 {
		t.Fatalf("unexpected backfilled runtime values: load1=%v load5=%v load15=%v cpus=%d", load1, load5, load15, logicalCPUs)
	}
	if runtimeTime.UTC().Format(time.RFC3339) != collectedAt {
		t.Fatalf("runtime collectedAt = %s, want %s", runtimeTime.UTC().Format(time.RFC3339), collectedAt)
	}

	var migratedDeploymentRole string
	if err := pool.QueryRow(ctx, `SELECT deployment_role FROM servers WHERE id = $1::uuid`, serverID).Scan(&migratedDeploymentRole); err != nil {
		t.Fatalf("read migrated deployment role: %v", err)
	}
	if migratedDeploymentRole != "hybrid" {
		t.Fatalf("pre-RG-114 server deployment role = %q, want hybrid", migratedDeploymentRole)
	}

	version, err := NewSchemaVersionRepository(pool).AppliedSchemaVersion(ctx)
	if err != nil {
		t.Fatalf("read applied schema version: %v", err)
	}
	if version != "000178_retired_vless_credentials" {
		t.Fatalf("applied schema version = %q, want 000178_retired_vless_credentials", version)
	}
}

func resetPublicSchema(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(ctx, `DROP SCHEMA IF EXISTS routegate_dispatch CASCADE; DROP SCHEMA public CASCADE; CREATE SCHEMA public;`); err != nil {
		t.Fatalf("reset public schema: %v", err)
	}
}

func copyMigrationsBefore(t *testing.T, sourceDir, stopBefore string) string {
	t.Helper()
	destination := t.TempDir()
	files, err := filepath.Glob(filepath.Join(sourceDir, "*.up.sql"))
	if err != nil {
		t.Fatalf("list migrations: %v", err)
	}
	for _, file := range files {
		base := filepath.Base(file)
		if base >= stopBefore {
			continue
		}
		content, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read migration %s: %v", base, err)
		}
		if err := os.WriteFile(filepath.Join(destination, base), content, 0o600); err != nil {
			t.Fatalf("copy migration %s: %v", base, err)
		}
	}
	return destination
}
