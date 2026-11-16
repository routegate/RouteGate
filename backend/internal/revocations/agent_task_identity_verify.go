package revocations

import (
 "context"
 "github.com/jackc/pgx/v5"
 "github.com/jackc/pgx/v5/pgxpool"
)

// AgentTaskIdentityVerifier checks ONLY whether a bounded identity assertion
// matches the current authenticated Agent token hash AND the unmodified
// schema-165 reservation, schema-163 claim and schema-161/162 parent history.
//
// It is intentionally unmounted: a future endpoint MUST authenticate the
// bearer token and pass agents.HashToken(rawBearer), never a client-supplied
// hash from a JSON body. Match does NOT record receipt, prove runtime action,
// return a dispatch token or authorize Agent execution/Manager success.
type AgentTaskIdentityVerifier struct { pool *pgxpool.Pool }

func NewAgentTaskIdentityVerifier(pool *pgxpool.Pool) *AgentTaskIdentityVerifier {
 return &AgentTaskIdentityVerifier{pool:pool}
}

// Match is read-only and fail-closed. An identity match MUST NOT be used to
// mark a VPN account revoked or to release an outcome_unknown operation.
func (s *AgentTaskIdentityVerifier) Match(
 ctx context.Context, trustedAgentTokenHash string, rawIdentity []byte,
) error {
 if s==nil || s.pool==nil || !validRemovalHash(trustedAgentTokenHash){
  return ErrPreflight
 }
 identity,err:=ParseCredentialRemovalTaskIdentityV1(rawIdentity)
 if err!=nil{return ErrPreflight}
 tx,err:=s.pool.BeginTx(ctx,pgx.TxOptions{AccessMode:pgx.ReadOnly})
 if err!=nil{return ErrPreflight}
 defer tx.Rollback(ctx)
 var expected CredentialRemovalTaskIdentityV1
 err=tx.QueryRow(ctx,`
 SELECT 1,r.task_id::text,r.claim_id::text,r.operation_id::text,
        r.server_id::text,r.agent_id::text,r.agent_credential_generation,
        r.baseline_version_id::text,r.candidate_hash,r.candidate_runtime_hash,
        r.task_kind
 FROM vpn_credential_revocation_task_reservations r
 JOIN vpn_credential_revocation_dispatch_claims c
   ON c.claim_id=r.claim_id AND c.operation_id=r.operation_id
 JOIN vpn_credential_revocations p ON p.id=r.operation_id
 JOIN vpn_credential_revocation_confirmations f ON f.operation_id=p.id
 JOIN agents a ON a.id=r.agent_id AND a.server_id=r.server_id
 JOIN servers s ON s.id=r.server_id
 JOIN config_versions v ON v.id=r.baseline_version_id AND v.server_id=r.server_id
 WHERE r.task_id=$1::uuid
   AND a.token_hash=$2
   AND a.status='online'
   AND a.credential_generation=r.agent_credential_generation
   AND a.last_authenticated_heartbeat_generation=a.credential_generation
   AND a.last_authenticated_heartbeat_at>now()-interval '90 seconds'
   AND s.active_config_version_id=r.baseline_version_id
   AND v.status='applied'
   AND p.state='prepared' AND c.state='outcome_unknown'
   AND r.state='identity_reserved'
   AND r.reserved_by=p.requested_by AND c.claimed_by=p.requested_by
   AND f.confirmed_by=p.requested_by AND f.shared_restart_acknowledged
   AND c.server_id=r.server_id AND p.server_id=r.server_id
   AND c.agent_id=r.agent_id AND p.agent_id=r.agent_id AND f.agent_id=r.agent_id
   AND c.agent_credential_generation=r.agent_credential_generation
   AND p.agent_credential_generation=r.agent_credential_generation
   AND f.agent_credential_generation=r.agent_credential_generation
   AND c.baseline_version_id=r.baseline_version_id
   AND p.baseline_version_id=r.baseline_version_id
   AND f.baseline_version_id=r.baseline_version_id
   AND c.candidate_hash=r.candidate_hash AND p.candidate_hash=r.candidate_hash
   AND f.candidate_hash=r.candidate_hash
   AND c.candidate_runtime_hash=r.candidate_runtime_hash
   AND p.candidate_runtime_hash=r.candidate_runtime_hash
   AND f.candidate_runtime_hash=r.candidate_runtime_hash
 `,identity.TaskID,trustedAgentTokenHash).Scan(
  &expected.SchemaVersion,&expected.TaskID,&expected.ClaimID,
  &expected.OperationID,&expected.ServerID,&expected.AgentID,
  &expected.AgentCredentialGeneration,&expected.BaselineVersionID,
  &expected.CandidateHash,&expected.CandidateRuntimeHash,&expected.TaskKind)
 if err!=nil || expected!=identity{return ErrPreflight}
 if tx.Commit(ctx)!=nil{return ErrPreflight}
 return nil
}
