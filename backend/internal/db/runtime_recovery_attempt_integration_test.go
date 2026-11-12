package db

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ikaevus/routegate/backend/internal/recoveryauth"
	"github.com/jackc/pgx/v5/pgconn"
)

func requireAttemptGuard(t *testing.T, err error) {
	t.Helper()
	var pg *pgconn.PgError
	if !errors.As(err, &pg) || pg.Code != "P0144" {
		t.Fatalf("expected fail-closed P0144 attempt guard, got %v", err)
	}
}

func assertRecoveryAttempt(t *testing.T, f recoveryLedgerFixture, challengeID string, expected int) {
	t.Helper()
	var attempts, matching, holds int
	err := f.pool.QueryRow(f.ctx, `
SELECT (SELECT count(*) FROM vpn_runtime_recovery_attempts WHERE reservation_id=$1::uuid),
       (SELECT count(*) FROM vpn_runtime_recovery_attempts a
           JOIN vpn_runtime_recovery_challenges c ON c.id=a.challenge_id
           JOIN vpn_runtime_recovery_reservations r ON r.id=a.reservation_id
         WHERE c.id=$2::uuid AND c.state='consumed'
           AND a.challenge_consumed_at=c.consumed_at
           AND a.state='awaiting_fresh_observation'
           AND a.server_id=r.server_id AND a.agent_id=r.agent_id
           AND a.agent_credential_generation=r.agent_credential_generation
           AND a.mutation_intent_sha256=r.mutation_intent_sha256
           AND a.result_receipt_sha256=r.result_receipt_sha256
           AND a.runtime_witness_sha256=r.runtime_witness_sha256
           AND a.operator_user_id=c.operator_user_id
           AND a.operator_session_id=c.operator_session_id
           AND a.attempt_epoch>0),
       (SELECT count(*) FROM vpn_runtime_recovery_reservations WHERE id=$1::uuid)
`,f.reservationID,challengeID).Scan(&attempts,&matching,&holds)
	if err!=nil || attempts!=expected || matching!=expected || holds!=1 {
		t.Fatalf("attempt/history invalid: rows=%d matching=%d holds=%d err=%v want=%d",attempts,matching,holds,err,expected)
	}
}

func TestRuntimeRecoveryAttemptCreatedAtomicallyAfterConsumerSuccess(t *testing.T) {
	f:=newRecoveryLedgerFixture(t)
	defer f.done()
	issued,err:=recoveryauth.NewIssuer(f.pool).Issue(f.ctx,f.reservationID,verifiedRecoveryActor(f))
	if err!=nil{t.Fatal(err)}
	assertRecoveryAttempt(t,f,issued.ID,0)
	if err=recoveryauth.NewConsumer(f.pool).Consume(f.ctx,issued.ID,issued.Token,verifiedRecoveryActor(f));err!=nil{
		t.Fatal(err)
	}
	assertRecoveryAttempt(t,f,issued.ID,1)
	assertRecoveryChallengeState(t,f,issued.ID,"consumed",1)
	if err=recoveryauth.NewConsumer(f.pool).Consume(f.ctx,issued.ID,issued.Token,verifiedRecoveryActor(f));
		!errors.Is(err,recoveryauth.ErrChallengeRejected) {
		t.Fatalf("replayed human challenge succeeded: %v",err)
	}
	assertRecoveryAttempt(t,f,issued.ID,1)
}

