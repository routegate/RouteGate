package revocations

import (
 "context"
 "crypto/subtle"
 "time"

 "github.com/ikaevus/routegate/backend/internal/auth"
 "github.com/jackc/pgx/v5"
)

// This is a private transaction-bound dispatch candidate, not an execution
// authorization, runtime receipt, or a claim of revoked VPN access.
type preparedRemovalDispatchGate struct {
 OperationID, ServerID, AgentID, VersionID, JobID, CandidateHash, RuntimeHash string
 Generation int64
 IssuedAt, ExpiresAt time.Time
}

// CheckPreparedRemovalDispatchAdmission checks freshness but grants NO lease.
// A future writer must call validatePreparedRemovalDispatchLocked in the SAME
// transaction that creates its narrowly-scoped version and job. A positive
// result from this wrapper must never authorize a later write.
func (s *Service) CheckPreparedRemovalDispatchAdmission(ctx context.Context, operationID, nonce string, confirmRestart bool) error {
 u, ok := auth.UserFromContext(ctx)
 if !ok || !validRecoveryActor(u) || !confirmRestart || !evidenceID.MatchString(operationID) || !validRemovalHash(nonce) {
  return ErrPreflight
 }
 pinned, err := s.InspectPreparedRemovalIntegrity(ctx, operationID)
 if err != nil { return ErrPreflight }
 tx, err := s.pool.Begin(ctx)
 if err != nil { return err }
 defer tx.Rollback(ctx)
 if _, err = validatePreparedRemovalDispatchLocked(ctx, tx, pinned, u, nonce); err != nil { return ErrPreflight }
 return tx.Commit(ctx)
}

