package db

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ikaevus/routegate/backend/internal/recoveryobserve"
	"github.com/jackc/pgx/v5/pgconn"
)

// All tests use disposable PostgreSQL schemas. This migration has no Agent
// dispatcher caller, application API or runtime unlock, and never touches
// an installed node.
func pendingCheckpointConfigFixture(t *testing.T) (recoveryReservationFixture, string) {
	t.Helper()
	f := newRecoveryReservationFixture(t)
	f.enableFencing(t)
	var jobID string
	err := f.pool.QueryRow(f.ctx, `
INSERT INTO config_apply_jobs(server_id,agent_id,config_version_id,action,status)
VALUES($1::uuid,$2::uuid,$3::uuid,'apply','pending')
RETURNING id::text
`, f.serverID, f.agentID, f.configVersionID).Scan(&jobID)
	if err != nil {
		f.done()
		t.Fatal("create isolated pending job", err)
	}
	return f, jobID
}

func checkpointInsert(ctx context.Context, f recoveryReservationFixture, jobID, kind string) (string, error) {
	var id string
	err := f.pool.QueryRow(ctx, `
INSERT INTO vpn_runtime_pre_dispatch_checkpoints(task_id,task_kind)
VALUES($1::uuid,$2) RETURNING id::text
`, jobID, kind).Scan(&id)
	return id, err
}

func requirePreDispatchGuard(t *testing.T, err error) {
	t.Helper()
	var pg *pgconn.PgError
	if !errors.As(err, &pg) || pg.Code != "P0146" {
		t.Fatalf("expected safe pre-dispatch checkpoint guard P0146, got %v", err)
	}
}

func TestRuntimePreDispatchCheckpointDerivesManagerOwnedJobAndAtomicAudit(t *testing.T) {
	f, jobID := pendingCheckpointConfigFixture(t)
	defer f.done()

	id, err := checkpointInsert(f.ctx, f, jobID, "config_apply")
	if err != nil {
		t.Fatal("committing Manager checkpoint:", err)
	}
	var taskID, serverID, agentID, marker, configVersion, configHash, state string
	var generation, epoch int64
	var eventCount int
	err = f.pool.QueryRow(f.ctx, `
SELECT task_id::text,server_id::text,agent_id::text,
       agent_credential_generation,dispatch_epoch,expected_marker_sha256,
       config_version_id::text,expected_config_sha256,state,
       (SELECT count(*) FROM vpn_runtime_pre_dispatch_checkpoint_events
        WHERE checkpoint_id=c.id AND action='committed')
  FROM vpn_runtime_pre_dispatch_checkpoints c WHERE id=$1::uuid
`, id).Scan(&taskID, &serverID, &agentID, &generation, &epoch, &marker,
		&configVersion, &configHash, &state, &eventCount)
	if err != nil {
		t.Fatal(err)
	}
	var expectedConfigHash string
	if err := f.pool.QueryRow(f.ctx,
		`SELECT config_hash FROM config_versions WHERE id=$1::uuid`, f.configVersionID,
	).Scan(&expectedConfigHash); err != nil {
		t.Fatal(err)
	}
	wantMarker, err := recoveryobserve.ManagerExpectedMutationMarkerV1(recoveryobserve.ManagerJobIntentV1{
		TaskID: jobID, AgentID: f.agentID, ServerID: f.serverID,
		Kind: "config_apply", AgentCredentialGeneration: f.generation,
	})
	if err != nil || taskID != jobID || serverID != f.serverID ||
		agentID != f.agentID || generation != f.generation || epoch <= 0 ||
		marker != wantMarker || configVersion != f.configVersionID ||
		configHash != expectedConfigHash || eventCount != 1 ||
		state != "pre_dispatch_unattested" {
		t.Fatalf("checkpoint not anchored to Manager job before dispatch: task=%s node=%s agent=%s gen=%d epoch=%d marker=%s cfg=%s hash=%s state=%s audit=%d err=%v",
			taskID, serverID, agentID, generation, epoch, marker, configVersion, configHash, state, eventCount, err)
	}

	// The source job may be claimed after commit, without changing identity.
	if _, err := f.pool.Exec(f.ctx, `
UPDATE config_apply_jobs SET status='in_progress',started_at=now()
 WHERE id=$1::uuid
`, jobID); err != nil {
		t.Fatal("pre-dispatch checkpoint unexpectedly broke normal job claim:", err)
	}
	var started bool
	if err := f.pool.QueryRow(f.ctx,
		`SELECT status='in_progress' AND started_at IS NOT NULL FROM config_apply_jobs WHERE id=$1::uuid`, jobID,
	).Scan(&started); err != nil || !started {
		t.Fatal("job status did not advance independently", err)
	}
}

