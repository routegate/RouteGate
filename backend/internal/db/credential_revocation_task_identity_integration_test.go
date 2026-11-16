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

func prepareClaimForIdentityTests(t *testing.T) (
	*revocations.AuthorizedTaskIdentityReservation,
	revocations.ReserveTaskIdentityInput,
	string, string,
	func(),
) {
	t.Helper()
	ctx,pool,in,_,done:=removalPreparationFixture(t)
	user:=dispatchReviewUser()
	authCtx:=auth.ContextWithUser(ctx,user)
	in.Actor=""
	prepared,err:=revocations.NewAuthorizedPreparation(revocations.NewService(pool)).Prepare(authCtx,in,true)
	if err!=nil { done();t.Fatal(err) }
	var hash string
	if err=pool.QueryRow(ctx,`SELECT candidate_hash FROM vpn_credential_revocations WHERE id=$1::uuid`,prepared.ID).Scan(&hash);err!=nil { done();t.Fatal(err) }
	if _,err=revocations.NewAuthorizedConfirmation(pool).Confirm(authCtx,revocations.ConfirmInput{
		OperationID:prepared.ID,ExpectedCandidateHash:hash,SharedProcessRestartAcknowledged:true,
	});err!=nil { done();t.Fatal(err) }
	claim,err:=revocations.NewAuthorizedDispatchClaim(pool).Claim(authCtx,revocations.DispatchClaimInput{
		OperationID:prepared.ID,ExpectedCandidateHash:hash,ExplicitExecutionApproval:true,
	})
	if err!=nil { done();t.Fatal(err) }
	return revocations.NewAuthorizedTaskIdentityReservation(pool),
		revocations.ReserveTaskIdentityInput{
			OperationID:prepared.ID,ClaimID:claim.ID,ExpectedCandidateHash:hash,
		},in.ServerID,user.ID,done
}

