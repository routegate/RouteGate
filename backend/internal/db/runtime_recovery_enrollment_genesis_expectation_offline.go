package db

import (
	"context"
	"errors"

	"github.com/ikaevus/routegate/backend/internal/recoveryenroll"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Manager-only, read-only, OFFLINE expectation. A committed preparation and
// its DB audit are independent of the Agent's two local files, but a hash
// derived here does NOT authenticate a remote Agent, prove its host identity,
// anchor a live challenge, or permit an Agent task/recovery/activation.
var errRecoveryGenesisManagerExpectationRefusedV1 = errors.New("offline Manager genesis expectation refused")

type recoveryGenesisManagerExpectationV1 struct {
	PreparationID             string
	AgentID                   string
	ServerID                  string
	AgentCredentialGeneration int64
	GenesisSHA256             string
	Unattested                bool
}

// This observation is historical the moment its REPEATABLE READ READ ONLY
// snapshot completes. In particular no lock, token grant, dispatch right or
// trust assertion survives the return. Never use this as an admission gate.
func inspectRecoveryGenesisManagerExpectationOfflineV1(
	ctx context.Context, pool *pgxpool.Pool, agentID string,
) (recoveryGenesisManagerExpectationV1, error) {
	deny := func() (recoveryGenesisManagerExpectationV1, error) {
		return recoveryGenesisManagerExpectationV1{}, errRecoveryGenesisManagerExpectationRefusedV1
	}
	if ctx == nil || ctx.Err() != nil || pool == nil || agentID == "" {
		return deny()
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{
		IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly,
	})
	if err != nil {
		return deny()
	}
	defer tx.Rollback(ctx)

	var p recoveryenroll.Preparation
	var actualGeneration int64
	var status string
	var fencing bool
	var eventCount, holds, checkpoints int64
	var configJobs, operationJobs, platformJobs int64
	err = tx.QueryRow(ctx, `
SELECT p.id::text,p.agent_id::text,p.server_id::text,
       p.agent_credential_generation,p.phase,
       a.credential_generation,a.status,
       (a.capabilities @> '{"runtimeMutationFencingV1":true}'::jsonb),
       (SELECT count(*) FROM vpn_runtime_recovery_enrollment_preparation_events e
         WHERE e.preparation_id=p.id AND e.action='prepared_unattested'),
       (SELECT count(*) FROM vpn_runtime_recovery_reservations r
         WHERE r.server_id=a.server_id),
       (SELECT count(*) FROM vpn_runtime_pre_dispatch_checkpoints c
         WHERE c.server_id=a.server_id),
       (SELECT count(*) FROM config_apply_jobs j
         WHERE j.server_id=a.server_id AND j.status IN ('pending','in_progress')),
       (SELECT count(*) FROM agent_operation_jobs j
         WHERE j.server_id=a.server_id AND j.status IN ('pending','in_progress')),
       (SELECT count(*) FROM agent_platform_update_jobs j
         WHERE j.server_id=a.server_id AND j.status IN
           ('pending','in_progress','mutation_dispatched','outcome_unknown'))
  FROM vpn_runtime_recovery_enrollment_preparations p
  JOIN agents a ON a.id=p.agent_id AND a.server_id=p.server_id
  JOIN servers s ON s.id=p.server_id
 WHERE p.agent_id=$1::uuid
`, agentID).Scan(
		&p.ID, &p.AgentID, &p.ServerID,
		&p.AgentCredentialGeneration, &p.Phase,
		&actualGeneration, &status, &fencing,
		&eventCount, &holds, &checkpoints,
		&configJobs, &operationJobs, &platformJobs,
	)
	if err != nil || ctx.Err() != nil ||
		p.AgentID != agentID || p.AgentCredentialGeneration != actualGeneration ||
		status == "disabled" || !fencing || eventCount != 1 ||
		holds != 0 || checkpoints != 0 || configJobs != 0 ||
		operationJobs != 0 || platformJobs != 0 {
		return deny()
	}
	digest, err := recoveryenroll.OfflineGenesisDigestUnattestedV1(p)
	if err != nil {
		return deny()
	}
	if err := tx.Commit(ctx); err != nil {
		return deny()
	}
	return recoveryGenesisManagerExpectationV1{
		PreparationID: p.ID,
		AgentID: p.AgentID,
		ServerID: p.ServerID,
		AgentCredentialGeneration: p.AgentCredentialGeneration,
		GenesisSHA256: digest,
		Unattested: true,
	}, nil
}
