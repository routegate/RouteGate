package db

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// Every fixture is an isolated, disposable PostgreSQL schema. The rehearsal
// SQL and all issuer/consumer functions are test-only Go code and never
// migrate or alter a RouteGate installation.
type enrollmentOneShotFixtureV1 struct {
	recoveryReservationFixture
}

func newEnrollmentOneShotFixtureV1(t *testing.T) enrollmentOneShotFixtureV1 {
	t.Helper()
	f:=newRecoveryReservationFixture(t)
	f.enableFencing(t)
	if _,err:=f.pool.Exec(f.ctx,enrollmentOneShotRehearsalSchemaV1);err!=nil{
		f.done()
		t.Fatal("install disposable one-shot rehearsal tables: ",err)
	}
	if _,err:=prepareRecoveryEnrollmentV1(f.ctx,f);err!=nil{
		f.done()
		t.Fatal("create historical preparation for isolated Agent: ",err)
	}
	return enrollmentOneShotFixtureV1{f}
}

func requireOneShotRefusedV1(t *testing.T,err error){
	t.Helper()
	if !errors.Is(err,errEnrollmentOneShotRehearsalRefusedV1){
		t.Fatalf("expected one-shot fail-closed refusal, got %v",err)
	}
}

func observedOneShotStateV1(t *testing.T,f enrollmentOneShotFixtureV1,id string)(string,int){
	t.Helper()
	var state string
	var audit int
	err:=f.pool.QueryRow(f.ctx,`
SELECT o.state,(SELECT count(*) FROM rg140_test_enrollment_attempt_events e
   WHERE e.attempt_id=o.id) FROM rg140_test_enrollment_attempts o
WHERE o.id=$1::uuid
`,id).Scan(&state,&audit)
	if err!=nil{t.Fatal(err)}
	return state,audit
}

func TestRuntimeRecoveryEnrollmentOneShotRehearsalV1SingleUseAndUnattested(t *testing.T){
	f:=newEnrollmentOneShotFixtureV1(t)
	defer f.done()
	issued,err:=issueEnrollmentOneShotRehearsalV1(f.ctx,f.pool,f.agentID)
	if err!=nil||!issued.Unattested||len(issued.ID)!=36||
		len(issued.ExpectedGenesisSHA256)!=64||issued.Nonce==""{
		t.Fatalf("no durable Manager challenge was issued: %+v %v",issued,err)
	}
	var storedNonceHash string
	err=f.pool.QueryRow(f.ctx,`
SELECT nonce_sha256 FROM rg140_test_enrollment_attempts WHERE id=$1::uuid
`,issued.ID).Scan(&storedNonceHash)
	if err!=nil||len(storedNonceHash)!=64||strings.Contains(storedNonceHash,issued.Nonce){
		t.Fatalf("plaintext nonce stored in manager ledger: %q %v",storedNonceHash,err)
	}
	state,n:=observedOneShotStateV1(t,f,issued.ID)
	if state!="issued_unattested"||n!=1{t.Fatalf("issuance not audit-atomic: %s/%d",state,n)}
	result,err:=consumeEnrollmentOneShotRehearsalV1(
		f.ctx,f.pool,issued.ID,issued.Nonce,recoveryFixtureAgentToken,
		issued.ExpectedGenesisSHA256,
	)
	if err!=nil||!result.Unattested||result.State!="reply_matched_unattested"{
		t.Fatalf("matched response improperly elevated or refused: %+v %v",result,err)
	}
	state,n=observedOneShotStateV1(t,f,issued.ID)
	if state!="reply_matched_unattested"||n!=2{
		t.Fatalf("one-shot consume not atomically audited: %s/%d",state,n)
	}
	_,err=consumeEnrollmentOneShotRehearsalV1(
		f.ctx,f.pool,issued.ID,issued.Nonce,recoveryFixtureAgentToken,
		issued.ExpectedGenesisSHA256,
	)
	requireOneShotRefusedV1(t,err)
	state,n=observedOneShotStateV1(t,f,issued.ID)
	if state!="reply_matched_unattested"||n!=2{t.Fatalf("replay mutated state: %s/%d",state,n)}
	_,err=issueEnrollmentOneShotRehearsalV1(f.ctx,f.pool,f.agentID)
	requireOneShotRefusedV1(t,err)
}

