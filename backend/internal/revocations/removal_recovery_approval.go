package revocations

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"time"

	"github.com/ikaevus/routegate/backend/internal/auth"
	"github.com/jackc/pgx/v5"
)

// RemovalRecoveryDecision is a new human decision about the original held
// operation. It is signed by the independently provisioned approval authority.
// An approval must later be consumed with a fresh Agent quiescence observation.
type RemovalRecoveryDecision struct {
	Audience             string `json:"audience"`
	AttemptID            string `json:"attemptId"`
	PreviousAttemptID    string `json:"previousAttemptId,omitempty"`
	OperationID          string `json:"operationId"`
	JobID                string `json:"jobId"`
	AccountID            string `json:"accountId"`
	ServerID             string `json:"serverId"`
	AgentID              string `json:"agentId"`
	AgentGeneration      int64  `json:"agentGeneration"`
	CandidateVersionID   string `json:"candidateVersionId"`
	CandidateRuntimeHash string `json:"candidateRuntimeHash"`
	FailureReportDigest  string `json:"failureReportDigest"`
	ActorID              string `json:"actorId"`
	SessionID            string `json:"sessionId"`
	Nonce                string `json:"nonce"`
	SharedRestartConsent bool   `json:"sharedRestartConsent"`
	IssuedAtUnixMicros   int64  `json:"issuedAtUnixMicros"`
	ExpiresAtUnixMicros  int64  `json:"expiresAtUnixMicros"`
}

func (d RemovalRecoveryDecision) SigningBytes() ([]byte, error) {
	body, err := json.Marshal(d)
	if err != nil {
		return nil, err
	}
	return append([]byte("routegate/v1/approve-held-vless-removal-reconciliation\x00"), body...), nil
}

type RemovalRecoveryApproval struct {
	AttemptID   string
	OperationID string
	RecordedAt  time.Time
	ExpiresAt   time.Time
}

