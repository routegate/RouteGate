package revocations

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"

	"github.com/ikaevus/routegate/backend/internal/auth"
	"github.com/jackc/pgx/v5"
)

// PreparedRemovalDispatchApproval binds one explicit human decision to the
// ORIGINAL immutable candidate and proposed IDs, not to a live Agent task.
// Only the future, separately protected coordinator can consume this record.
type PreparedRemovalDispatchApproval struct {
	OperationID          string
	CandidateVersionID    string
	ApplyJobID            string
	AgentGeneration       int64
	CandidateHash         string
	CandidateRuntimeHash  string
	Nonce                 string
	IssuedAt              time.Time
	ExpiresAt             time.Time
}

type PreparedRemovalDispatchApprovalInput struct {
	OperationID string
	ConfirmSharedProcessRestart bool
}

// IssuePreparedRemovalDispatchApproval writes at most one historical human
// authorization. It cannot allocate a version/job, dispatch a task, transition
// the reservation, or claim the target VLESS credential is no longer usable.
func (s *Service) IssuePreparedRemovalDispatchApproval(ctx context.Context, in PreparedRemovalDispatchApprovalInput) (PreparedRemovalDispatchApproval, error) {
	var zero PreparedRemovalDispatchApproval
	u, ok := auth.UserFromContext(ctx)
	actorID, actorErr := authenticatedRevocationActor(ctx)
	if !ok || actorErr != nil || actorID != u.ID ||
		!in.ConfirmSharedProcessRestart || !evidenceID.MatchString(in.OperationID) {
		return zero, ErrPreflight
	}
	inspected, err := s.InspectPreparedRemovalIntegrity(ctx, in.OperationID)
	if err != nil {
		return zero, ErrPreflight
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback(ctx)
	var node string
	if err = tx.QueryRow(ctx, `SELECT server_id::text FROM vpn_credential_revocations WHERE id=$1::uuid`, in.OperationID).Scan(&node); err != nil || node != inspected.ServerID {
		return zero, ErrPreflight
	}
	if _, err = tx.Exec(ctx, `SELECT id FROM servers WHERE id=$1::uuid FOR UPDATE`, node); err != nil {
		return zero, err
	}
	var state, agent string
	var generation int64
	var candidateHash, runtimeHash string
	if err = tx.QueryRow(ctx, `SELECT state,agent_id::text,agent_credential_generation,candidate_hash,candidate_runtime_hash
	FROM vpn_credential_revocations WHERE id=$1::uuid FOR UPDATE`, in.OperationID).Scan(
		&state,&agent,&generation,&candidateHash,&runtimeHash); err != nil ||
		state!="prepared" || agent!=inspected.AgentID ||
		generation!=inspected.AgentGeneration || candidateHash!=inspected.CandidateHash ||
		runtimeHash!=inspected.CandidateRuntimeHash {
		return zero, ErrPreflight
	}
	if _, err = tx.Exec(ctx, `SELECT id FROM agents WHERE id=$1::uuid FOR UPDATE`, agent); err != nil {
		return zero, ErrPreflight
	}
	if err = lockRemovalDispatchActor(ctx, tx, u); err != nil {
		return zero, ErrPreflight
	}
	var ids PreparedRemovalJobIDs
	if err = tx.QueryRow(ctx, `SELECT operation_id::text,candidate_version_id::text,apply_job_id::text,
	 agent_credential_generation,baseline_version_id::text,candidate_hash,candidate_runtime_hash
	 FROM vpn_credential_revocation_dispatch_bindings WHERE operation_id=$1::uuid`, in.OperationID).Scan(
		&ids.OperationID,&ids.CandidateVersionID,&ids.ApplyJobID,&ids.AgentGeneration,
		&ids.BaselineVersionID,&ids.CandidateHash,&ids.CandidateRuntimeHash); err != nil ||
		ids.AgentGeneration!=inspected.AgentGeneration || ids.BaselineVersionID!=inspected.BaselineVersionID ||
		ids.CandidateHash!=inspected.CandidateHash || ids.CandidateRuntimeHash!=inspected.CandidateRuntimeHash {
		return zero, ErrPreflight
	}
	var now, sessionExpiry time.Time
	if err = tx.QueryRow(ctx, `SELECT clock_timestamp(),expires_at FROM auth_sessions WHERE id=$1::uuid AND user_id=$2::uuid`, u.SessionID,u.ID).Scan(&now,&sessionExpiry); err != nil {
		return zero, ErrPreflight
	}
	var actor, session string
	var result PreparedRemovalDispatchApproval
	err = tx.QueryRow(ctx, `SELECT candidate_version_id::text,apply_job_id::text,agent_credential_generation,
	 candidate_hash,candidate_runtime_hash,nonce,issued_at,expires_at,
	 actor_user_id::text,actor_session_id::text
	 FROM vpn_credential_removal_dispatch_authorizations WHERE operation_id=$1::uuid`, in.OperationID).Scan(
		&result.CandidateVersionID,&result.ApplyJobID,&result.AgentGeneration,
		&result.CandidateHash,&result.CandidateRuntimeHash,&result.Nonce,&result.IssuedAt,&result.ExpiresAt,
		&actor,&session)
	if err == nil {
		// PostgreSQL timestamptz may decode with a location pointer different
		// from time.UTC; keep idempotent typed receipts representation-stable.
		result.IssuedAt=result.IssuedAt.UTC()
		result.ExpiresAt=result.ExpiresAt.UTC()
		if actor!=u.ID || session!=u.SessionID || !now.Before(result.ExpiresAt) ||
			result.ExpiresAt.After(sessionExpiry) ||
			result.CandidateVersionID!=ids.CandidateVersionID || result.ApplyJobID!=ids.ApplyJobID ||
			result.AgentGeneration!=ids.AgentGeneration || result.CandidateHash!=ids.CandidateHash ||
			result.CandidateRuntimeHash!=ids.CandidateRuntimeHash {
			return zero, ErrPreflight
		}
		result.OperationID=in.OperationID
		if err = tx.Commit(ctx); err != nil { return zero, err }
		return result,nil
	}
	if err != pgx.ErrNoRows { return zero, ErrPreflight }
	expiry := now.Add(2*time.Minute)
	if sessionExpiry.Before(expiry) { expiry=sessionExpiry }
	if !expiry.After(now) { return zero, ErrPreflight }
	nonce := make([]byte,32)
	if _, err=rand.Read(nonce); err!=nil { return zero,err }
	result = PreparedRemovalDispatchApproval{
		OperationID:in.OperationID,CandidateVersionID:ids.CandidateVersionID,ApplyJobID:ids.ApplyJobID,
		AgentGeneration:ids.AgentGeneration,CandidateHash:ids.CandidateHash,
		CandidateRuntimeHash:ids.CandidateRuntimeHash,Nonce:hex.EncodeToString(nonce),
		IssuedAt:now.UTC(),ExpiresAt:expiry.UTC(),
	}
	_,err=tx.Exec(ctx, `INSERT INTO vpn_credential_removal_dispatch_authorizations
	 (operation_id,actor_user_id,actor_session_id,nonce,candidate_version_id,apply_job_id,
	  agent_credential_generation,candidate_hash,candidate_runtime_hash,issued_at,expires_at)
	 VALUES ($1::uuid,$2::uuid,$3::uuid,$4,$5::uuid,$6::uuid,$7,$8,$9,$10,$11)`,
		in.OperationID,u.ID,u.SessionID,result.Nonce,result.CandidateVersionID,result.ApplyJobID,
		result.AgentGeneration,result.CandidateHash,result.CandidateRuntimeHash,result.IssuedAt,result.ExpiresAt)
	if err!=nil { return zero, ErrPreflight }
	if err=tx.Commit(ctx); err!=nil { return zero,err }
	return result,nil
}