func TestRuntimePreDispatchCheckpointRefusesSelfSuppliedEvidenceAndLateJobs(t *testing.T) {
	for _, mode := range []string{"supplied_marker", "supplied_agent", "supplied_generation", "supplied_config_hash", "unknown_job", "completed_job", "already_claimed", "legacy_unfenced"} {
		t.Run(mode, func(t *testing.T) {
			f := newRecoveryReservationFixture(t)
			defer f.done()
			if mode != "legacy_unfenced" {
				f.enableFencing(t)
			}
			var jobID string
			if err := f.pool.QueryRow(f.ctx, `
INSERT INTO config_apply_jobs(server_id,agent_id,config_version_id,action,status)
VALUES($1::uuid,$2::uuid,$3::uuid,'apply','pending') RETURNING id::text
`, f.serverID, f.agentID, f.configVersionID).Scan(&jobID); err != nil {
				t.Fatal(err)
			}
			if mode == "completed_job" {
				jobID = f.jobID
			}
			if mode == "already_claimed" {
				if _, err := f.pool.Exec(f.ctx,
					`UPDATE config_apply_jobs SET status='in_progress',started_at=now() WHERE id=$1::uuid`, jobID,
				); err != nil { t.Fatal(err) }
			}
			if mode == "unknown_job" {
				jobID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
			}
			column, value := "", ""
			switch mode {
			case "supplied_marker":
				column, value = "expected_marker_sha256", strings.Repeat("a", 64)
			case "supplied_agent":
				column, value = "agent_id", f.agentID
			case "supplied_generation":
				column, value = "agent_credential_generation", "5"
			case "supplied_config_hash":
				column, value = "expected_config_sha256", strings.Repeat("b", 64)
			}
			var err error
			if column != "" {
				// Column names come exclusively from the static test cases.
				query := "INSERT INTO vpn_runtime_pre_dispatch_checkpoints(task_id,task_kind," +
					column + ") VALUES($1::uuid,'config_apply',$2) "
				if column == "agent_id" {
					query += "RETURNING id"
					_, err = f.pool.Exec(f.ctx, query, jobID, value)
				} else {
					_, err = f.pool.Exec(f.ctx, query, jobID, value)
				}
			} else {
				_, err = checkpointInsert(f.ctx, f, jobID, "config_apply")
			}
			requirePreDispatchGuard(t, err)
			var n int
			if err := f.pool.QueryRow(f.ctx,
				`SELECT count(*) FROM vpn_runtime_pre_dispatch_checkpoints`,
			).Scan(&n); err != nil || n != 0 {
				t.Fatalf("invalid checkpoint was persisted: count=%d err=%v", n, err)
			}
		})
	}
}

