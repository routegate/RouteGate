package db

import (
 "encoding/json"
 "errors"
 "strings"
 "testing"

 "github.com/ikaevus/routegate/backend/internal/agents"
 "github.com/ikaevus/routegate/backend/internal/auth"
 "github.com/ikaevus/routegate/backend/internal/revocations"
)

// The "authenticated" half is the CURRENT registered Agent bearer hash.
// Payload fields are untrusted and cannot select another reservation/Agent.
// A match is identity-only: no job, action, receipt, or successful result.
func TestCredentialRemovalTaskIdentityCurrentAgentAndDurableReservation(t *testing.T){
 ctx,pool,in,_,done:=removalPreparationFixture(t)
 defer done()
 const rawAgentToken="rg_agent_credential-removal-identity-test"
 tokenHash:=agents.HashToken(rawAgentToken)
 var agentID string
 if err:=pool.QueryRow(ctx,`
  UPDATE agents SET token_hash=$2 WHERE server_id=$1::uuid
  RETURNING id::text
 `,in.ServerID,tokenHash).Scan(&agentID);err!=nil{t.Fatal(err)}
 user:=dispatchReviewUser()
 userCtx:=auth.ContextWithUser(ctx,user)
 in.Actor=""
 prepared,err:=revocations.NewAuthorizedPreparation(revocations.NewService(pool)).Prepare(userCtx,in,true)
 if err!=nil{t.Fatal(err)}
 var candidateHash string
 if err=pool.QueryRow(ctx,`
 SELECT candidate_hash FROM vpn_credential_revocations WHERE id=$1::uuid
 `,prepared.ID).Scan(&candidateHash);err!=nil{t.Fatal(err)}
 _,err=revocations.NewAuthorizedConfirmation(pool).Confirm(userCtx,revocations.ConfirmInput{
  OperationID:prepared.ID,ExpectedCandidateHash:candidateHash,SharedProcessRestartAcknowledged:true,
 })
 if err!=nil{t.Fatal(err)}
 claimed,err:=revocations.NewAuthorizedDispatchClaim(pool).Claim(userCtx,revocations.DispatchClaimInput{
  OperationID:prepared.ID,ExpectedCandidateHash:candidateHash,ExplicitExecutionApproval:true,
 })
 if err!=nil{t.Fatal(err)}
 reserved,err:=revocations.NewAuthorizedTaskIdentityReservation(pool).Reserve(userCtx,revocations.ReserveTaskIdentityInput{
  OperationID:prepared.ID,ClaimID:claimed.ID,ExpectedCandidateHash:candidateHash,
 })
 if err!=nil{t.Fatal(err)}

 var originalAccount,originalClaims string
 var beforeApplyJobs int
 err=pool.QueryRow(ctx,`SELECT
  (SELECT row_to_json(a)::text FROM vpn_accounts a WHERE id=$1::uuid),
  (SELECT jsonb_agg(to_jsonb(c))::text FROM vpn_credential_revocation_dispatch_claims c WHERE operation_id=$2::uuid),
  (SELECT count(*) FROM config_apply_jobs WHERE server_id=$3::uuid)
 `,in.AccountID,prepared.ID,in.ServerID).Scan(&originalAccount,&originalClaims,&beforeApplyJobs)
 if err!=nil{t.Fatal(err)}
 var v revocations.CredentialRemovalTaskIdentityV1
 err=pool.QueryRow(ctx,`
 SELECT 1,task_id::text,claim_id::text,operation_id::text,server_id::text,
  agent_id::text,agent_credential_generation,baseline_version_id::text,
  candidate_hash,candidate_runtime_hash,task_kind
 FROM vpn_credential_revocation_task_reservations WHERE task_id=$1::uuid
 `,reserved.TaskID).Scan(
  &v.SchemaVersion,&v.TaskID,&v.ClaimID,&v.OperationID,&v.ServerID,&v.AgentID,
  &v.AgentCredentialGeneration,&v.BaselineVersionID,&v.CandidateHash,
  &v.CandidateRuntimeHash,&v.TaskKind)
 if err!=nil{t.Fatal(err)}
 if v.AgentID!=agentID || v.ServerID!=in.ServerID || v.ClaimID!=claimed.ID ||
  v.OperationID!=prepared.ID || v.TaskID!=reserved.TaskID || v.CandidateHash!=candidateHash {
  t.Fatal("fixture did not bind immutable task identity to original Manager claim")
 }
 verifier:=revocations.NewAgentTaskIdentityVerifier(pool)
 marshal:=func(v revocations.CredentialRemovalTaskIdentityV1) []byte {
  t.Helper();b,err:=json.Marshal(v);if err!=nil{t.Fatal(err)};return b
 }
 if err=verifier.Match(ctx,tokenHash,marshal(v));err!=nil{
  t.Fatalf("read-only current-Agent identity should match: %v",err)
 }
 if err=verifier.Match(ctx,strings.Repeat("f",64),marshal(v));!errors.Is(err,revocations.ErrPreflight){
  t.Fatalf("foreign token accepted: %v",err)
 }
 if err=verifier.Match(ctx,"unhashed-token",marshal(v));!errors.Is(err,revocations.ErrPreflight){
  t.Fatalf("raw/untrusted token accepted: %v",err)
 }
 badID:="99999999-9999-4999-8999-999999999999"
 for name,mutate:=range map[string]func(*revocations.CredentialRemovalTaskIdentityV1){
  "task":func(p *revocations.CredentialRemovalTaskIdentityV1){p.TaskID=badID},
  "claim":func(p *revocations.CredentialRemovalTaskIdentityV1){p.ClaimID=badID},
  "operation":func(p *revocations.CredentialRemovalTaskIdentityV1){p.OperationID=badID},
  "node":func(p *revocations.CredentialRemovalTaskIdentityV1){p.ServerID=badID},
  "agent":func(p *revocations.CredentialRemovalTaskIdentityV1){p.AgentID=badID},
  "generation":func(p *revocations.CredentialRemovalTaskIdentityV1){p.AgentCredentialGeneration++},
  "baseline":func(p *revocations.CredentialRemovalTaskIdentityV1){p.BaselineVersionID=badID},
  "candidate hash":func(p *revocations.CredentialRemovalTaskIdentityV1){p.CandidateHash=strings.Repeat("c",64)},
  "runtime hash":func(p *revocations.CredentialRemovalTaskIdentityV1){p.CandidateRuntimeHash=strings.Repeat("d",64)},
  "kind":func(p *revocations.CredentialRemovalTaskIdentityV1){p.TaskKind="config_apply"},
  "schema":func(p *revocations.CredentialRemovalTaskIdentityV1){p.SchemaVersion=2},
 }{
  t.Run(name,func(t *testing.T) {
   bad:=v;mutate(&bad)
   if err:=verifier.Match(ctx,tokenHash,marshal(bad));!errors.Is(err,revocations.ErrPreflight){
    t.Fatalf("untrusted identity mismatch accepted: %v",err)
   }
  })
 }
 // Agent heartbeat loss is not success and not a reusable task authority.
 if _,err=pool.Exec(ctx,`UPDATE agents
 SET last_authenticated_heartbeat_at=now()-interval '10 minutes'
 WHERE id=$1::uuid`,agentID);err!=nil{t.Fatal(err)}
 if err=verifier.Match(ctx,tokenHash,marshal(v));!errors.Is(err,revocations.ErrPreflight){
  t.Fatalf("stale Agent accepted: %v",err)
 }
 if _,err=pool.Exec(ctx,`UPDATE agents SET last_authenticated_heartbeat_at=now() WHERE id=$1::uuid`,agentID);err!=nil{t.Fatal(err)}

 var afterAccount,afterClaims string
 var jobs,afterApplyJobs int
 err=pool.QueryRow(ctx,`SELECT
  (SELECT row_to_json(a)::text FROM vpn_accounts a WHERE id=$1::uuid),
  (SELECT jsonb_agg(to_jsonb(c))::text FROM vpn_credential_revocation_dispatch_claims c WHERE operation_id=$2::uuid),
  (SELECT count(*) FROM agent_operation_jobs WHERE server_id=$3::uuid),
  (SELECT count(*) FROM config_apply_jobs WHERE server_id=$3::uuid)
 `,in.AccountID,prepared.ID,in.ServerID).Scan(&afterAccount,&afterClaims,&jobs,&afterApplyJobs)
 if err!=nil{t.Fatal(err)}
 if afterAccount!=originalAccount || afterClaims!=originalClaims || jobs!=0 || afterApplyJobs!=beforeApplyJobs{
  t.Fatalf("identity observation mutated runtime/control: account=%v claim=%v jobs=%d apply_before=%d apply_after=%d",
   afterAccount==originalAccount,afterClaims==originalClaims,jobs,beforeApplyJobs,afterApplyJobs)
 }
}
