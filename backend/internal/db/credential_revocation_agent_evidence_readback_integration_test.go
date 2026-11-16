package db

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ikaevus/routegate/backend/internal/agents"
	"github.com/ikaevus/routegate/backend/internal/auth"
	"github.com/ikaevus/routegate/backend/internal/revocations"
)

func TestRevocationAgentEvidenceReadbackIsAuthenticatedAndReadOnly(t *testing.T) {
	ctx,pool,in,_,done:=removalPreparationFixture(t)
	defer done()
	const agentToken="rg_agent_readback-isolated-review-token-not-for-production"
	// The token must exist before preparation: SQL 161 correctly fences any
	// Agent token replacement while an operation is reserved.
	if _,err:=pool.Exec(ctx,`UPDATE agents SET token_hash=$2 WHERE server_id=$1::uuid`,
		in.ServerID,agents.HashToken(agentToken));err!=nil{t.Fatal(err)}

	super:=dispatchReviewUser()
	adminCtx:=auth.ContextWithUser(ctx,super)
	in.Actor=""
	prepared,err:=revocations.NewAuthorizedPreparation(revocations.NewService(pool)).Prepare(adminCtx,in,true)
	if err!=nil{t.Fatal(err)}

	var candidateHash,oldRuntimeHash,newRuntimeHash string
	if err=pool.QueryRow(ctx,`
		SELECT candidate_hash,baseline_runtime_hash,candidate_runtime_hash
		FROM vpn_credential_revocations WHERE id=$1::uuid
	`,prepared.ID).Scan(&candidateHash,&oldRuntimeHash,&newRuntimeHash);err!=nil{t.Fatal(err)}
	if _,err=revocations.NewAuthorizedConfirmation(pool).Confirm(adminCtx,revocations.ConfirmInput{
		OperationID:prepared.ID,ExpectedCandidateHash:candidateHash,
		SharedProcessRestartAcknowledged:true,
	});err!=nil{t.Fatal(err)}
	claim,err:=revocations.NewAuthorizedDispatchClaim(pool).Claim(adminCtx,revocations.DispatchClaimInput{
		OperationID:prepared.ID,ExpectedCandidateHash:candidateHash,ExplicitExecutionApproval:true,
	})
	if err!=nil{t.Fatal(err)}

	// A synthetic database candidate version is used solely for testing the
	// readback comparator. This test neither renders nor applies it to Agent.
	var candidateVersion string
	if err=pool.QueryRow(ctx,`
		INSERT INTO config_versions(server_id,version,config_hash,status,rendered_config)
		SELECT $1::uuid,COALESCE(MAX(version),0)+1,$2,'applied','{}'::jsonb
		FROM config_versions WHERE server_id=$1::uuid
		RETURNING id::text
	`,in.ServerID,candidateHash).Scan(&candidateVersion);err!=nil{
		t.Fatalf("create synthetic readback-only candidate fixture: %v",err)
	}
	var port int
	if err=pool.QueryRow(ctx,"SELECT vless_port FROM servers WHERE id=$1::uuid",in.ServerID).Scan(&port);err!=nil{t.Fatal(err)}
	report:=revocations.CredentialRemovalReport{
		SchemaVersion:1,JobID:claim.ID,OperationID:prepared.ID,
		ServerID:in.ServerID,AgentID:prepared.AgentID,AccountID:in.AccountID,
		BaselineVersionID:in.BaselineVersionID,ConfigVersionID:candidateVersion,
		ConfigHash:candidateHash,BaselineRuntimeHash:oldRuntimeHash,
		ActiveRuntimeHash:newRuntimeHash,ListenerPort:port,
		State:"succeeded",SessionEffect:"shared_process_restart_observed",
		Before:revocations.RuntimeGeneration{
			InvocationID:strings.Repeat("a",32),PID:101,StartedMonotonic:"1000",
		},
		After:revocations.RuntimeGeneration{
			InvocationID:strings.Repeat("b",32),PID:102,StartedMonotonic:"2000",
		},
	}
	raw,err:=json.Marshal(report);if err!=nil{t.Fatal(err)}
	readback:=revocations.NewAgentEvidenceReadback(pool)

	before:=func()string{
		var snapshot string
		if err:=pool.QueryRow(ctx,`
			SELECT jsonb_build_object(
			 'claim',(SELECT to_jsonb(c) FROM vpn_credential_revocation_dispatch_claims c WHERE operation_id=$1::uuid),
			 'prepared',(SELECT to_jsonb(p) FROM vpn_credential_revocations p WHERE id=$1::uuid),
			 'confirmation',(SELECT to_jsonb(c) FROM vpn_credential_revocation_confirmations c WHERE operation_id=$1::uuid),
			 'agent_jobs',(SELECT count(*) FROM agent_operation_jobs WHERE server_id=$2::uuid)
			)::text
		`,prepared.ID,in.ServerID).Scan(&snapshot);err!=nil{t.Fatal(err)}
		return snapshot
	}
	original:=before()
	good,err:=readback.Check(ctx,agentToken,prepared.ID,raw)
	if err!=nil||!good.Matched||good.ClaimID!=claim.ID||good.OperationID!=prepared.ID||len(good.ReportSHA256)!=64{
		t.Fatalf("valid synthetic bound readback rejected: %+v %v",good,err)
	}
	if again,err:=readback.Check(ctx,agentToken,prepared.ID,raw);err!=nil||again!=good{
		t.Fatalf("identical readback was not idempotent: %+v %v",again,err)
	}

	cases:=map[string]func(*revocations.CredentialRemovalReport){
		"foreign Agent":func(r *revocations.CredentialRemovalReport){r.AgentID="33333333-3333-4333-8333-333333333333"},
		"foreign job":func(r *revocations.CredentialRemovalReport){r.JobID="44444444-4444-4444-8444-444444444444"},
		"stale candidate hash":func(r *revocations.CredentialRemovalReport){r.ConfigHash=strings.Repeat("f",64)},
		"stale runtime hash":func(r *revocations.CredentialRemovalReport){r.ActiveRuntimeHash=oldRuntimeHash},
		"unverified candidate version":func(r *revocations.CredentialRemovalReport){r.ConfigVersionID=in.BaselineVersionID},
		"missing restart":func(r *revocations.CredentialRemovalReport){r.After=r.Before},
		"old monotonic generation":func(r *revocations.CredentialRemovalReport){r.After.StartedMonotonic="1"},
		"unverified session effect":func(r *revocations.CredentialRemovalReport){r.SessionEffect=""},
		"failed operation":func(r *revocations.CredentialRemovalReport){r.State="failed"},
	}
	for name,change:=range cases{
		t.Run(name,func(t *testing.T){
			bad:=report;change(&bad);payload,_:=json.Marshal(bad)
			if result,err:=readback.Check(ctx,agentToken,prepared.ID,payload);err==nil||result.Matched{
				t.Fatalf("tampered Agent evidence accepted: %+v",result)
			}
		})
	}
	for name,token:=range map[string]string{
		"foreign Agent bearer":"rg_agent_unrelated-token",
		"missing Agent bearer":"",
		"operator session instead of Agent bearer":"a-user-session-token",
	}{
		t.Run(name,func(t *testing.T){
			if result,err:=readback.Check(ctx,token,prepared.ID,raw);err==nil||result.Matched{
				t.Fatalf("nonmatching Agent credential accepted: %+v",result)
			}
		})
	}
	for _,malformed:=range [][]byte{
		[]byte(strings.Replace(string(raw),`"state":`,`"state":"failed","state":`,1)),
		append(append([]byte(nil),raw...),[]byte(" {}")...),
		[]byte(strings.Repeat(" ",16385)),
	}{
		if result,err:=readback.Check(ctx,agentToken,prepared.ID,malformed);err==nil||result.Matched{
			t.Fatal("invalid report accepted")
		}
	}
	if after:=before();after!=original{
		t.Fatal("readback modified claim, prepared operation, confirmation or Agent job")
	}
}