// The mandatory original order is server -> preparation -> Agent -> human
// session and RBAC. Every mutable fact is re-read AFTER its lock wait.
func validatePreparedRemovalDispatchLocked(ctx context.Context, tx pgx.Tx, pinned PreparedRemovalIntegrity, u auth.AuthenticatedUser, nonce string) (preparedRemovalDispatchGate, error) {
 var zero preparedRemovalDispatchGate
 if !validRecoveryActor(u) || !validRemovalHash(nonce) || !evidenceID.MatchString(pinned.OperationID) { return zero, ErrPreflight }
 var node string
 if err := tx.QueryRow(ctx, `SELECT server_id::text FROM vpn_credential_revocations WHERE id=$1::uuid`, pinned.OperationID).Scan(&node); err != nil || node != pinned.ServerID { return zero, ErrPreflight }
 if _, err := tx.Exec(ctx, `SELECT id FROM servers WHERE id=$1::uuid FOR UPDATE`, node); err != nil { return zero, err }
 var state,agent,baselineID,baselineHash,candidateHash,runtimeHash,accountStatus string
 var generation int64
 if err := tx.QueryRow(ctx, `SELECT state,agent_id::text,agent_credential_generation,
 baseline_version_id::text,baseline_hash,candidate_hash,candidate_runtime_hash,account_status
 FROM vpn_credential_revocations WHERE id=$1::uuid FOR UPDATE`,pinned.OperationID).Scan(
 &state,&agent,&generation,&baselineID,&baselineHash,&candidateHash,&runtimeHash,&accountStatus); err != nil ||
 state!="prepared" || agent!=pinned.AgentID || generation!=pinned.AgentGeneration ||
 baselineID!=pinned.BaselineVersionID || baselineHash!=pinned.BaselineHash ||
 candidateHash!=pinned.CandidateHash || runtimeHash!=pinned.CandidateRuntimeHash { return zero, ErrPreflight }
 if _, err := tx.Exec(ctx, `SELECT id FROM agents WHERE id=$1::uuid FOR UPDATE`, agent); err != nil { return zero, err }
 if err := lockRecoveryAuthority(ctx, tx, u); err != nil { return zero, ErrPreflight }
 var fresh bool
 if err := tx.QueryRow(ctx, `SELECT EXISTS(
 SELECT 1 FROM servers s
 JOIN agents a ON a.id=$2::uuid AND a.server_id=s.id
 JOIN config_versions v ON v.id=$3::uuid AND v.server_id=s.id
 JOIN vpn_accounts acc ON acc.id=$5::uuid AND acc.server_id=s.id
 WHERE s.id=$1::uuid AND s.status='active'
 AND s.active_config_version_id=v.id AND v.config_hash=$4
 AND v.status='applied' AND v.applied_at IS NOT NULL
 AND a.status='online' AND a.credential_generation=$6
 AND a.last_authenticated_heartbeat_generation=a.credential_generation
 AND a.last_authenticated_heartbeat_at>clock_timestamp()-interval '90 seconds'
 AND a.capabilities @> '{"sing-box":true,"systemctl":true,"ss":true}'::jsonb
 AND acc.status=$7
 )`,node,agent,baselineID,baselineHash,pinned.AccountID,generation,accountStatus).Scan(&fresh); err != nil || !fresh { return zero, ErrPreflight }
 var gate preparedRemovalDispatchGate
 var boundBaseline string
 if err := tx.QueryRow(ctx, `SELECT operation_id::text,candidate_version_id::text,apply_job_id::text,
 agent_credential_generation,baseline_version_id::text,candidate_hash,candidate_runtime_hash
 FROM vpn_credential_revocation_dispatch_bindings WHERE operation_id=$1::uuid`,pinned.OperationID).Scan(
 &gate.OperationID,&gate.VersionID,&gate.JobID,&gate.Generation,&boundBaseline,&gate.CandidateHash,&gate.RuntimeHash); err != nil ||
 gate.OperationID!=pinned.OperationID || boundBaseline!=baselineID || gate.Generation!=generation ||
 gate.CandidateHash!=candidateHash || gate.RuntimeHash!=runtimeHash ||
 !evidenceID.MatchString(gate.VersionID) || !evidenceID.MatchString(gate.JobID) { return zero, ErrPreflight }
 var actor,session,storedNonce,version,job,storedHash,storedRuntime string
 var authorizedGeneration int64
 var issued,expires,now,sessionExpiry time.Time
 if err := tx.QueryRow(ctx, `SELECT a.actor_user_id::text,a.actor_session_id::text,a.nonce,
 a.candidate_version_id::text,a.apply_job_id::text,a.agent_credential_generation,
 a.candidate_hash,a.candidate_runtime_hash,a.issued_at,a.expires_at,
 clock_timestamp(),s.expires_at
 FROM vpn_credential_removal_dispatch_authorizations a
 JOIN auth_sessions s ON s.id=a.actor_session_id
 WHERE a.operation_id=$1::uuid`,pinned.OperationID).Scan(&actor,&session,
 &storedNonce,&version,&job,&authorizedGeneration,&storedHash,&storedRuntime,
 &issued,&expires,&now,&sessionExpiry); err != nil { return zero, ErrPreflight }
 if actor!=u.ID || session!=u.SessionID ||
 subtle.ConstantTimeCompare([]byte(nonce),[]byte(storedNonce))!=1 ||
 version!=gate.VersionID || job!=gate.JobID || authorizedGeneration!=generation ||
 storedHash!=candidateHash || storedRuntime!=runtimeHash ||
 now.Before(issued) || !now.Before(expires) || expires.After(sessionExpiry) ||
 expires.Sub(issued)>2*time.Minute { return zero, ErrPreflight }
 // Planned IDs must still be unallocated. This is NOT a future-use guarantee.
 var vacant bool
 if err := tx.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM config_versions WHERE id=$1::uuid)
 AND NOT EXISTS(SELECT 1 FROM config_apply_jobs WHERE id=$2::uuid)`,
 gate.VersionID,gate.JobID).Scan(&vacant); err!=nil || !vacant { return zero, ErrPreflight }
 gate.ServerID=node
 gate.AgentID=agent
 gate.IssuedAt=issued
 gate.ExpiresAt=expires
 return gate,nil
}
