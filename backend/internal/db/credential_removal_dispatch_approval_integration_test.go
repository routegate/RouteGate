package db

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/ikaevus/routegate/backend/internal/auth"
	"github.com/ikaevus/routegate/backend/internal/revocations"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestCredentialRemovalDispatchHumanApproval(t *testing.T) {
	ctx, pool, in, _, done := removalPreparationFixture(t)
	defer done()
	svc := revocations.NewService(pool)
	prepared, err := svc.Prepare(ctx,in)
	if err!=nil { t.Fatal(err) }
	ids, err := svc.ReservePreparedRemovalJobIDs(ctx,prepared.ID)
	if err!=nil { t.Fatal(err) }
	var initialVersions,initialJobs int
	if err=pool.QueryRow(ctx,`SELECT
	 (SELECT count(*) FROM config_versions),
	 (SELECT count(*) FROM config_apply_jobs)`).Scan(&initialVersions,&initialJobs);err!=nil {
		t.Fatal(err)
	}

	actor := auth.AuthenticatedUser{UserProfile:auth.UserProfile{
		UserType:"human", Status:"active", Roles:[]string{"admin"}, Permissions:[]string{"configs:apply"},
	}}
	if err=pool.QueryRow(ctx,`INSERT INTO users(email,display_name,user_type,status)
	 VALUES('revocation-admin@isolated.invalid','Revocation operator','human','active')
	 RETURNING id::text`).Scan(&actor.ID); err!=nil { t.Fatal(err) }
	if err=pool.QueryRow(ctx,`INSERT INTO auth_sessions(user_id,token_hash,expires_at)
	 VALUES($1::uuid,'rg140-isolated-human',clock_timestamp()+interval '1 hour')
	 RETURNING id::text`,actor.ID).Scan(&actor.SessionID); err!=nil { t.Fatal(err) }
	if _,err=pool.Exec(ctx,`INSERT INTO roles(code,name) VALUES('admin','Admin') ON CONFLICT(code) DO NOTHING;
	 INSERT INTO permissions(code,name) VALUES('configs:apply','Apply config') ON CONFLICT(code) DO NOTHING`);err!=nil{t.Fatal(err)}
	if _,err=pool.Exec(ctx,`INSERT INTO user_roles(user_id,role_id) SELECT $1::uuid,id FROM roles WHERE code='admin'`,actor.ID);err!=nil{t.Fatal(err)}
	if _,err=pool.Exec(ctx,`INSERT INTO role_permissions(role_id,permission_id)
	 SELECT r.id,p.id FROM roles r,permissions p WHERE r.code='admin' AND p.code='configs:apply'
	 ON CONFLICT DO NOTHING`);err!=nil{t.Fatal(err)}
	authorized:=auth.ContextWithUser(ctx,actor)
	withoutPermission:=actor
	withoutPermission.Permissions=nil
	withoutRole:=actor
	withoutRole.Roles=[]string{"read_only"}
	inp:=revocations.PreparedRemovalDispatchApprovalInput{OperationID:prepared.ID,ConfirmSharedProcessRestart:true}
	for _,bad:=range []struct{name string; ctx context.Context; input revocations.PreparedRemovalDispatchApprovalInput}{
		{"no_context",ctx,inp},
		{"no_confirmation",authorized,revocations.PreparedRemovalDispatchApprovalInput{OperationID:prepared.ID}},
		{"invalid_operation",authorized,revocations.PreparedRemovalDispatchApprovalInput{OperationID:"invalid",ConfirmSharedProcessRestart:true}},
		{"no_permission",auth.ContextWithUser(ctx,withoutPermission),inp},
		{"no_role",auth.ContextWithUser(ctx,withoutRole),inp},
	}{
		t.Run(bad.name,func(t *testing.T){
			if _,err:=svc.IssuePreparedRemovalDispatchApproval(bad.ctx,bad.input); !errors.Is(err,revocations.ErrPreflight) {
				t.Fatal("unapproved human dispatch authority created",err)
			}
		})
	}
	var wg sync.WaitGroup
	results:=make(chan struct{approval revocations.PreparedRemovalDispatchApproval; err error},2)
	for i:=0;i<2;i++{
		wg.Add(1)
		go func(){
			defer wg.Done()
			a,e:=svc.IssuePreparedRemovalDispatchApproval(authorized,inp)
			results<-struct{approval revocations.PreparedRemovalDispatchApproval;err error}{a,e}
		}()
	}
	wg.Wait()
	a,b:=<-results,<-results
	if a.err!=nil||b.err!=nil||a.approval!=b.approval ||
		a.approval.OperationID!=prepared.ID ||
		a.approval.CandidateVersionID!=ids.CandidateVersionID ||
		a.approval.ApplyJobID!=ids.ApplyJobID ||
		a.approval.AgentGeneration!=ids.AgentGeneration ||
		a.approval.CandidateHash!=ids.CandidateHash ||
		a.approval.CandidateRuntimeHash!=ids.CandidateRuntimeHash ||
		len(a.approval.Nonce)!=64 ||
		a.approval.ExpiresAt.Sub(a.approval.IssuedAt)>2*time.Minute {
		t.Fatal("human authority not uniquely bound to original job/candidate",a,b)
	}
	var count,versionCount,jobCount int
	if err=pool.QueryRow(ctx,`SELECT
	 (SELECT count(*) FROM vpn_credential_removal_dispatch_authorizations),
	 (SELECT count(*) FROM config_versions),
	 (SELECT count(*) FROM config_apply_jobs)`).Scan(&count,&versionCount,&jobCount); err!=nil ||
		count!=1 || versionCount!=initialVersions || jobCount!=initialJobs {
		t.Fatal("authorization changed executable jobs/versions or duplicated authority",
			count,versionCount,jobCount,initialVersions,initialJobs,err)
	}
	for _,sql:=range []string{
		`UPDATE vpn_credential_removal_dispatch_authorizations SET expires_at=expires_at+interval '1 minute'`,
		`DELETE FROM vpn_credential_removal_dispatch_authorizations`,
	}{
		if _,err=pool.Exec(ctx,sql);err==nil {t.Fatal("historical approval mutable",sql)}
		var p *pgconn.PgError
		if !errors.As(err,&p)||p.Code!="P0153" {t.Fatal("wrong immutable authorization guard",err)}
	}
	if _,err=pool.Exec(ctx,`UPDATE auth_sessions SET revoked_at=clock_timestamp()
	 WHERE id=$1::uuid`,actor.SessionID);err!=nil {t.Fatal(err)}
	if _,err=svc.IssuePreparedRemovalDispatchApproval(authorized,inp);!errors.Is(err,revocations.ErrPreflight){
		t.Fatal("revoked human session renewed authority",err)
	}
	if _,err=pool.Exec(ctx,`INSERT INTO config_apply_jobs
	 (server_id,agent_id,config_version_id,action)
	 VALUES($1::uuid,$2::uuid,$3::uuid,'apply')`,
		prepared.ServerID,prepared.AgentID,ids.BaselineVersionID);err==nil {
		t.Fatal("new authorization bypassed normal reserved-node apply guard")
	}
	var guard *pgconn.PgError
	if !errors.As(err,&guard)||guard.Code!="P0141" {t.Fatal("normal Apply guard changed",err)}
	if _,err=svc.Cancel(ctx,prepared.ID,in.Actor);err!=nil {t.Fatal(err)}
	if _,err=svc.IssuePreparedRemovalDispatchApproval(authorized,inp);!errors.Is(err,revocations.ErrPreflight) {
		t.Fatal("cancelled preparation renewed dispatch authority",err)
	}
	heldDown, err := os.ReadFile("../../migrations/000176_credential_removal_held_dispatch.down.sql")
	if err != nil { t.Fatal(err) }
	if _, err = pool.Exec(ctx, string(heldDown)); err != nil { t.Fatal("empty held dispatch downgrade", err) }
	stageDown,err:=os.ReadFile("../../migrations/000175_credential_removal_dispatch_stages.down.sql")
	if err!=nil{t.Fatal(err)}
	if _,err=pool.Exec(ctx,string(stageDown));err!=nil{t.Fatal("empty stage downgrade",err)}
	down,err:=os.ReadFile("../../migrations/000174_credential_removal_dispatch_authorizations.down.sql")
	if err!=nil {t.Fatal(err)}
	if _,err=pool.Exec(ctx,string(down));err==nil {t.Fatal("authorization downgrade erased durable human decision")}
	if err=pool.QueryRow(ctx,`SELECT count(*) FROM vpn_credential_removal_dispatch_authorizations`).Scan(&count);err!=nil||count!=1 {
		t.Fatal("failed downgrade erased authority history",err)
	}
}
