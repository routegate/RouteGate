package db

import (
	"errors"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/ikaevus/routegate/backend/internal/auth"
	"github.com/ikaevus/routegate/backend/internal/revocations"
	"github.com/jackc/pgx/v5/pgconn"
)

func dispatchReviewUser() auth.AuthenticatedUser {
	return auth.AuthenticatedUser{UserProfile: auth.UserProfile{
		ID: "11111111-1111-4111-8111-111111111111",
		UserType: auth.UserTypeHuman, Status: "active",
		Roles: []string{auth.RoleSuperAdmin},
		Permissions: []string{"system:manage", "configs:apply", "vpn_users:disable"},
	}, SessionID: "22222222-2222-4222-8222-222222222222"}
}

func TestRevocationDispatchClaimNeverDispatchesAndNeverRetries(t *testing.T) {
	ctx,pool,in,_,done:=removalPreparationFixture(t)
	defer done()
	user:=dispatchReviewUser()
	userCtx:=auth.ContextWithUser(ctx,user)
	in.Actor=""
	p,err:=revocations.NewAuthorizedPreparation(revocations.NewService(pool)).Prepare(userCtx,in,true)
	if err!=nil {t.Fatal(err)}
	var hash string
	if err=pool.QueryRow(ctx,`SELECT candidate_hash FROM vpn_credential_revocations WHERE id=$1::uuid`,p.ID).Scan(&hash);err!=nil{t.Fatal(err)}
	confirm:=revocations.NewAuthorizedConfirmation(pool)
	if _,err=confirm.Confirm(userCtx,revocations.ConfirmInput{
		OperationID:p.ID,ExpectedCandidateHash:hash,SharedProcessRestartAcknowledged:true,
	});err!=nil{t.Fatalf("confirm intent: %v",err)}
	claim:=revocations.NewAuthorizedDispatchClaim(pool)
	request:=revocations.DispatchClaimInput{OperationID:p.ID,ExpectedCandidateHash:hash,ExplicitExecutionApproval:true}

	// Every rejection must precede durable DB mutation and must not create
	// an Agent job, including lack of fresh execution consent or principal.
	for name,mutate:=range map[string]func(*revocations.DispatchClaimInput){
		"no execution consent":func(v *revocations.DispatchClaimInput){v.ExplicitExecutionApproval=false},
		"stale preview":func(v *revocations.DispatchClaimInput){v.ExpectedCandidateHash=strings.Repeat("f",64)},
		"invalid ID":func(v *revocations.DispatchClaimInput){v.OperationID="garbage"},
	}{
		t.Run(name,func(t *testing.T){
			bad:=request; mutate(&bad)
			if _,err:=claim.Claim(userCtx,bad);err!=revocations.ErrPreflight{
				t.Fatalf("unapproved dispatch claim was accepted: %v",err)
			}
		})
	}
	if _,err:=claim.Claim(ctx,request);err!=revocations.ErrPreflight{
		t.Fatalf("anonymous request was accepted: %v",err)
	}
	other:=dispatchReviewUser()
	other.ID="33333333-3333-4333-8333-333333333333"
	other.SessionID="44444444-4444-4444-8444-444444444444"
	if _,err:=claim.Claim(auth.ContextWithUser(ctx,other),request);err!=revocations.ErrPreflight{
		t.Fatalf("a different super-admin captured someone else's confirmation: %v",err)
	}

	// A direct SQL insert also must enforce the original immutable binding.
	forgedSQL:=`INSERT INTO vpn_credential_revocation_dispatch_claims
	 (operation_id,claimed_by,server_id,agent_id,agent_credential_generation,
	  baseline_version_id,confirmation_at,candidate_hash,candidate_runtime_hash)
	 SELECT id,requested_by,server_id,agent_id,agent_credential_generation,
	        baseline_version_id,now(),repeat('f',64),candidate_runtime_hash
	 FROM vpn_credential_revocations WHERE id=$1::uuid`
	_,err=pool.Exec(ctx,forgedSQL,p.ID)
	var pgErr *pgconn.PgError
	if !errors.As(err,&pgErr)||pgErr.Code!="P0141"{
		t.Fatalf("forged claim snapshot not rejected by SQL: %v",err)
	}

	// An Agent could go offline after confirmation: reject stale identity
	// even though the prepared/confirmed rows remain correctly persisted.
	if _,err=pool.Exec(ctx,`UPDATE agents SET last_authenticated_heartbeat_at=now()-interval '10 minutes' WHERE id=$1::uuid`,p.AgentID);err!=nil{t.Fatal(err)}
	if _,err=claim.Claim(userCtx,request);err!=revocations.ErrPreflight{
		t.Fatalf("stale Agent was claimed for possible dispatch: %v",err)
	}
	if _,err=pool.Exec(ctx,`UPDATE agents SET last_authenticated_heartbeat_at=now() WHERE id=$1::uuid`,p.AgentID);err!=nil{t.Fatal(err)}

	var first,second revocations.DispatchClaim
	var e1,e2 error
	var wg sync.WaitGroup
	wg.Add(2)
	go func(){defer wg.Done();first,e1=claim.Claim(userCtx,request)}()
	go func(){defer wg.Done();second,e2=claim.Claim(userCtx,request)}()
	wg.Wait()
	valid:=func(a revocations.DispatchClaim,e error)bool{return e==nil&&a.OperationID==p.ID&&a.ClaimedBy==user.ID&&a.State=="outcome_unknown"&&!a.ClaimedAt.IsZero()}
	if !((valid(first,e1)&&errors.Is(e2,revocations.ErrDispatchClaimAlreadyExists))||
		(valid(second,e2)&&errors.Is(e1,revocations.ErrDispatchClaimAlreadyExists))){
		t.Fatalf("two concurrent dispatch claims were not fenced: first=%+v %v second=%+v %v",first,e1,second,e2)
	}
	if _,err=claim.Claim(userCtx,request);!errors.Is(err,revocations.ErrDispatchClaimAlreadyExists){
		t.Fatalf("replay/restart would be allowed to resend: %v",err)
	}

	var count,jobs,events int
	if err=pool.QueryRow(ctx,`SELECT
	  (SELECT count(*) FROM vpn_credential_revocation_dispatch_claims),
	  (SELECT count(*) FROM agent_operation_jobs WHERE server_id=$1::uuid),
	  (SELECT count(*) FROM vpn_credential_revocation_events WHERE operation_id=$2::uuid)
	`,in.ServerID,p.ID).Scan(&count,&jobs,&events);err!=nil{t.Fatal(err)}
	if count!=1||jobs!=0||events!=1{
		t.Fatalf("duplicate, dispatch or unapproved audit: claims=%d Agent jobs=%d events=%d",count,jobs,events)
	}

	// Once a claim might have been transmitted, *cancellation is unsafe*.
	if _,err=revocations.NewAuthorizedPreparation(revocations.NewService(pool)).Cancel(userCtx,p.ID);err!=revocations.ErrPreflight{
		t.Fatalf("claim was cancelled after possible dispatch: %v",err)
	}
	if _,err=pool.Exec(ctx,`UPDATE vpn_credential_revocations SET state='cancelled',cancelled_at=now() WHERE id=$1::uuid`,p.ID);!errors.As(err,&pgErr)||pgErr.Code!="P0141"{
		t.Fatalf("direct SQL bypassed the cancellation fence: %v",err)
	}
	for _,stmt:=range []string{
		"UPDATE vpn_credential_revocation_dispatch_claims SET state='outcome_unknown' WHERE operation_id=$1::uuid",
		"DELETE FROM vpn_credential_revocation_dispatch_claims WHERE operation_id=$1::uuid",
	}{
		if _,err=pool.Exec(ctx,stmt,p.ID);!errors.As(err,&pgErr)||pgErr.Code!="P0141"{
			t.Fatalf("immutable dispatch claim changed: %s: %v",stmt,err)
		}
	}
	down,err:=os.ReadFile("../../migrations/000163_credential_revocation_dispatch_claims.down.sql")
	if err!=nil{t.Fatal(err)}
	if _,err=pool.Exec(ctx,string(down));!errors.As(err,&pgErr)||pgErr.Code!="P0141"{
		t.Fatalf("downgrade erased possible dispatch history: %v",err)
	}
	if err=pool.QueryRow(ctx,`SELECT count(*) FROM vpn_credential_revocation_dispatch_claims`).Scan(&count);err!=nil||count!=1{
		t.Fatalf("failed downgrade partially removed safety record: %d %v",count,err)
	}
}

