package revocations

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/ikaevus/routegate/backend/internal/agents"
	"github.com/jackc/pgx/v5"
)

type heldAgentRemoval struct {
	binding     EvidenceBinding
	config      json.RawMessage
	vlessUUID   string
	actorID     string
	sessionID   string
	deadline    time.Time
	deliverable bool
}

// lockHeldRemovalAgent serializes against issuance, token rotation and result
// submission using the same server-first order. Recheck the bearer after wait.
func lockHeldRemovalAgent(ctx context.Context, tx pgx.Tx, tokenHash string) (agentID string, err error) {
	var node string
	if err := tx.QueryRow(ctx, `SELECT server_id::text FROM agents WHERE token_hash=$1`, tokenHash).Scan(&node); err != nil {
		return "", err
	}
	if err := tx.QueryRow(ctx, `SELECT id::text FROM servers WHERE id=$1::uuid FOR UPDATE`, node).Scan(&node); err != nil {
		return "", err
	}
	err = tx.QueryRow(ctx, `SELECT id::text FROM agents WHERE token_hash=$1 AND server_id=$2::uuid FOR UPDATE`,
		tokenHash, node).Scan(&agentID)
	return agentID, err
}

func readHeldAgentRemoval(ctx context.Context, tx pgx.Tx, agentID string) (heldAgentRemoval, error) {
	var h heldAgentRemoval
	b := &h.binding
	err := tx.QueryRow(ctx, `SELECT r.id::text,h.job_id::text,r.vpn_account_id::text,r.server_id::text,
 r.agent_id::text,r.baseline_version_id::text,h.version_id::text,r.candidate_hash,
 r.baseline_runtime_hash,r.candidate_runtime_hash,s.vless_port,v.rendered_config,
 acc.vless_uuid::text,a.actor_user_id::text,a.actor_session_id::text,
 LEAST(a.expires_at,ag.last_authenticated_heartbeat_at+interval '90 seconds'),
 ag.status='online' AND s.status='active'
 AND ag.last_authenticated_heartbeat_generation IS NOT DISTINCT FROM ag.credential_generation
 AND ag.last_authenticated_heartbeat_at IS NOT NULL
 AND ag.capabilities @> '{"credentialRemovalV1":true,"runtimeMutationFencingV1":true,"sing-box":true,"systemctl":true,"ss":true}'::jsonb
 FROM routegate_dispatch.held_removals h
 JOIN vpn_credential_revocations r ON r.id=h.operation_id
 JOIN vpn_credential_removal_dispatch_authorizations a ON a.operation_id=r.id
 JOIN servers s ON s.id=r.server_id AND s.active_config_version_id=r.baseline_version_id
 JOIN agents ag ON ag.id=r.agent_id AND ag.server_id=s.id AND ag.credential_generation=r.agent_credential_generation
 JOIN vpn_accounts acc ON acc.id=r.vpn_account_id AND acc.server_id=s.id AND acc.status=r.account_status
 JOIN config_versions v ON v.id=h.version_id AND v.server_id=s.id AND v.status='held_for_revocation'
 JOIN config_apply_jobs j ON j.id=h.job_id AND j.server_id=s.id AND j.agent_id=ag.id
   AND j.config_version_id=v.id AND j.status='held_for_revocation' AND j.action='apply'
 WHERE ag.id=$1::uuid AND r.state='prepared' AND v.config_hash=r.candidate_hash
   AND v.rendered_config=r.candidate_config`, agentID).Scan(
		&b.OperationID, &b.JobID, &b.AccountID, &b.ServerID, &b.AgentID, &b.BaselineVersionID,
		&b.ConfigVersionID, &b.ConfigHash, &b.BaselineRuntimeHash, &b.CandidateRuntimeHash, &b.ListenerPort,
		&h.config, &h.vlessUUID, &h.actorID, &h.sessionID, &h.deadline, &h.deliverable)
	return h, err
}

