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

// The preparation ledger is dormant: this verifies PostgreSQL transaction
// ordering, not runtime credential removal or an authorized Manager command.
//
// An unprotected SELECT EXISTS on the prepared ledger can miss a concurrent
// uncommitted insert. Preparation, Manager apply and global routing edits
// therefore share one advisory transaction lock before they inspect state.
func TestCredentialRevocationGlobalReservationSerializesPreparationAndRouting(t *testing.T) {
    databaseURL := os.Getenv("ROUTEGATE_TEST_DATABASE_URL")
    if databaseURL == "" {
        t.Skip("ROUTEGATE_TEST_DATABASE_URL is not set")
    }
    ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
    defer cancel()
    logger := slog.New(slog.NewTextHandler(io.Discard, nil))
    pool, err := Connect(ctx, databaseURL, logger)
    if err != nil {
        t.Fatalf("connect to disposable PostgreSQL: %v", err)
    }
    defer pool.Close()
    resetPublicSchema(t, ctx, pool)
    if err := Migrate(ctx, pool, "../../migrations", logger); err != nil {
        t.Fatalf("create schema with dormant revocation preparation: %v", err)
    }
    var nodeID, agentID, accountID, baselineID string
    if err := pool.QueryRow(ctx, `
        INSERT INTO servers (name, status)
        VALUES ('rg140-reservation-serializable-fixture', 'active')
        RETURNING id::text
    `).Scan(&nodeID); err != nil {
        t.Fatal(err)
    }
    if err := pool.QueryRow(ctx, `
        INSERT INTO agents (server_id, agent_version, token_hash, status)
        VALUES ($1::uuid, 'ci', 'rg140-reservation-serializable-token', 'online')
        RETURNING id::text
    `, nodeID).Scan(&agentID); err != nil {
        t.Fatal(err)
    }
    if err := pool.QueryRow(ctx, `
        INSERT INTO vpn_accounts (server_id, username, display_name, protocol, status)
        VALUES ($1::uuid, 'rg140-reservation-serializable', 'Reservation fixture', 'sing-box', 'active')
        RETURNING id::text
    `, nodeID).Scan(&accountID); err != nil {
        t.Fatal(err)
    }
    if err := pool.QueryRow(ctx, `
        INSERT INTO config_versions (server_id, version, config_hash, status, rendered_config)
        VALUES ($1::uuid, 1, 'fixture-before-preparation', 'applied', '{}'::jsonb)
        RETURNING id::text
    `, nodeID).Scan(&baselineID); err != nil {
        t.Fatal(err)
    }

    insertPreparation := func(queryCtx context.Context) (string, error) {
        var id string
        err := pool.QueryRow(queryCtx, `
            INSERT INTO vpn_credential_revocations (
                vpn_account_id, server_id, agent_id, agent_credential_generation,
                baseline_version_id, baseline_hash, baseline_runtime_hash,
                candidate_runtime_hash, candidate_hash, candidate_config,
                account_status, requested_by, last_action_by
            ) VALUES (
                $1::uuid, $2::uuid, $3::uuid, 1, $4::uuid,
                $5, $6, $7, $8, '{}'::jsonb, 'active', 'ci-reviewer', 'ci-reviewer'
            ) RETURNING id::text
        `, accountID, nodeID, agentID, baselineID,
            strings.Repeat("a", 64), strings.Repeat("b", 64),
            strings.Repeat("c", 64), strings.Repeat("d", 64)).Scan(&id)
        return id, err
    }

    // Simulate a concurrent Manager apply holding the shared global lock.
    tx, err := pool.Begin(ctx)
    if err != nil {
        t.Fatal(err)
    }
    defer tx.Rollback(ctx)
    if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(140159)"); err != nil {
        t.Fatal(err)
    }
    shortCtx, shortCancel := context.WithTimeout(ctx, 350*time.Millisecond)
    _, err = insertPreparation(shortCtx)
    shortCancel()
    if err == nil {
        t.Fatal("preparation raced ahead of a concurrent Manager apply reservation")
    }
    if err := tx.Commit(ctx); err != nil {
        t.Fatal(err)
    }

    operationID, err := insertPreparation(ctx)
    if err != nil {
        t.Fatalf("preparation must succeed after apply lock releases: %v", err)
    }
    var auditCount int
    if err := pool.QueryRow(ctx, `
        SELECT count(*) FROM vpn_credential_revocation_events
        WHERE operation_id=$1::uuid AND action='prepared'
    `, operationID).Scan(&auditCount); err != nil || auditCount != 1 {
        t.Fatalf("prepared operation is missing its one audit event: count=%d err=%v", auditCount, err)
    }
    if _, err := pool.Exec(ctx, `
        INSERT INTO routing_profiles (name) VALUES ('rg140-reserved-global-route')
    `); err == nil {
        t.Fatal("routing edit was allowed while a revocation was prepared")
    }

    if _, err := pool.Exec(ctx, `
        UPDATE vpn_credential_revocations
        SET state='cancelled', cancelled_at=now(), last_action_by='ci-reviewer'
        WHERE id=$1::uuid
    `, operationID); err != nil {
        t.Fatalf("cancel dormant preparation for routing test: %v", err)
    }

    // With no prepared operation, a concurrent *future* preparation must
    // still serialize against a global routing mutation before the latter
    // checks an empty ledger. Otherwise it can race ahead of the lock.
    routingTx, err := pool.Begin(ctx)
    if err != nil {
        t.Fatal(err)
    }
    defer routingTx.Rollback(ctx)
    if _, err := routingTx.Exec(ctx, "SELECT pg_advisory_xact_lock(140159)"); err != nil {
        t.Fatal(err)
    }
    routingCtx, routingCancel := context.WithTimeout(ctx, 350*time.Millisecond)
    _, err = pool.Exec(routingCtx, `
        INSERT INTO routing_profiles (name) VALUES ('rg140-global-route-after-cancel')
    `)
    routingCancel()
    if err == nil {
        t.Fatal("global routing mutation bypassed an in-flight preparation lock")
    }
    if err := routingTx.Commit(ctx); err != nil {
        t.Fatal(err)
    }
    if _, err := pool.Exec(ctx, `
        INSERT INTO routing_profiles (name) VALUES ('rg140-global-route-after-cancel')
    `); err != nil {
        t.Fatalf("routing must recover after reservation is cancelled and lock released: %v", err)
    }
}
