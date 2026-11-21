package db

import (
	"testing"

	"github.com/ikaevus/routegate/backend/internal/agents"
)

func TestRuntimeMutationFencingPreservesUncertainJobs(t *testing.T){
	for _,fenced:=range []bool{false,true}{
		t.Run(map[bool]string{false:"legacy_timeout",true:"sticky_fenced_policy"}[fenced],func(t *testing.T){
			ctx,pool,in,_,done:=removalPreparationFixture(t)
			defer done()
			var agentID,token string
			if err:=pool.QueryRow(ctx,`SELECT id::text,token_hash FROM agents WHERE server_id=$1::uuid`,in.ServerID).Scan(&agentID,&token);err!=nil{t.Fatal(err)}
			var configJob,operationJob string
			if err:=pool.QueryRow(ctx,`INSERT INTO config_apply_jobs(server_id,agent_id,config_version_id,action,status,started_at) VALUES($1::uuid,$2::uuid,$3::uuid,'apply','in_progress',now()-interval '10 minutes') RETURNING id::text`,in.ServerID,agentID,in.BaselineVersionID).Scan(&configJob);err!=nil{t.Fatal(err)}
			if err:=pool.QueryRow(ctx,`INSERT INTO agent_operation_jobs(server_id,agent_id,kind,operation,status,started_at) VALUES($1::uuid,$2::uuid,'vpn_core_service','restart','in_progress',now()-interval '10 minutes') RETURNING id::text`,in.ServerID,agentID).Scan(&operationJob);err!=nil{t.Fatal(err)}
			repo:=agents.NewRepository(pool)
			for _,caps:=range []agents.Capabilities{{"runtimeMutationFencingV1":fenced},nil,{}, {"runtimeMutationFencingV1":false}}{
				a,err:=repo.UpdateAgentHeartbeat(ctx,agents.UpdateAgentHeartbeatInput{TokenHash:token,Capabilities:caps})
				if err!=nil{t.Fatal(err)}
				if a.Capabilities.RuntimeMutationFencingEnabled()!=fenced{t.Fatal("sticky policy forgotten")}
				var cs,os string
				if err:=pool.QueryRow(ctx,`SELECT (SELECT status FROM config_apply_jobs WHERE id=$1::uuid),(SELECT status FROM agent_operation_jobs WHERE id=$2::uuid)`,configJob,operationJob).Scan(&cs,&os);err!=nil{t.Fatal(err)}
				want:="failed";if fenced{want="in_progress"}
				if cs!=want||os!=want{t.Fatalf("config=%s operation=%s want=%s",cs,os,want)}
			}
			// Credential replacement must not silently re-enable timeout cleanup.
			if _,err:=repo.CreateOrReplaceAgentForServer(ctx,agents.CreateOrReplaceAgentInput{ServerID:in.ServerID,AgentVersion:"test",TokenHash:"replacement-fenced-fixture",Capabilities:agents.Capabilities{}});err!=nil{t.Fatal(err)}
			a,err:=repo.UpdateAgentHeartbeat(ctx,agents.UpdateAgentHeartbeatInput{TokenHash:"replacement-fenced-fixture",Capabilities:agents.Capabilities{}})
			if err!=nil||a.Capabilities.RuntimeMutationFencingEnabled()!=fenced{t.Fatal("replacement lost policy",err)}
			var status string
			if err:=pool.QueryRow(ctx,`SELECT status FROM config_apply_jobs WHERE id=$1::uuid`,configJob).Scan(&status);err!=nil{t.Fatal(err)}
			if fenced&&status!="in_progress"{t.Fatal("replacement terminalized uncertain job")}
		})
	}
}