func TestRuntimeRecoveryAttemptTamperAndDirectAdmissionAreRefused(t *testing.T) {
	f:=newRecoveryLedgerFixture(t)
	defer f.done()
	issued,err:=recoveryauth.NewIssuer(f.pool).Issue(f.ctx,f.reservationID,verifiedRecoveryActor(f))
	if err!=nil{t.Fatal(err)}

	// Direct insertion using a real but not-yet-consumed challenge is not
	// allowed. No application route exists to issue or release attempts.
	_,err=f.pool.Exec(f.ctx,`
INSERT INTO vpn_runtime_recovery_attempts (
 reservation_id,challenge_id,server_id,agent_id,job_id,job_kind,
 agent_credential_generation,operator_user_id,operator_session_id,
 mutation_intent_sha256,result_receipt_sha256,runtime_witness_sha256,
 challenge_consumed_at
)
SELECT r.id,c.id,r.server_id,r.agent_id,r.job_id,r.job_kind,
 r.agent_credential_generation,c.operator_user_id,c.operator_session_id,
 r.mutation_intent_sha256,r.result_receipt_sha256,r.runtime_witness_sha256,
 clock_timestamp()
FROM vpn_runtime_recovery_challenges c
JOIN vpn_runtime_recovery_reservations r ON r.id=c.reservation_id
WHERE c.id=$1::uuid
`,issued.ID)
	requireAttemptGuard(t,err)
	assertRecoveryAttempt(t,f,issued.ID,0)

	if err=recoveryauth.NewConsumer(f.pool).Consume(f.ctx,issued.ID,issued.Token,verifiedRecoveryActor(f));err!=nil{
		t.Fatal(err)
	}
	assertRecoveryAttempt(t,f,issued.ID,1)
	for _,sql:=range []string{
		`UPDATE vpn_runtime_recovery_attempts SET state='awaiting_fresh_observation' WHERE challenge_id=$1::uuid`,
		`DELETE FROM vpn_runtime_recovery_attempts WHERE challenge_id=$1::uuid`,
	}{
		_,err=f.pool.Exec(f.ctx,sql,issued.ID)
		requireAttemptGuard(t,err)
	}
	_,err=f.pool.Exec(f.ctx,`TRUNCATE vpn_runtime_recovery_attempts CASCADE`)
	requireAttemptGuard(t,err)
	assertRecoveryAttempt(t,f,issued.ID,1)
}

func TestRuntimeRecoveryAttemptFailureRollsBackChallengeAndAudit(t *testing.T) {
	f:=newRecoveryLedgerFixture(t)
	defer f.done()
	issued,err:=recoveryauth.NewIssuer(f.pool).Issue(f.ctx,f.reservationID,verifiedRecoveryActor(f))
	if err!=nil{t.Fatal(err)}

	// This disposable database deliberately refuses writing the new attempt.
	// Neither the 000163 consumed audit event nor the state may commit.
	_,err=f.pool.Exec(f.ctx,`
CREATE FUNCTION rg140_reject_recovery_attempt()
RETURNS trigger LANGUAGE plpgsql AS
'BEGIN RAISE EXCEPTION ''injected recovery attempt persistence failure''; END';
`)
	if err!=nil{t.Fatal(err)}
	_,err=f.pool.Exec(f.ctx,`
CREATE TRIGGER rg140_reject_attempt
BEFORE INSERT ON vpn_runtime_recovery_attempts
FOR EACH ROW EXECUTE FUNCTION rg140_reject_recovery_attempt();
`)
	if err!=nil{t.Fatal(err)}
	err=recoveryauth.NewConsumer(f.pool).Consume(f.ctx,issued.ID,issued.Token,verifiedRecoveryActor(f))
	if err==nil{t.Fatal("consumption succeeded without durable recovery attempt")}
	assertRecoveryAttempt(t,f,issued.ID,0)
	assertRecoveryChallengeState(t,f,issued.ID,"issued",0)
}

func TestRuntimeRecoveryAttemptConcurrentConsumeHasExactlyOnePendingIntent(t *testing.T){
	f:=newRecoveryLedgerFixture(t)
	defer f.done()
	issued,err:=recoveryauth.NewIssuer(f.pool).Issue(f.ctx,f.reservationID,verifiedRecoveryActor(f))
	if err!=nil{t.Fatal(err)}
	ctx,cancel:=context.WithTimeout(f.ctx,12*time.Second)
	defer cancel()
	start:=make(chan struct{})
	results:=make(chan error,2)
	var wg sync.WaitGroup
	for i:=0;i<2;i++{
		wg.Add(1)
		go func(){
			defer wg.Done()
			<-start
			results<-recoveryauth.NewConsumer(f.pool).Consume(ctx,issued.ID,issued.Token,verifiedRecoveryActor(f))
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	success,denied:=0,0
	for err:=range results{
		if err==nil{success++}else if errors.Is(err,recoveryauth.ErrChallengeRejected){denied++}else{
			t.Fatalf("unexpected database/consumer error: %v",err)
		}
	}
	if success!=1||denied!=1{t.Fatalf("duplicate authorized attempt: success=%d denied=%d",success,denied)}
	assertRecoveryAttempt(t,f,issued.ID,1)
	assertRecoveryChallengeState(t,f,issued.ID,"consumed",1)
}