func TestRevocationClaimRequiresDurableConfirmationAndPreparedParent(t *testing.T) {
	ctx,pool,in,_,done:=removalPreparationFixture(t)
	defer done()
	user:=dispatchReviewUser()
	userCtx:=auth.ContextWithUser(ctx,user)
	in.Actor=""
	p,err:=revocations.NewAuthorizedPreparation(revocations.NewService(pool)).Prepare(userCtx,in,true)
	if err!=nil{t.Fatal(err)}
	var hash string
	if err=pool.QueryRow(ctx,`SELECT candidate_hash FROM vpn_credential_revocations WHERE id=$1::uuid`,p.ID).Scan(&hash);err!=nil{t.Fatal(err)}
	claim:=revocations.NewAuthorizedDispatchClaim(pool)
	input:=revocations.DispatchClaimInput{OperationID:p.ID,ExpectedCandidateHash:hash,ExplicitExecutionApproval:true}
	if _,err=claim.Claim(userCtx,input);err!=revocations.ErrPreflight{
		t.Fatalf("unconfirmed operation claimed: %v",err)
	}
	if _,err=revocations.NewAuthorizedConfirmation(pool).Confirm(userCtx,revocations.ConfirmInput{
		OperationID:p.ID,ExpectedCandidateHash:hash,SharedProcessRestartAcknowledged:true,
	});err!=nil{t.Fatal(err)}
	if _,err=revocations.NewAuthorizedPreparation(revocations.NewService(pool)).Cancel(userCtx,p.ID);err!=nil{
		t.Fatalf("undispatched confirmed operation should remain cancellable: %v",err)
	}
	if _,err=claim.Claim(userCtx,input);err!=revocations.ErrPreflight{
		t.Fatalf("cancelled confirmed operation claimed: %v",err)
	}
	var count int
	if err=pool.QueryRow(ctx,"SELECT count(*) FROM vpn_credential_revocation_dispatch_claims").Scan(&count);err!=nil||count!=0{
		t.Fatalf("ineligible operation left dispatch claim: %d %v",count,err)
	}
}