func TestRuntimeRecoveryEnrollmentOneShotRehearsalV1WrongBearerOrNonceDoesNotConsume(t *testing.T){
	f:=newEnrollmentOneShotFixtureV1(t)
	defer f.done()
	issued,err:=issueEnrollmentOneShotRehearsalV1(f.ctx,f.pool,f.agentID)
	if err!=nil{t.Fatal(err)}
	for _,tc:=range []struct{ name,nonce,bearer string }{
		{"wrong_bearer",issued.Nonce,"incorrect-agent-token"},
		{"malformed_nonce","invalid!!!",recoveryFixtureAgentToken},
		{"different_nonce","AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",recoveryFixtureAgentToken},
	}{
		t.Run(tc.name,func(t *testing.T){
			_,err:=consumeEnrollmentOneShotRehearsalV1(f.ctx,f.pool,
				issued.ID,tc.nonce,tc.bearer,issued.ExpectedGenesisSHA256)
			requireOneShotRefusedV1(t,err)
			state,n:=observedOneShotStateV1(t,f,issued.ID)
			if state!="issued_unattested"||n!=1{
				t.Fatalf("unknown bearer/nonce consumed legitimate attempt: %s/%d",state,n)
			}
		})
	}
}

func TestRuntimeRecoveryEnrollmentOneShotRehearsalV1WrongDigestConsumedAsRejection(t *testing.T){
	f:=newEnrollmentOneShotFixtureV1(t)
	defer f.done()
	issued,err:=issueEnrollmentOneShotRehearsalV1(f.ctx,f.pool,f.agentID)
	if err!=nil{t.Fatal(err)}
	result,err:=consumeEnrollmentOneShotRehearsalV1(f.ctx,f.pool,issued.ID,
		issued.Nonce,recoveryFixtureAgentToken,strings.Repeat("a",64))
	if err!=nil||!result.Unattested||result.State!="reply_rejected"{
		t.Fatalf("mismatched remote claim not consumed as rejection: %+v %v",result,err)
	}
	state,n:=observedOneShotStateV1(t,f,issued.ID)
	if state!="reply_rejected"||n!=2{t.Fatalf("rejection was replayable: %s/%d",state,n)}
	_,err=consumeEnrollmentOneShotRehearsalV1(f.ctx,f.pool,
		issued.ID,issued.Nonce,recoveryFixtureAgentToken,issued.ExpectedGenesisSHA256)
	requireOneShotRefusedV1(t,err)
}

func TestRuntimeRecoveryEnrollmentOneShotRehearsalV1NoPreparationRefused(t *testing.T){
	f:=newRecoveryReservationFixture(t)
	defer f.done()
	f.enableFencing(t)
	if _,err:=f.pool.Exec(f.ctx,enrollmentOneShotRehearsalSchemaV1);err!=nil{t.Fatal(err)}
	got,err:=issueEnrollmentOneShotRehearsalV1(f.ctx,f.pool,f.agentID)
	requireOneShotRefusedV1(t,err)
	if got!=(enrollmentOneShotRehearsalIssueV1{}){t.Fatal("unprepared Agent received challenge",got)}
}

func TestRuntimeRecoveryEnrollmentOneShotRehearsalV1RefusesCredentialRotationAndNewWork(t *testing.T){
	for _,mode:=range []string{"rotation","disabled","capability_lost","new_job","recovery_hold"}{
		t.Run(mode,func(t *testing.T){
			f:=newEnrollmentOneShotFixtureV1(t)
			defer f.done()
			issued,err:=issueEnrollmentOneShotRehearsalV1(f.ctx,f.pool,f.agentID)
			if err!=nil{t.Fatal(err)}
			switch mode{
			case "rotation":
				_,err=f.pool.Exec(f.ctx,`UPDATE agents SET credential_generation=credential_generation+1,
last_authenticated_heartbeat_at=NULL,last_authenticated_heartbeat_generation=NULL WHERE id=$1::uuid`,f.agentID)
			case "disabled":
				_,err=f.pool.Exec(f.ctx,`UPDATE agents SET status='disabled' WHERE id=$1::uuid`,f.agentID)
			case "capability_lost":
				_,err=f.pool.Exec(f.ctx,`UPDATE agents SET capabilities='{}'::jsonb WHERE id=$1::uuid`,f.agentID)
			case "new_job":
				_,err=f.pool.Exec(f.ctx,`INSERT INTO config_apply_jobs(
server_id,agent_id,config_version_id,action,status)
VALUES($1::uuid,$2::uuid,$3::uuid,'apply','pending')`,
					f.serverID,f.agentID,f.configVersionID)
			case "recovery_hold":
				err=f.reserve(f.ctx,f.generation,"config_apply")
			}
			if err!=nil{t.Fatal(err)}
			_,err=consumeEnrollmentOneShotRehearsalV1(f.ctx,f.pool,issued.ID,
				issued.Nonce,recoveryFixtureAgentToken,issued.ExpectedGenesisSHA256)
			requireOneShotRefusedV1(t,err)
			state,n:=observedOneShotStateV1(t,f,issued.ID)
			if state!="issued_unattested"||n!=1{
				t.Fatalf("stale or busy Agent challenge resolved: %s/%d",state,n)
			}
		})
	}
}