// Two callers must never receive two task identities for a claim, even when
// they start at once. No response authorizes a transport send or Agent job.
func TestCredentialRemovalTaskIdentityOnePerClaimAndImmutable(t *testing.T) {
	ctx,pool,in,_,done:=removalPreparationFixture(t)
	defer done()
	user:=dispatchReviewUser()
	userCtx:=auth.ContextWithUser(ctx,user)
	in.Actor=""
	p,err:=revocations.NewAuthorizedPreparation(revocations.NewService(pool)).Prepare(userCtx,in,true)
	if err!=nil {t.Fatal(err)}
	var hash string
	if err=pool.QueryRow(ctx,`SELECT candidate_hash FROM vpn_credential_revocations WHERE id=$1::uuid`,p.ID).Scan(&hash);err!=nil{t.Fatal(err)}
	_,err=revocations.NewAuthorizedConfirmation(pool).Confirm(userCtx,revocations.ConfirmInput{OperationID:p.ID,ExpectedCandidateHash:hash,SharedProcessRestartAcknowledged:true})
	if err!=nil {t.Fatal(err)}
	claim,err:=revocations.NewAuthorizedDispatchClaim(pool).Claim(userCtx,revocations.DispatchClaimInput{OperationID:p.ID,ExpectedCandidateHash:hash,ExplicitExecutionApproval:true})
	if err!=nil {t.Fatal(err)}
	svc:=revocations.NewAuthorizedTaskIdentityReservation(pool)
	request:=revocations.ReserveTaskIdentityInput{OperationID:p.ID,ClaimID:claim.ID,ExpectedCandidateHash:hash}

	// Refuse caller-supplied/task-unrelated identity before writing anything.
	other:=user
	other.ID="33333333-3333-4333-8333-333333333333"
	other.SessionID="44444444-4444-4444-8444-444444444444"
	badHash:=request
	badHash.ExpectedCandidateHash=strings.Repeat("f",64)
	for name,attempt:=range map[string]func()error{
		"anonymous":func()error{_,e:=svc.Reserve(ctx,request);return e},
		"foreign admin":func()error{_,e:=svc.Reserve(auth.ContextWithUser(ctx,other),request);return e},
		"changed hash":func()error{_,e:=svc.Reserve(userCtx,badHash);return e},
		"invalid claim":func()error{v:=request;v.ClaimID="not-uuid";_,e:=svc.Reserve(userCtx,v);return e},
		"unrelated claim":func()error{v:=request;v.ClaimID="99999999-9999-4999-8999-999999999999";_,e:=svc.Reserve(userCtx,v);return e},
	}{
		t.Run(name,func(t *testing.T){if e:=attempt();!errors.Is(e,revocations.ErrPreflight){t.Fatalf("unexpected reservation success: %v",e)}})
	}
	var count int
	if err=pool.QueryRow(ctx,`SELECT count(*) FROM vpn_credential_revocation_task_reservations`).Scan(&count);err!=nil||count!=0{t.Fatalf("unauthorized request persisted identity: %d %v",count,err)}

	// Stale authenticated heartbeat cannot generate a new task identity.
	if _,err=pool.Exec(ctx,`UPDATE agents SET last_authenticated_heartbeat_at=now()-interval '10 minutes' WHERE id=$1::uuid`,p.AgentID);err!=nil{t.Fatal(err)}
	if _,err=svc.Reserve(userCtx,request);!errors.Is(err,revocations.ErrPreflight){t.Fatalf("stale Agent identity reserved: %v",err)}
	if _,err=pool.Exec(ctx,`UPDATE agents SET last_authenticated_heartbeat_at=now() WHERE id=$1::uuid`,p.AgentID);err!=nil{t.Fatal(err)}

	type outcome struct{value revocations.ReservedTaskIdentity;err error}
	results:=make(chan outcome,2)
	var wg sync.WaitGroup
	wg.Add(2)
	for range 2{go func(){defer wg.Done();v,e:=svc.Reserve(userCtx,request);results<-outcome{v,e}}()}
	wg.Wait()
	a,b:=<-results,<-results
	success:=func(x outcome)bool{
		return x.err==nil&&x.value.OperationID==p.ID&&x.value.ClaimID==claim.ID&&
			x.value.State=="identity_reserved"&&!x.value.ReservedAt.IsZero()&&x.value.TaskID!=""
	}
	if !((success(a)&&errors.Is(b.err,revocations.ErrTaskIdentityAlreadyReserved))||
		(success(b)&&errors.Is(a.err,revocations.ErrTaskIdentityAlreadyReserved))){
		t.Fatalf("task identity raced: first=%+v second=%+v",a,b)
	}
	winner:=a.value
	if a.err!=nil {winner=b.value}
	if _,err=svc.Reserve(userCtx,request);!errors.Is(err,revocations.ErrTaskIdentityAlreadyReserved){
		t.Fatalf("same claim accepted for a second task identity: %v",err)
	}
	var taskID,claimID,operationID,by,server,agent,baseline,candidate,runtime,kind,state string
	var generation int64
	err=pool.QueryRow(ctx,`SELECT task_id::text,claim_id::text,operation_id::text,reserved_by,
		server_id::text,agent_id::text,agent_credential_generation,baseline_version_id::text,
		candidate_hash,candidate_runtime_hash,task_kind,state
		FROM vpn_credential_revocation_task_reservations WHERE claim_id=$1::uuid`,claim.ID).
		Scan(&taskID,&claimID,&operationID,&by,&server,&agent,&generation,&baseline,&candidate,&runtime,&kind,&state)
	if err!=nil{t.Fatal(err)}
	var expectedAgent,expectedBaseline,expectedRuntime string
	var expectedGen int64
	if err=pool.QueryRow(ctx,`SELECT agent_id::text,baseline_version_id::text,
		agent_credential_generation,candidate_runtime_hash FROM vpn_credential_revocations
		WHERE id=$1::uuid`,p.ID).Scan(&expectedAgent,&expectedBaseline,&expectedGen,&expectedRuntime);err!=nil{t.Fatal(err)}
	if taskID!=winner.TaskID||claimID!=claim.ID||operationID!=p.ID||by!=user.ID||
		server!=in.ServerID||agent!=expectedAgent||generation!=expectedGen||baseline!=expectedBaseline||
		candidate!=hash||runtime!=expectedRuntime||kind!=revocations.CredentialRemovalTaskKind||state!="identity_reserved"{
		t.Fatalf("task identity did not match the immutable operation snapshot")
	}

	// Direct SQL updates/deletes and browser-supplied task IDs are forbidden.
	statements:=[]string{
		"UPDATE vpn_credential_revocation_task_reservations SET state='identity_reserved' WHERE claim_id=$1::uuid",
		"DELETE FROM vpn_credential_revocation_task_reservations WHERE claim_id=$1::uuid",
		"INSERT INTO vpn_credential_revocation_task_reservations(claim_id,task_id) VALUES($1::uuid,gen_random_uuid())",
	}
	for _,sql:=range statements{
		_,e:=pool.Exec(ctx,sql,claim.ID)
		var pgErr *pgconn.PgError
		if !errors.As(e,&pgErr)||pgErr.Code!="P0141"{t.Fatalf("mutable or forged identity: %s: %v",sql,e)}
	}
	down,e:=os.ReadFile("../../migrations/000165_credential_revocation_task_identity.down.sql")
	if e!=nil {t.Fatal(e)}
	_,e=pool.Exec(ctx,string(down))
	var pgErr *pgconn.PgError
	if !errors.As(e,&pgErr)||pgErr.Code!="P0141"{t.Fatalf("downgrade discarded task identity: %v",e)}

	var claims,reservations,jobCount,events int
	err=pool.QueryRow(ctx,`SELECT
		(SELECT count(*) FROM vpn_credential_revocation_dispatch_claims WHERE operation_id=$1::uuid),
		(SELECT count(*) FROM vpn_credential_revocation_task_reservations WHERE operation_id=$1::uuid),
		(SELECT count(*) FROM agent_operation_jobs WHERE server_id=$2::uuid),
		(SELECT count(*) FROM vpn_credential_revocation_events WHERE operation_id=$1::uuid)
	`,p.ID,in.ServerID).Scan(&claims,&reservations,&jobCount,&events)
	if err!=nil||claims!=1||reservations!=1||jobCount!=0||events!=1{
		t.Fatalf("reservation created job/audit: claim=%d reserved=%d jobs=%d events=%d err=%v",claims,reservations,jobCount,events,err)
	}
}