func TestRuntimePreDispatchCheckpointServiceRestartUsesManagerJobIdentity(t *testing.T) {
	f := newRecoveryReservationFixture(t)
	defer f.done()
	f.enableFencing(t)
	var jobID string
	if err := f.pool.QueryRow(f.ctx, `
INSERT INTO agent_operation_jobs(server_id,agent_id,kind,operation,status)
VALUES($1::uuid,$2::uuid,'vpn_core_service','restart','pending')
RETURNING id::text
`, f.serverID, f.agentID).Scan(&jobID); err != nil {
		t.Fatal(err)
	}
	id, err := checkpointInsert(f.ctx, f, jobID, "vpn_core_service")
	if err != nil {
		t.Fatal(err)
	}
	var marker string
	var noConfig bool
	err = f.pool.QueryRow(f.ctx, `
SELECT expected_marker_sha256, config_version_id IS NULL AND expected_config_sha256 IS NULL
  FROM vpn_runtime_pre_dispatch_checkpoints WHERE id=$1::uuid
`, id).Scan(&marker, &noConfig)
	expected, expectedErr := recoveryobserve.ManagerExpectedMutationMarkerV1(recoveryobserve.ManagerJobIntentV1{
		TaskID: jobID, AgentID: f.agentID, ServerID: f.serverID,
		Kind: "vpn_core_service", AgentCredentialGeneration: f.generation,
	})
	if err != nil || expectedErr != nil || marker != expected || !noConfig {
		t.Fatalf("service checkpoint drift: %q want %q no-config=%v err=%v %v", marker, expected, noConfig, err, expectedErr)
	}
	// A normal status update is still permitted, but operation identity is not.
	if _, err := f.pool.Exec(f.ctx,
		`UPDATE agent_operation_jobs SET status='in_progress',started_at=now() WHERE id=$1::uuid`, jobID,
	); err != nil { t.Fatal("normal job claim refused:",err) }
	_, err = f.pool.Exec(f.ctx,
		`UPDATE agent_operation_jobs SET operation='stop' WHERE id=$1::uuid`, jobID)
	requirePreDispatchGuard(t, err)
}

func TestRuntimePreDispatchCheckpointIsImmutableAndProtectsSource(t *testing.T) {
	f, jobID := pendingCheckpointConfigFixture(t)
	defer f.done()
	id, err := checkpointInsert(f.ctx, f, jobID, "config_apply")
	if err != nil { t.Fatal(err) }
	for _, stmt := range []string{
		`UPDATE vpn_runtime_pre_dispatch_checkpoints SET state='pre_dispatch_unattested' WHERE id=$1::uuid`,
		`DELETE FROM vpn_runtime_pre_dispatch_checkpoints WHERE id=$1::uuid`,
		`UPDATE config_apply_jobs SET agent_id=NULL WHERE id=$1::uuid`,
		`UPDATE config_apply_jobs SET action='rollback' WHERE id=$1::uuid`,
		`UPDATE config_apply_jobs SET config_version_id=NULL WHERE id=$1::uuid`,
		`DELETE FROM config_apply_jobs WHERE id=$1::uuid`,
	} {
		targetID := jobID
		if strings.Contains(stmt, "vpn_runtime_pre_dispatch_checkpoints") {
			targetID = id
		}
		_, err := f.pool.Exec(f.ctx, stmt, targetID)
		requirePreDispatchGuard(t, err)
	}
	_, err = f.pool.Exec(f.ctx,
		`UPDATE config_versions SET config_hash=repeat('a',64) WHERE id=$1::uuid`, f.configVersionID)
	requirePreDispatchGuard(t, err)
	_, err = f.pool.Exec(f.ctx,
		`DELETE FROM config_versions WHERE id=$1::uuid`, f.configVersionID)
	requirePreDispatchGuard(t, err)
	for _, sql := range []string{
		`UPDATE vpn_runtime_pre_dispatch_checkpoint_events SET action='committed'`,
		`DELETE FROM vpn_runtime_pre_dispatch_checkpoint_events`,
		`TRUNCATE vpn_runtime_pre_dispatch_checkpoints CASCADE`,
		`TRUNCATE vpn_runtime_pre_dispatch_checkpoint_events`,
	} {
		_, err := f.pool.Exec(f.ctx, sql)
		requirePreDispatchGuard(t, err)
	}
	down, err := os.ReadFile("../../migrations/000166_runtime_pre_dispatch_checkpoint.down.sql")
	if err != nil { t.Fatal(err) }
	if _, err := f.pool.Exec(f.ctx, string(down)); err == nil ||
		!strings.Contains(err.Error(), "pre-dispatch checkpoint history") {
		t.Fatal("destructive downgrade erased checkpoint", err)
	}
	var checkpoints, events int
	if err := f.pool.QueryRow(f.ctx,
		`SELECT (SELECT count(*) FROM vpn_runtime_pre_dispatch_checkpoints),
		        (SELECT count(*) FROM vpn_runtime_pre_dispatch_checkpoint_events)`,
	).Scan(&checkpoints, &events); err != nil || checkpoints != 1 || events != 1 {
		t.Fatalf("checkpoint and audit not retained: %d/%d %v", checkpoints, events, err)
	}
}