func TestRuntimeRecoveryEnrollmentOneShotRehearsalV1ExpiresWithoutReplay(t *testing.T){
	f:=newEnrollmentOneShotFixtureV1(t)
	defer f.done()
	issued,err:=issueEnrollmentOneShotRehearsalV1(f.ctx,f.pool,f.agentID)
	if err!=nil{t.Fatal(err)}
	// Controlled clock simulation in disposable test schema only.
	_,err=f.pool.Exec(f.ctx,`UPDATE rg140_test_enrollment_attempts
 SET issued_at=clock_timestamp()-interval '61 seconds',
     expires_at=clock_timestamp()-interval '31 seconds'
 WHERE id=$1::uuid`,issued.ID)
	if err!=nil{t.Fatal(err)}
	_,err=consumeEnrollmentOneShotRehearsalV1(f.ctx,f.pool,issued.ID,
		issued.Nonce,recoveryFixtureAgentToken,issued.ExpectedGenesisSHA256)
	requireOneShotRefusedV1(t,err)
	_,err=issueEnrollmentOneShotRehearsalV1(f.ctx,f.pool,f.agentID)
	requireOneShotRefusedV1(t,err)
}

func TestRuntimeRecoveryEnrollmentOneShotRehearsalV1ConcurrentOnlyOneIssue(t *testing.T){
	f:=newEnrollmentOneShotFixtureV1(t)
	defer f.done()
	ctx,cancel:=context.WithTimeout(f.ctx,12*time.Second)
	defer cancel()
	start:=make(chan struct{})
	out:=make(chan error,2)
	var wg sync.WaitGroup
	for i:=0;i<2;i++{
		wg.Add(1)
		go func(){
			defer wg.Done()
			<-start
			_,err:=issueEnrollmentOneShotRehearsalV1(ctx,f.pool,f.agentID)
			out<-err
		}()
	}
	close(start);wg.Wait();close(out)
	ok,refused:=0,0
	for err:=range out{
		if err==nil{ok++}else if errors.Is(err,errEnrollmentOneShotRehearsalRefusedV1){
			refused++
		}else{t.Fatal(err)}
	}
	if ok!=1||refused!=1{t.Fatalf("two active Manager challenges: %d/%d",ok,refused)}
	var attempts,events int
	err:=f.pool.QueryRow(f.ctx,`SELECT
 (SELECT count(*) FROM rg140_test_enrollment_attempts),
 (SELECT count(*) FROM rg140_test_enrollment_attempt_events)`).Scan(&attempts,&events)
	if err!=nil||attempts!=1||events!=1{
		t.Fatalf("duplicate attempt or audit: %d/%d %v",attempts,events,err)
	}
}

func TestRuntimeRecoveryEnrollmentOneShotRehearsalV1ConcurrentOnlyOneConsume(t *testing.T){
	f:=newEnrollmentOneShotFixtureV1(t)
	defer f.done()
	issued,err:=issueEnrollmentOneShotRehearsalV1(f.ctx,f.pool,f.agentID)
	if err!=nil{t.Fatal(err)}
	ctx,cancel:=context.WithTimeout(f.ctx,12*time.Second)
	defer cancel()
	start:=make(chan struct{})
	out:=make(chan error,2)
	var wg sync.WaitGroup
	for i:=0;i<2;i++{
		wg.Add(1)
		go func(){
			defer wg.Done();<-start
			_,err:=consumeEnrollmentOneShotRehearsalV1(ctx,f.pool,issued.ID,
				issued.Nonce,recoveryFixtureAgentToken,issued.ExpectedGenesisSHA256)
			out<-err
		}()
	}
	close(start);wg.Wait();close(out)
	ok,refused:=0,0
	for err:=range out{
		if err==nil{ok++}else if errors.Is(err,errEnrollmentOneShotRehearsalRefusedV1){
			refused++
		}else{t.Fatal(err)}
	}
	if ok!=1||refused!=1{t.Fatalf("nonce consumed more than once: %d/%d",ok,refused)}
	state,n:=observedOneShotStateV1(t,f,issued.ID)
	if state!="reply_matched_unattested"||n!=2{
		t.Fatalf("concurrent reply lacked single atomic audit: %s/%d",state,n)
	}
}