// IssueRemovalRecoveryApproval records fresh consent under the same node
// reservation. It creates no Agent work and does not consume execution
// authority. Exact retries preserve the original expiry, even after expiry.
func (s *Service) IssueRemovalRecoveryApproval(ctx context.Context, d RemovalRecoveryDecision, signature []byte) (RemovalRecoveryApproval, error) {
	var zero RemovalRecoveryApproval
	u, ok := auth.UserFromContext(ctx)
	if !ok || !validRecoveryActor(u) || d.ActorID != u.ID || d.SessionID != u.SessionID ||
		d.AttemptID == d.OperationID || d.AttemptID == d.JobID ||
		!d.SharedRestartConsent || !validRemovalHash(d.Nonce) || !validRemovalHash(d.FailureReportDigest) ||
		len(signature) != ed25519.SignatureSize ||
		(d.PreviousAttemptID != "" && !evidenceID.MatchString(d.PreviousAttemptID)) {
		return zero, ErrPreflight
	}
	for _, id := range []string{d.Audience, d.AttemptID, d.OperationID, d.JobID, d.AccountID, d.ServerID, d.AgentID, d.CandidateVersionID} {
		if !evidenceID.MatchString(id) {
			return zero, ErrPreflight
		}
	}
	body, err := json.Marshal(d)
	if err != nil {
		return zero, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback(ctx)
	var locked string
	if err := tx.QueryRow(ctx, `SELECT s.id::text FROM servers s
JOIN vpn_credential_revocations r ON r.server_id=s.id
WHERE r.id=$1::uuid AND s.id=$2::uuid FOR UPDATE OF s`, d.OperationID, d.ServerID).Scan(&locked); err != nil {
		return zero, ErrPreflight
	}
	if err := tx.QueryRow(ctx, `SELECT id::text FROM vpn_credential_revocations WHERE id=$1::uuid FOR UPDATE`,
		d.OperationID).Scan(&locked); err != nil {
		return zero, ErrPreflight
	}
	if err := tx.QueryRow(ctx, `SELECT id::text FROM agents WHERE id=$1::uuid AND server_id=$2::uuid FOR UPDATE`,
		d.AgentID, d.ServerID).Scan(&locked); err != nil {
		return zero, ErrPreflight
	}
	if err := lockRecoveryAuthority(ctx, tx, u); err != nil {
		return zero, ErrPreflight
	}
	pinned, err := inspectPreparedRemovalIntegrity(ctx, tx, d.OperationID)
	if err != nil || pinned.AgentGeneration != d.AgentGeneration {
		return zero, ErrPreflight
	}
	held, err := readHeldAgentRemoval(ctx, tx, d.AgentID)
	if err != nil || held.binding.OperationID != d.OperationID || held.binding.JobID != d.JobID ||
		held.binding.AccountID != d.AccountID || held.binding.ConfigVersionID != d.CandidateVersionID ||
		held.binding.CandidateRuntimeHash != d.CandidateRuntimeHash {
		return zero, ErrPreflight
	}
	var evidence, key []byte
	var audience, oldNonce string
	if err := tx.QueryRow(ctx, `SELECT e.evidence,h.verifier_key,h.audience::text,a.nonce
FROM routegate_dispatch.removal_results e JOIN routegate_dispatch.held_removals h USING(operation_id)
JOIN vpn_credential_removal_dispatch_authorizations a USING(operation_id)
WHERE e.operation_id=$1::uuid`, d.OperationID).Scan(&evidence, &key, &audience, &oldNonce); err != nil {
		return zero, ErrPreflight
	}
	message, err := d.SigningBytes()
	if err != nil || audience != d.Audience || oldNonce == d.Nonce || len(key) != ed25519.PublicKeySize ||
		!ed25519.Verify(ed25519.PublicKey(key), message, signature) {
		return zero, ErrPreflight
	}
	if _, err := VerifyRecoveryRequired(evidence, held.binding); err != nil {
		return zero, ErrPreflight
	}
	digest, err := RuntimeDigest(evidence)
	if err != nil || digest != d.FailureReportDigest {
		return zero, ErrPreflight
	}
	var result RemovalRecoveryApproval
	var saved, savedSignature []byte
	err = tx.QueryRow(ctx, `SELECT attempt_id::text,operation_id::text,created_at,expires_at,decision,signature
FROM routegate_dispatch.removal_recovery_approvals WHERE attempt_id=$1::uuid`, d.AttemptID).Scan(
		&result.AttemptID, &result.OperationID, &result.RecordedAt, &result.ExpiresAt, &saved, &savedSignature)
	if err == nil {
		if !samePreparedRemovalJSON(saved, body) || !bytes.Equal(savedSignature, signature) {
			return zero, ErrPreflight
		}
		return result, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return zero, err
	}
	var now time.Time
	var previous string
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp(),COALESCE((
SELECT x.attempt_id::text FROM routegate_dispatch.removal_recovery_approvals x
WHERE x.operation_id=$1::uuid AND NOT EXISTS(
 SELECT 1 FROM routegate_dispatch.removal_recovery_approvals child WHERE child.previous_attempt_id=x.attempt_id)
),'')`, d.OperationID).Scan(&now, &previous); err != nil || previous != d.PreviousAttemptID {
		return zero, ErrPreflight
	}
	issued, expires := time.UnixMicro(d.IssuedAtUnixMicros), time.UnixMicro(d.ExpiresAtUnixMicros)
	if issued.After(now) || !expires.After(now) || !expires.After(issued) || expires.Sub(issued) > 90*time.Second {
		return zero, ErrPreflight
	}
	err = tx.QueryRow(ctx, `INSERT INTO routegate_dispatch.removal_recovery_approvals
(attempt_id,operation_id,previous_attempt_id,nonce,decision,signature,expires_at)
SELECT $1::uuid,$2::uuid,NULLIF($3,'')::uuid,$4,$5::jsonb,$6,$7::timestamptz
FROM agents a JOIN auth_sessions subject ON subject.id=$8::uuid
WHERE a.id=$9::uuid AND a.credential_generation=$10 AND a.status='online'
AND a.last_authenticated_heartbeat_generation=a.credential_generation
AND a.last_authenticated_heartbeat_at>clock_timestamp()-interval '90 seconds'
AND a.capabilities @> '{"credentialRemovalV1":true,"runtimeMutationFencingV1":true,"sing-box":true,"systemctl":true,"ss":true}'::jsonb
AND subject.revoked_at IS NULL AND subject.expires_at>clock_timestamp() AND subject.expires_at>=$7::timestamptz
AND clock_timestamp()<$7::timestamptz
AND ($3='' OR EXISTS(SELECT 1 FROM routegate_dispatch.removal_recovery_approvals p
 WHERE p.attempt_id=NULLIF($3,'')::uuid AND p.operation_id=$2::uuid AND p.expires_at<=clock_timestamp()))
RETURNING attempt_id::text,operation_id::text,created_at,expires_at`,
		d.AttemptID, d.OperationID, d.PreviousAttemptID, d.Nonce, body, signature, expires,
		u.SessionID, d.AgentID, d.AgentGeneration).Scan(&result.AttemptID, &result.OperationID, &result.RecordedAt, &result.ExpiresAt)
	if err != nil {
		return zero, ErrPreflight
	}
	return result, tx.Commit(ctx)
}
