package revocations

import (
 "context"
 "time"

 "github.com/ikaevus/routegate/backend/internal/auth"
)

// StagedCredentialRemoval is immutable, secret-free intent metadata.
// No config_versions or config_apply_jobs row exists as a result of staging.
type StagedCredentialRemoval struct {
 OperationID string
 CandidateVersionID string
 ApplyJobID string
 AgentID string
 AgentGeneration int64
 CandidateHash string
 CandidateRuntimeHash string
 StagedAt time.Time
}

// StagePreparedCredentialRemoval atomically binds the original approved
// operator decision to an immutable, NON-EXECUTABLE dispatch manifest.
// The eventual task producer is deliberately absent: neither the generic
// Agent poller nor ordinary config apply may execute this record.
// The locked admission validator must be re-used by any future executor.
func (s *Service) StagePreparedCredentialRemoval(ctx context.Context, operationID, approvalNonce string, confirmSharedProcessRestart bool) (StagedCredentialRemoval, error) {
 var zero StagedCredentialRemoval
 u, ok := auth.UserFromContext(ctx)
 actorID, actorErr := authenticatedRevocationActor(ctx)
 if !ok || actorErr != nil || actorID != u.ID || !confirmSharedProcessRestart ||
  !evidenceID.MatchString(operationID) || !validRemovalHash(approvalNonce) {
  return zero, ErrPreflight
 }
 pinned, err := s.InspectPreparedRemovalIntegrity(ctx, operationID)
 if err != nil { return zero, ErrPreflight }
 tx, err := s.pool.Begin(ctx)
 if err != nil { return zero, err }
 defer tx.Rollback(ctx)

 gate, err := validatePreparedRemovalDispatchLocked(ctx, tx, pinned, u, approvalNonce)
 if err != nil { return zero, ErrPreflight }
 // SQL independently checks every pinned identity, fresh Agent state and
 // live human approval. First writer wins; an idempotent retry cannot rebind.
 if _,err=tx.Exec(ctx,`INSERT INTO vpn_credential_removal_dispatch_stages
 (operation_id,candidate_version_id,apply_job_id,agent_id,
  agent_credential_generation,candidate_hash,candidate_runtime_hash,
  actor_user_id,actor_session_id)
 VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid,$5,$6,$7,$8::uuid,$9::uuid)
 ON CONFLICT(operation_id) DO NOTHING`,
 operationID,gate.VersionID,gate.JobID,gate.AgentID,gate.Generation,
 gate.CandidateHash,gate.RuntimeHash,u.ID,u.SessionID); err!=nil {
  return zero, ErrPreflight
 }
 var stage StagedCredentialRemoval
 var actor,session string
 if err=tx.QueryRow(ctx,`SELECT operation_id::text,candidate_version_id::text,
 apply_job_id::text,agent_id::text,agent_credential_generation,
 candidate_hash,candidate_runtime_hash,staged_at,
 actor_user_id::text,actor_session_id::text
 FROM vpn_credential_removal_dispatch_stages WHERE operation_id=$1::uuid`,
 operationID).Scan(&stage.OperationID,&stage.CandidateVersionID,
 &stage.ApplyJobID,&stage.AgentID,&stage.AgentGeneration,
 &stage.CandidateHash,&stage.CandidateRuntimeHash,&stage.StagedAt,
 &actor,&session);err!=nil{
  return zero, ErrPreflight
 }
 if stage.OperationID!=gate.OperationID ||
  stage.CandidateVersionID!=gate.VersionID ||
  stage.ApplyJobID!=gate.JobID ||
  stage.AgentID!=gate.AgentID ||
  stage.AgentGeneration!=gate.Generation ||
  stage.CandidateHash!=gate.CandidateHash ||
  stage.CandidateRuntimeHash!=gate.RuntimeHash ||
  actor!=u.ID || session!=u.SessionID{
  return zero, ErrPreflight
 }
 if err=tx.Commit(ctx);err!=nil{return zero,err}
 stage.StagedAt=stage.StagedAt.UTC()
 return stage,nil
}