func blockOneShotAuditV1(t *testing.T,f enrollmentOneShotFixtureV1,target string){
	t.Helper()
	// Only known test-case constants are interpolated, never external input.
	stmt:=`CREATE FUNCTION rg140_test_fail_enroll_audit() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.state='`+target+`' THEN RAISE EXCEPTION 'injected enrollment audit error'; END IF;
 RETURN NEW;
END; $$;`
	_,err:=f.pool.Exec(f.ctx,stmt)
	if err!=nil{t.Fatal(err)}
	_,err=f.pool.Exec(f.ctx,`
CREATE TRIGGER rg140_test_fail_enroll_audit_trigger
BEFORE INSERT ON rg140_test_enrollment_attempt_events
FOR EACH ROW EXECUTE FUNCTION rg140_test_fail_enroll_audit();
`)
	if err!=nil{t.Fatal(err)}
}

func TestRuntimeRecoveryEnrollmentOneShotRehearsalV1AuditIssueFailureRollsBack(t *testing.T){
	f:=newEnrollmentOneShotFixtureV1(t)
	defer f.done()
	blockOneShotAuditV1(t,f,"issued_unattested")
	got,err:=issueEnrollmentOneShotRehearsalV1(f.ctx,f.pool,f.agentID)
	requireOneShotRefusedV1(t,err)
	if got!=(enrollmentOneShotRehearsalIssueV1{}){t.Fatal("audit failure leaked nonce receipt")}
	var attempts,events int
	err=f.pool.QueryRow(f.ctx,`SELECT
 (SELECT count(*) FROM rg140_test_enrollment_attempts),
 (SELECT count(*) FROM rg140_test_enrollment_attempt_events)`).Scan(&attempts,&events)
	if err!=nil||attempts!=0||events!=0{
		t.Fatalf("challenge survived failed audit: %d/%d %v",attempts,events,err)
	}
}

func TestRuntimeRecoveryEnrollmentOneShotRehearsalV1AuditConsumeFailureRollsBack(t *testing.T){
	f:=newEnrollmentOneShotFixtureV1(t)
	defer f.done()
	issued,err:=issueEnrollmentOneShotRehearsalV1(f.ctx,f.pool,f.agentID)
	if err!=nil{t.Fatal(err)}
	blockOneShotAuditV1(t,f,"reply_matched_unattested")
	got,err:=consumeEnrollmentOneShotRehearsalV1(f.ctx,f.pool,issued.ID,
		issued.Nonce,recoveryFixtureAgentToken,issued.ExpectedGenesisSHA256)
	requireOneShotRefusedV1(t,err)
	if got!=(enrollmentOneShotRehearsalReplyV1{}){t.Fatal("failed audit leaked reply receipt")}
	state,n:=observedOneShotStateV1(t,f,issued.ID)
	if state!="issued_unattested"||n!=1{
		t.Fatalf("consume committed without atomic audit: %s/%d",state,n)
	}
}

func TestRuntimeRecoveryEnrollmentOneShotRehearsalV1LegacyAgentJobsUnchanged(t *testing.T){
	f:=newEnrollmentOneShotFixtureV1(t)
	defer f.done()
	_,err:=issueEnrollmentOneShotRehearsalV1(f.ctx,f.pool,f.agentID)
	if err!=nil{t.Fatal(err)}
	// The test ledger is not wired to normal Agent tasks; it must not
	// silently introduce a system-wide gate for legacy VPN traffic.
	var job string
	err=f.pool.QueryRow(f.ctx,`INSERT INTO config_apply_jobs(
server_id,agent_id,config_version_id,action,status)
VALUES($1::uuid,$2::uuid,$3::uuid,'apply','pending') RETURNING id::text`,
		f.serverID,f.agentID,f.configVersionID).Scan(&job)
	if err!=nil||job==""{t.Fatalf("test-only ledger changed existing Agent paths: %v",err)}
}

