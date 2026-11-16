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
// Check the SQL's dollar-quoted PL/pgSQL functions without requiring a
// database. This catches accidental damage to the source when editing the
// reservation trigger/guard, before Manager startup or runtime testing.
func TestCredentialRevocationReservationSQLQuoting(t *testing.T) {
    raw, err := os.ReadFile("../../migrations/000161_credential_revocation_preparations.up.sql")
    if err != nil {
        t.Fatal(err)
    }
    source := string(raw)
    for _, name := range []string{"routegate_revocation_ledger_guard", "routegate_revocation_routing_guard"} {
        prefix := "CREATE FUNCTION " + name + "() RETURNS trigger LANGUAGE plpgsql AS " + string([]byte{36, 36})
        if strings.Count(source, prefix) != 1 {
            t.Errorf("%s must start with a valid PostgreSQL double-dollar quote", name)
        }
    }
    if strings.Contains(source, "LANGUAGE plpgsql AS "+string([]byte{36})+"\n") {
        t.Fatal("single-dollar PL/pgSQL quote is invalid")
    }
}

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

    const preparationSQL = `
            INSERT INTO vpn_credential_revocations (
                vpn_account_id, server_id, agent_id, agent_credential_generation,
                baseline_version_id, baseline_hash, baseline_runtime_hash,
                candidate_runtime_hash, candidate_hash, candidate_config,
                account_status, requested_by, last_action_by
            ) VALUES (
                $1::uuid, $2::uuid, $3::uuid, 1, $4::uuid,
                $5, $6, $7, $8, '{}'::jsonb, 'active', 'ci-reviewer', 'ci-reviewer'
            ) RETURNING id::text
        `
    preparationArgs := []any{accountID, nodeID, agentID, baselineID,
        strings.Repeat("a", 64), strings.Repeat("b", 64),
        strings.Repeat("c", 64), strings.Repeat("d", 64)}
    insertPreparation := func(queryCtx context.Context) (string, error) {
        var id string
        err := pool.QueryRow(queryCtx, preparationSQL, preparationArgs...).Scan(&id)
        return id, err
    }
    // A client-side context deadline can race the database commit. An explicit
    // PostgreSQL lock_timeout inside its *own* transaction guarantees the
    // blocked SQL statement is rejected by PostgreSQL before releasing the
    // other transaction's lock. Do not confuse client cancellation with
    // transactional safety.
    runWithServerLockTimeout := func(statement string, args ...any) error {
        attempted, err := pool.Begin(ctx)
        if err != nil {
            return err
        }
        defer attempted.Rollback(ctx)
        if _, err := attempted.Exec(ctx, "SET LOCAL lock_timeout = '350ms'"); err != nil {
            return err
        }
        _, err = attempted.Exec(ctx, statement, args...)
        return err
    }

    // Pending work at this node must be rejected at the DB boundary,
    // not merely at a future Manager handler that has not been wired yet.
    var applyJobID string
    if err := pool.QueryRow(ctx, `
        INSERT INTO config_apply_jobs (server_id, agent_id, config_version_id, action, status)
        VALUES ($1::uuid, $2::uuid, $3::uuid, 'apply', 'pending')
        RETURNING id::text
    `, nodeID, agentID, baselineID).Scan(&applyJobID); err != nil {
        t.Fatalf("create pending existing apply: %v", err)
    }
    if _, err := insertPreparation(ctx); err == nil ||
        !strings.Contains(err.Error(), "revocation_node_mutation_unresolved") {
        t.Fatalf("preparation was not refused while config Apply pending: %v", err)
    }
    if _, err := pool.Exec(ctx, "DELETE FROM config_apply_jobs WHERE id=$1::uuid", applyJobID); err != nil {
        t.Fatalf("remove disposable pending job: %v", err)
    }

    var targetID, transferID string
    if err := pool.QueryRow(ctx, `
        INSERT INTO servers (name, status) VALUES ('rg140-reservation-transfer-target', 'active')
        RETURNING id::text
    `).Scan(&targetID); err != nil {
        t.Fatal(err)
    }
    if err := pool.QueryRow(ctx, `
        INSERT INTO vpn_account_transfers (
            vpn_account_id, source_server_id, target_server_id, source_version_id,
            state, requested_by, last_action_by
        ) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid,
            'preparing', 'ci-reviewer', 'ci-reviewer')
        RETURNING id::text
    `, accountID, nodeID, targetID, baselineID).Scan(&transferID); err != nil {
        t.Fatalf("create pending staged transfer: %v", err)
    }
    if _, err := pool.Exec(ctx, `
        INSERT INTO vpn_account_transfer_nodes (server_id, transfer_id)
        VALUES ($1::uuid, $2::uuid)
    `, nodeID, transferID); err != nil {
        t.Fatalf("reserve source node for staged transfer: %v", err)
    }
    if _, err := insertPreparation(ctx); err == nil ||
        !strings.Contains(err.Error(), "revocation_node_transfer_reserved") {
        t.Fatalf("preparation was not refused during an active transfer: %v", err)
    }
    if _, err := pool.Exec(ctx, "DELETE FROM vpn_account_transfer_nodes WHERE transfer_id=$1::uuid", transferID); err != nil {
        t.Fatalf("release disposable transfer reservation: %v", err)
    }
    if _, err := pool.Exec(ctx, "DELETE FROM vpn_account_transfers WHERE id=$1::uuid", transferID); err != nil {
        t.Fatalf("remove disposable staged transfer fixture: %v", err)
    }
    var unapprovedPreparations int
    if err := pool.QueryRow(ctx, "SELECT count(*) FROM vpn_credential_revocations").Scan(&unapprovedPreparations); err != nil {
        t.Fatal(err)
    }
    if unapprovedPreparations != 0 {
        t.Fatalf("failed admission wrote %d preparation rows", unapprovedPreparations)
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
    err = runWithServerLockTimeout(preparationSQL, preparationArgs...)
    if err == nil || !strings.Contains(err.Error(), "SQLSTATE 55P03") {
        t.Fatalf("preparation did not receive PostgreSQL lock_timeout against Manager apply: %v", err)
    }
    if err := tx.Commit(ctx); err != nil {
        t.Fatal(err)
    }
    // Server-side lock_timeout must leave no partially committed operation.
    var silentlyCommitted int
    if err := pool.QueryRow(ctx, "SELECT count(*) FROM vpn_credential_revocations WHERE state='prepared'").Scan(&silentlyCommitted); err != nil {
        t.Fatal(err)
    }
    if silentlyCommitted != 0 {
        t.Fatalf("timed-out preparation unexpectedly committed %d operations", silentlyCommitted)
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
    err = runWithServerLockTimeout(`
        INSERT INTO routing_profiles (name) VALUES ('rg140-global-route-blocked-at-lock')
    `)
    if err == nil || !strings.Contains(err.Error(), "SQLSTATE 55P03") {
        t.Fatalf("routing mutation did not receive PostgreSQL lock_timeout against preparation: %v", err)
    }
    if err := routingTx.Commit(ctx); err != nil {
        t.Fatal(err)
    }
    var prematurelyCommitted int
    if err := pool.QueryRow(ctx, `
        SELECT count(*) FROM routing_profiles
        WHERE name='rg140-global-route-blocked-at-lock'
    `).Scan(&prematurelyCommitted); err != nil {
        t.Fatal(err)
    }
    if prematurelyCommitted != 0 {
        t.Fatalf("timed-out routing edit unexpectedly committed %d records", prematurelyCommitted)
    }
    if _, err := pool.Exec(ctx, `
        INSERT INTO routing_profiles (name) VALUES ('rg140-global-route-after-cancel')
    `); err != nil {
        t.Fatalf("routing must recover after reservation is cancelled and lock released: %v", err)
    }
}