func TestRuntimePreDispatchCheckpointAuditFailureRollsBackCommit(t *testing.T) {
	f, jobID := pendingCheckpointConfigFixture(t)
	defer f.done()
	if _, err := f.pool.Exec(f.ctx, `
CREATE FUNCTION rg140_fail_checkpoint_audit() RETURNS trigger LANGUAGE plpgsql AS
'BEGIN RAISE EXCEPTION ''injected pre-dispatch checkpoint audit failure''; END'
`); err != nil { t.Fatal(err) }
	if _, err := f.pool.Exec(f.ctx, `
CREATE TRIGGER rg140_checkpoint_audit_fail
BEFORE INSERT ON vpn_runtime_pre_dispatch_checkpoint_events
FOR EACH ROW EXECUTE FUNCTION rg140_fail_checkpoint_audit()
`); err != nil { t.Fatal(err) }
	if _, err := checkpointInsert(f.ctx, f, jobID, "config_apply"); err == nil {
		t.Fatal("checkpoint committed despite missing audit")
	}
	var checkpoints, events int
	if err := f.pool.QueryRow(f.ctx,
		`SELECT (SELECT count(*) FROM vpn_runtime_pre_dispatch_checkpoints),
		        (SELECT count(*) FROM vpn_runtime_pre_dispatch_checkpoint_events)`,
	).Scan(&checkpoints, &events); err != nil || checkpoints != 0 || events != 0 {
		t.Fatalf("audit failure did not roll back both tables: %d/%d %v", checkpoints, events, err)
	}
}

func TestRuntimePreDispatchCheckpointConcurrentInsertIsSingleUse(t *testing.T) {
	f, jobID := pendingCheckpointConfigFixture(t)
	defer f.done()
	ctx, cancel := context.WithTimeout(f.ctx, 12*time.Second)
	defer cancel()
	start := make(chan struct{})
	out := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := checkpointInsert(ctx, f, jobID, "config_apply")
			out <- err
		}()
	}
	close(start)
	wg.Wait()
	close(out)
	ok, duplicate := 0, 0
	for err := range out {
		if err == nil {
			ok++
			continue
		}
		var pg *pgconn.PgError
		if errors.As(err, &pg) && pg.Code == "23505" {
			duplicate++
		} else {
			t.Fatalf("unexpected competing checkpoint result: %v", err)
		}
	}
	if ok != 1 || duplicate != 1 {
		t.Fatalf("concurrent checkpoint was replayed: success=%d duplicate=%d", ok, duplicate)
	}
	var checkpoints, audit int
	if err := f.pool.QueryRow(f.ctx,
		`SELECT (SELECT count(*) FROM vpn_runtime_pre_dispatch_checkpoints),
		        (SELECT count(*) FROM vpn_runtime_pre_dispatch_checkpoint_events)`,
	).Scan(&checkpoints, &audit); err != nil || checkpoints != 1 || audit != 1 {
		t.Fatalf("concurrency left inconsistent checkpoint/audit: %d/%d %v", checkpoints, audit, err)
	}
}

func TestRuntimePreDispatchCheckpointEmptySchemaDowngradesWithoutTouchingJobs(t *testing.T) {
	f, _ := pendingCheckpointConfigFixture(t)
	defer f.done()
	down, err := os.ReadFile("../../migrations/000166_runtime_pre_dispatch_checkpoint.down.sql")
	if err != nil { t.Fatal(err) }
	if _, err := f.pool.Exec(f.ctx, string(down)); err != nil {
		t.Fatal("empty checkpoint schema cannot downgrade:", err)
	}
	up, err := os.ReadFile("../../migrations/000166_runtime_pre_dispatch_checkpoint.up.sql")
	if err != nil { t.Fatal(err) }
	if _, err := f.pool.Exec(f.ctx, string(up)); err != nil {
		t.Fatal("checkpoint schema cannot be reapplied:", err)
	}
}