// ClaimHeldRemoval is a dedicated, isolated Agent exchange. A repeat may
// retrieve the same task while the original authority is live. Once evidence
// exists, or authority expires, the task is withheld for terminal processing.
func (s *Service) ClaimHeldRemoval(ctx context.Context, tokenHash string) (*agents.AgentDeliveryTask, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	agentID, err := lockHeldRemovalAgent(ctx, tx, tokenHash)
	if err != nil {
		return nil, err
	}
	h, err := readHeldAgentRemoval(ctx, tx, agentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !h.deliverable {
		return nil, nil
	}
	if _, ok := normalizedRemovalUUID(h.vlessUUID); !ok {
		return nil, agents.ErrRemovalExchangeRejected
	}
	if err := lockRecoveryAuthoritySubject(ctx, tx, h.actorID, h.sessionID); err != nil {
		return nil, agents.ErrRemovalExchangeRejected
	}
	b := h.binding
	if _, err := tx.Exec(ctx, `INSERT INTO routegate_dispatch.removal_claims(operation_id,delivery_expires_at)
 SELECT $1::uuid,$2::timestamptz WHERE clock_timestamp()<$2
 AND NOT EXISTS (SELECT 1 FROM routegate_dispatch.removal_results WHERE operation_id=$1::uuid)
 ON CONFLICT(operation_id) DO NOTHING`, b.OperationID, h.deadline); err != nil {
		return nil, err
	}
	var claimedAt, expires time.Time
	err = tx.QueryRow(ctx, `SELECT claimed_at,LEAST(delivery_expires_at,$2::timestamptz) FROM routegate_dispatch.removal_claims
 WHERE operation_id=$1::uuid AND clock_timestamp()<LEAST(delivery_expires_at,$2::timestamptz)
 AND NOT EXISTS (SELECT 1 FROM routegate_dispatch.removal_results WHERE operation_id=$1::uuid)`,
		b.OperationID, h.deadline).Scan(&claimedAt, &expires)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	expires = expires.UTC()
	return &agents.AgentDeliveryTask{
		AgentConfigTask: agents.AgentConfigTask{
			ID: b.JobID, Kind: TaskKindCredentialRemoval, ServerID: b.ServerID, AgentID: b.AgentID,
			ConfigVersionID: b.ConfigVersionID, Action: "apply", Status: agents.ConfigApplyJobStatusInProgress,
			RenderedConfig: h.config, ConfigHash: b.ConfigHash, CreatedAt: claimedAt, StartedAt: &claimedAt,
		},
		CredentialRemoval: &agents.AgentCredentialRemovalRequest{
			SchemaVersion: 1, OperationID: b.OperationID, AccountID: b.AccountID,
			BaselineVersionID: b.BaselineVersionID, BaselineRuntimeHash: b.BaselineRuntimeHash,
			CandidateRuntimeHash: b.CandidateRuntimeHash, VLESSUUID: h.vlessUUID,
			SharedProcessRestartAcknowledged: true,
		},
		DeliveryExpiresAt: &expires,
	}, nil
}

// AcceptHeldRemovalResult stores verified, secret-free evidence exactly once.
// A report never activates the config, lifts the reservation, or authorizes a
// retry. Late evidence can be recorded after delivery authority has expired.
func (s *Service) AcceptHeldRemovalResult(ctx context.Context, tokenHash, jobID string, data []byte) error {
	if !evidenceID.MatchString(jobID) || len(data) > 16384 {
		return agents.ErrRemovalExchangeRejected
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	agentID, err := lockHeldRemovalAgent(ctx, tx, tokenHash)
	if err != nil {
		return err
	}
	h, err := readHeldAgentRemoval(ctx, tx, agentID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && h.binding.JobID != jobID) {
		return acceptConfirmedRemovalReplay(ctx, tx, agentID, jobID, data)
	}
	if err != nil {
		return err
	}
	report, err := VerifySuccess(data, h.binding)
	if err != nil {
		return agents.ErrRemovalExchangeRejected
	}
	evidence, err := json.Marshal(report)
	if err != nil || !samePreparedRemovalJSON(data, evidence) {
		return agents.ErrRemovalExchangeRejected
	}
	if _, err := tx.Exec(ctx, `INSERT INTO routegate_dispatch.removal_results(operation_id,evidence)
 SELECT operation_id,$2::jsonb FROM routegate_dispatch.removal_claims WHERE operation_id=$1::uuid
 ON CONFLICT(operation_id) DO NOTHING`, h.binding.OperationID, evidence); err != nil {
		return err
	}
	var exact bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM routegate_dispatch.removal_results
 WHERE operation_id=$1::uuid AND evidence=$2::jsonb)`, h.binding.OperationID, evidence).Scan(&exact); err != nil {
		return err
	}
	if !exact {
		return agents.ErrRemovalExchangeRejected
	}
	return tx.Commit(ctx)
}

// A result sent again after a lost acknowledgement can still read its exact
// receipt, even when the terminal writer has already released the hold.
func acceptConfirmedRemovalReplay(ctx context.Context, tx pgx.Tx, agentID, jobID string, data []byte) error {
	if _, _, _, err := canonicalRemovalJSON(data); err != nil {
		return agents.ErrRemovalExchangeRejected
	}
	var stored []byte
	err := tx.QueryRow(ctx, `SELECT e.evidence FROM routegate_dispatch.confirmed_removals c
JOIN routegate_dispatch.removal_results e USING (operation_id)
JOIN routegate_dispatch.held_removals h USING (operation_id)
JOIN vpn_credential_revocations r ON r.id=c.operation_id AND r.state='confirmed'
JOIN agents a ON a.id=r.agent_id AND a.server_id=r.server_id AND a.credential_generation=r.agent_credential_generation
WHERE h.job_id=$1::uuid AND a.id=$2::uuid`, jobID, agentID).Scan(&stored)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !samePreparedRemovalJSON(data, stored)) {
		return agents.ErrRemovalExchangeRejected
	}
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
