package db

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// These private, read-only preflights are NOT connected to Agent dispatch,
// admission, enrollment activation, runtime release, or HTTP. A Manager DB
// snapshot cannot attest the Agent host. All returned observations are
// historical and are ALWAYS manual_review_required_unattested.
var errRecoveryEnrollmentPreflightRefusedV1 = errors.New("recovery enrollment preflight refused")

const recoveryEnrollmentManualReviewUnattestedV1 = "manual_review_required_unattested"

type recoveryEnrollmentPreflightV1 struct {
	Assessment                     string
	AgentID                        string
	ServerID                       string
	ManagerCredentialGeneration    int64
	PreparationID                  string
	PreparedCredentialGeneration   int64
	PreparationPhase               string
	AuditEvents                    int64
	CheckpointCount                int64
	CheckpointCredentialGeneration int64
	CheckpointEpoch                int64
	CheckpointMarkerSHA256         string
	OutstandingManagerJobs         int64
	RecoveryHolds                  int64
	Reasons                        []string
	Unattested                     bool
}

// inspectRecoveryEnrollmentPreflightV1 uses one PostgreSQL REPEATABLE READ
// READ ONLY transaction so every item comes from the same Manager snapshot.
// No SELECT FOR UPDATE is used: a read-only historical observation must
// NEVER be mistaken for a lock that survives this function return.
//
// Even if there are no blockers except "local_journal_unverified" and
// "independent_host_provenance_unverified", this is not an approval.
// Checkpoint rows also are NOT evidence that the pending→dispatched
// transition checked them, or that the Agent wrote its local journal.
func inspectRecoveryEnrollmentPreflightV1(
	ctx context.Context,
	pool *pgxpool.Pool,
	agentID string,
) (recoveryEnrollmentPreflightV1, error) {
	deny := func() (recoveryEnrollmentPreflightV1, error) {
		return recoveryEnrollmentPreflightV1{}, errRecoveryEnrollmentPreflightRefusedV1
	}
	if ctx == nil || ctx.Err() != nil || pool == nil || strings.TrimSpace(agentID) == "" {
		return deny()
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{
		IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly,
	})
	if err != nil {
		return deny()
	}
	defer tx.Rollback(ctx)

	var snap recoveryEnrollmentPreflightV1
	var status string
	var fencing bool
	var preparedServerID string
	var activeConfigJobs, activeOperationJobs, activePlatformJobs int64
	// This SQL is deliberately fixed: the caller supplies only Agent ID.
	// A user-supplied token, journal hash or checkpoint does not enter the
	// query and cannot become a Manager-owned fact.
	err = tx.QueryRow(ctx, `
SELECT COALESCE(a.server_id::text,''),
       a.credential_generation, a.status,
       (a.capabilities @> '{"runtimeMutationFencingV1":true}'::jsonb),
       COALESCE(p.id::text,''), COALESCE(p.server_id::text,''),
       COALESCE(p.agent_credential_generation,0), COALESCE(p.phase,''),
       (SELECT count(*) FROM vpn_runtime_recovery_enrollment_preparation_events e
         WHERE e.preparation_id=p.id AND e.action='prepared_unattested'),
       (SELECT count(*) FROM vpn_runtime_pre_dispatch_checkpoints c
         WHERE c.server_id=a.server_id),
       COALESCE((SELECT c.agent_credential_generation
         FROM vpn_runtime_pre_dispatch_checkpoints c
         WHERE c.server_id=a.server_id LIMIT 1),0),
       COALESCE((SELECT c.dispatch_epoch
         FROM vpn_runtime_pre_dispatch_checkpoints c
         WHERE c.server_id=a.server_id LIMIT 1),0),
       COALESCE((SELECT c.expected_marker_sha256
         FROM vpn_runtime_pre_dispatch_checkpoints c
         WHERE c.server_id=a.server_id LIMIT 1),''),
       (SELECT count(*) FROM vpn_runtime_recovery_reservations r
         WHERE r.server_id=a.server_id),
       (SELECT count(*) FROM config_apply_jobs j
         WHERE j.server_id=a.server_id AND j.status IN ('pending','in_progress')),
       (SELECT count(*) FROM agent_operation_jobs j
         WHERE j.server_id=a.server_id AND j.status IN ('pending','in_progress')),
       (SELECT count(*) FROM agent_platform_update_jobs j
         WHERE j.server_id=a.server_id AND j.status IN
           ('pending','in_progress','mutation_dispatched','outcome_unknown'))
FROM agents a
LEFT JOIN vpn_runtime_recovery_enrollment_preparations p ON p.agent_id=a.id
WHERE a.id=$1::uuid
`, agentID).Scan(
		&snap.ServerID, &snap.ManagerCredentialGeneration, &status, &fencing,
		&snap.PreparationID, &preparedServerID,
		&snap.PreparedCredentialGeneration, &snap.PreparationPhase,
		&snap.AuditEvents, &snap.CheckpointCount,
		&snap.CheckpointCredentialGeneration, &snap.CheckpointEpoch,
		&snap.CheckpointMarkerSHA256, &snap.RecoveryHolds,
		&activeConfigJobs, &activeOperationJobs, &activePlatformJobs,
	)
	if err != nil || ctx.Err() != nil {
		return deny()
	}
	if err := tx.Commit(ctx); err != nil {
		return deny()
	}

	snap.AgentID = agentID
	snap.Assessment = recoveryEnrollmentManualReviewUnattestedV1
	snap.Unattested = true
	snap.OutstandingManagerJobs = activeConfigJobs + activeOperationJobs + activePlatformJobs
	// Keep blockers deterministic so tests can detect accidental trust
	// elevation and administrative read-only tooling can explain a refusal.
	add := func(condition bool, reason string) {
		if condition {
			snap.Reasons = append(snap.Reasons, reason)
		}
	}
	add(snap.ServerID == "" || snap.ManagerCredentialGeneration <= 0 ||
		status == "disabled" || !fencing, "manager_agent_not_eligible")
	add(snap.PreparationID == "", "preparation_absent")
	add(snap.PreparationID != "" &&
		(snap.PreparationPhase != "prepared_unattested" ||
			preparedServerID != snap.ServerID || snap.AuditEvents != 1),
		"preparation_audit_or_identity_mismatch")
	add(snap.PreparationID != "" &&
		snap.PreparedCredentialGeneration != snap.ManagerCredentialGeneration,
		"preparation_generation_stale")
	add(snap.CheckpointCount == 0, "checkpoint_absent")
	add(snap.CheckpointCount > 1 ||
		(snap.CheckpointCount == 1 &&
			(snap.CheckpointEpoch <= 0 ||
				len(snap.CheckpointMarkerSHA256) != 64)),
		"checkpoint_inconsistent")
	add(snap.CheckpointCount == 1 &&
		snap.CheckpointCredentialGeneration != snap.ManagerCredentialGeneration,
		"checkpoint_generation_stale")
	add(snap.RecoveryHolds > 0, "manager_recovery_hold_present")
	add(snap.OutstandingManagerJobs > 0, "manager_jobs_not_quiet")
	// These two limits are ALWAYS present, even with a consistent DB snapshot.
	// No caller may turn the empty/nonempty blocker list into admission.
	snap.Reasons = append(snap.Reasons,
		"local_journal_unverified",
		"independent_host_provenance_unverified",
		"dispatcher_checkpoint_coupling_not_implemented")
	return snap, nil
}
