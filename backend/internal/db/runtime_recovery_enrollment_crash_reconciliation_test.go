package db

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ikaevus/routegate/backend/internal/recoveryenroll"
)

func expectManualEnrollmentHistoryV1(
	t *testing.T,f enrollmentOperatorGrantFixtureV1,want string,
) enrollmentManualReconciliationV1 {
	t.Helper()
	got,err:=inspectEnrollmentManualReconciliationOfflineV1(
		f.ctx,f.pool,f.preparationID,
	)
	if err!=nil || !got.Unattested || !got.ManualReviewOnly ||
		got.HistoricalState!=want || got.PreparationID!=f.preparationID ||
		got.AgentID!=f.agentID || got.ServerID!=f.serverID ||
		got.Generation!=f.generation {
		t.Fatalf("Manager historical reconciliation not fail-closed: %+v, err=%v, want=%s",
			got,err,want)
	}
	return got
}

func requireManualEnrollmentHistoryRefusedV1(
	t *testing.T,f enrollmentOperatorGrantFixtureV1,ctx context.Context,id string,
) {
	t.Helper()
	got,err:=inspectEnrollmentManualReconciliationOfflineV1(
		ctx,f.pool,id,
	)
	if !errors.Is(err,errEnrollmentManualReconciliationRefusedV1) ||
		got!=(enrollmentManualReconciliationV1{}) {
		t.Fatalf("untrusted Manager history produced a usable receipt: %+v %v",got,err)
	}
}

func TestRuntimeRecoveryEnrollmentCrashReconciliationV1NoGrantOrNonceCannotRecover(t *testing.T){
	f:=newEnrollmentOperatorGrantFixtureV1(t)
	defer f.done()
	// An earlier preparation is NOT the same as a committed human grant.
	requireManualEnrollmentHistoryRefusedV1(t,f,f.ctx,f.preparationID)
	grant:=grantForFixtureV1(t,f)
	got:=expectManualEnrollmentHistoryV1(t,f,enrollmentManualGrantOnlyStateV1)
	if got.GrantID!=grant.ID || got.AttemptID!=""{
		t.Fatalf("readback invented a nonce or challenge after grant only: %+v",got)
	}
	g,e,a,au:=operatorGrantCountsV1(t,f)
	if g!=1||e!=1||a!=0||au!=1 {
		t.Fatalf("read-only manual history changed committed data: %d/%d/%d/%d",g,e,a,au)
	}
}

func TestRuntimeRecoveryEnrollmentCrashReconciliationV1UncommittedIssueRollsBack(t *testing.T){
	f:=newEnrollmentOperatorGrantFixtureV1(t)
	defer f.done()
	grant:=grantForFixtureV1(t,f)
	tx,err:=f.pool.Begin(f.ctx)
	if err!=nil{t.Fatal(err)}
	defer tx.Rollback(f.ctx)
	var transientID string
	expected,err:=recoveryenroll.OfflineGenesisDigestUnattestedV1(
		recoveryenroll.Preparation{
			ID:f.preparationID,AgentID:f.agentID,ServerID:f.serverID,
			AgentCredentialGeneration:f.generation,
			Phase:recoveryenroll.PhasePreparedUnattested,
		},
	)
	if err!=nil{t.Fatal(err)}
	// This direct SQL INSERT models a Manager crash just before COMMIT.
	// Both attempt and its two DB-triggered audits exist inside tx only.
	err=tx.QueryRow(f.ctx,`
INSERT INTO rg140_test_enrollment_attempts(
 preparation_id,agent_id,server_id,credential_generation,
 expected_genesis_sha256,nonce_sha256,issued_at,expires_at,grant_id
) SELECT $1::uuid,$2::uuid,$3::uuid,$4,$5,$6,
 stamp.issued_at,stamp.issued_at+interval '15 seconds',$7::uuid
FROM (SELECT clock_timestamp() AS issued_at) stamp
RETURNING id::text
`,f.preparationID,f.agentID,f.serverID,f.generation,expected,
		strings.Repeat("8",64),grant.ID).Scan(&transientID)
	if err!=nil||transientID==""{
		t.Fatalf("could not create transient challenge before crash: %q %v",transientID,err)
	}
	var tentativeAudits int64
	err=tx.QueryRow(f.ctx,`
SELECT count(*) FROM audit_events
 WHERE action='runtime_recovery.enrollment_challenge_issued_unattested'
 AND metadata->>'attempt_id'=$1
`,transientID).Scan(&tentativeAudits)
	if err!=nil||tentativeAudits!=1{
		t.Fatalf("uncommitted challenge missing transaction-bound audit: %d %v",tentativeAudits,err)
	}
	// Connection abort / process crash: PostgreSQL rolls back the
	// uncommitted issue and both its trigger events.
	if err=tx.Rollback(f.ctx);err!=nil{t.Fatal(err)}
	got:=expectManualEnrollmentHistoryV1(t,f,enrollmentManualGrantOnlyStateV1)
	if got.GrantID!=grant.ID || got.AttemptID!=""{
		t.Fatalf("uncommitted attempt was recovered as durable: %+v",got)
	}
	g,e,a,au:=operatorGrantCountsV1(t,f)
	if g!=1||e!=1||a!=0||au!=1{
		t.Fatalf("failed issue left partial durable event: %d/%d/%d/%d",g,e,a,au)
	}
}

func TestRuntimeRecoveryEnrollmentCrashReconciliationV1LostIssueReceiptNeverReissues(t *testing.T){
	f:=newEnrollmentOperatorGrantFixtureV1(t)
	defer f.done()
	grant:=grantForFixtureV1(t,f)
	issued,err:=issueGrantedEnrollmentChallengeRehearsalV1(
		f.ctx,f.pool,grant.ID,f.operator)
	if err!=nil{t.Fatal(err)}
	// Manager finished COMMIT but lost the returned challenge before it
	// reached the caller. A fresh read from a different transaction sees
	// only the historical attempt ID/state, NOT recoverable plaintext nonce.
	history:=expectManualEnrollmentHistoryV1(t,f,"issued_unattested")
	if history.GrantID!=grant.ID||history.AttemptID!=issued.ID{
		t.Fatalf("lost issuance could not be reconciled: %+v",history)
	}
	_,err=issueGrantedEnrollmentChallengeRehearsalV1(
		f.ctx,f.pool,grant.ID,f.operator)
	requireOperatorGrantRefusedV1(t,err)
	again:=expectManualEnrollmentHistoryV1(t,f,"issued_unattested")
	if again!=history {
		t.Fatalf("readback is not deterministic for committed challenge: %+v %+v",history,again)
	}
	g,e,a,au:=operatorGrantCountsV1(t,f)
	if g!=1||e!=1||a!=1||au!=2 {
		t.Fatalf("readback silently reissued a new challenge: %d/%d/%d/%d",g,e,a,au)
	}
}

func TestRuntimeRecoveryEnrollmentCrashReconciliationV1LostReplyReceiptRemainsHistorical(t *testing.T){
	for _,mode:=range []string{"matched","rejected"}{
		t.Run(mode,func(t *testing.T){
			f:=newEnrollmentOperatorGrantFixtureV1(t)
			defer f.done()
			_,issued,reply,mac:=issueGrantedReplyForRevocationV1(t,f)
			want:="reply_matched_unattested"
			if mode=="rejected"{
				reply.LocalGenesisSHA256=strings.Repeat("a",64)
				mac=signedEnrollmentRehearsalMACV1(t,recoveryFixtureAgentToken,reply)
				want="reply_rejected"
			}
			_,err:=consumeSignedEnrollmentProbeRehearsalV1(
				f.ctx,f.pool,recoveryFixtureAgentToken,reply,mac)
			if err!=nil{t.Fatal(err)}
			// No returned receipt is persisted in the restarted Manager's
			// process memory; only the DB audit and response commitment remain.
			got:=expectManualEnrollmentHistoryV1(t,f,want)
			if got.AttemptID!=issued.ID||!got.ManualReviewOnly{
				t.Fatalf("ambiguous resolved reply was promoted: %+v",got)
			}
			_,err=consumeSignedEnrollmentProbeRehearsalV1(
				f.ctx,f.pool,recoveryFixtureAgentToken,reply,mac)
			requireOneShotRefusedV1(t,err)
			if again:=expectManualEnrollmentHistoryV1(t,f,want);again!=got{
				t.Fatalf("replay or readback changed historical response: %+v to %+v",got,again)
			}
		})
	}
}

func TestRuntimeRecoveryEnrollmentCrashReconciliationV1FailedResponseAuditStillIssued(t *testing.T){
	f:=newEnrollmentOperatorGrantFixtureV1(t)
	defer f.done()
	_,issued,reply,mac:=issueGrantedReplyForRevocationV1(t,f)
	blockOneShotAuditV1(t,enrollmentOneShotFixtureV1{f.recoveryReservationFixture},
		"reply_matched_unattested")
	_,err:=consumeSignedEnrollmentProbeRehearsalV1(
		f.ctx,f.pool,recoveryFixtureAgentToken,reply,mac)
	requireOneShotRefusedV1(t,err)
	got:=expectManualEnrollmentHistoryV1(t,f,"issued_unattested")
	if got.AttemptID!=issued.ID{
		t.Fatalf("failed resolution audit lost original attempt: %+v",got)
	}
}

func TestRuntimeRecoveryEnrollmentCrashReconciliationV1RevokedGrantReadbackOnly(t *testing.T){
	f:=newEnrollmentOperatorGrantFixtureV1(t)
	defer f.done()
	_,issued,reply,mac:=issueGrantedReplyForRevocationV1(t,f)
	_,err:=f.pool.Exec(f.ctx,`UPDATE auth_sessions SET revoked_at=clock_timestamp()
 WHERE id=$1::uuid`,f.operator.SessionID)
	if err!=nil{t.Fatal(err)}
	got:=expectManualEnrollmentHistoryV1(t,f,"issued_unattested")
	if got.AttemptID!=issued.ID{t.Fatalf("historical revoked grant lost: %+v",got)}
	_,err=consumeSignedEnrollmentProbeRehearsalV1(
		f.ctx,f.pool,recoveryFixtureAgentToken,reply,mac)
	requireOneShotRefusedV1(t,err)
	after:=expectManualEnrollmentHistoryV1(t,f,"issued_unattested")
	if after!=got{t.Fatalf("revocation readback changed state: %+v %+v",got,after)}
}

func TestRuntimeRecoveryEnrollmentCrashReconciliationV1IncompleteOrForgedHistoryRefused(t *testing.T){
	f:=newEnrollmentOperatorGrantFixtureV1(t)
	defer f.done()
	_,issued,_,_:=issueGrantedReplyForRevocationV1(t,f)
	// The test-only historical event table is intentionally not immutable
	// against a DB superuser. A corrupted extra event must fail closed.
	_,err:=f.pool.Exec(f.ctx,`
INSERT INTO rg140_test_enrollment_attempt_events(attempt_id,state)
VALUES($1::uuid,'issued_unattested')
`,issued.ID)
	if err!=nil{t.Fatal(err)}
	requireManualEnrollmentHistoryRefusedV1(t,f,f.ctx,f.preparationID)
}

func TestRuntimeRecoveryEnrollmentCrashReconciliationV1InvalidInputRefused(t *testing.T){
	f:=newEnrollmentOperatorGrantFixtureV1(t)
	defer f.done()
	grantForFixtureV1(t,f)
	requireManualEnrollmentHistoryRefusedV1(t,f,nil,f.preparationID)
	requireManualEnrollmentHistoryRefusedV1(t,f,f.ctx,"invalid-preparation")
	ctx,cancel:=context.WithCancel(f.ctx)
	cancel()
	requireManualEnrollmentHistoryRefusedV1(t,f,ctx,f.preparationID)
}

func TestRuntimeRecoveryEnrollmentCrashReconciliationV1UncommittedReplyRollsBack(t *testing.T){
	f:=newEnrollmentOperatorGrantFixtureV1(t)
	defer f.done()
	_,issued,_,_:=issueGrantedReplyForRevocationV1(t,f)
	tx,err:=f.pool.Begin(f.ctx)
	if err!=nil{t.Fatal(err)}
	defer tx.Rollback(f.ctx)
	// A crash after writing tentative reply state but BEFORE COMMIT.
	// Direct SQL models only PostgreSQL's transaction boundary; this
	// does not exercise Agent authentication (covered separately in ADR-0039).
	tag,err:=tx.Exec(f.ctx,`
UPDATE rg140_test_enrollment_attempts
 SET state='reply_matched_unattested',resolved_at=clock_timestamp(),
     response_sha256=$2
 WHERE id=$1::uuid AND state='issued_unattested'
`,issued.ID,strings.Repeat("4",64))
	if err!=nil||tag.RowsAffected()!=1{
		t.Fatalf("could not stage uncommitted reply: %v, affected=%d",err,tag.RowsAffected())
	}
	var tentativeEvents int64
	err=tx.QueryRow(f.ctx,`SELECT count(*)
 FROM rg140_test_enrollment_attempt_events WHERE attempt_id=$1::uuid
`,issued.ID).Scan(&tentativeEvents)
	if err!=nil||tentativeEvents!=2{
		t.Fatalf("uncommitted reply not audit-atomic: events=%d %v",tentativeEvents,err)
	}
	if err=tx.Rollback(f.ctx);err!=nil{t.Fatal(err)}
	got:=expectManualEnrollmentHistoryV1(t,f,"issued_unattested")
	if got.AttemptID!=issued.ID{
		t.Fatalf("aborted reply promoted to a terminal state: %+v",got)
	}
	state,events:=observedOneShotStateV1(
		t,enrollmentOneShotFixtureV1{f.recoveryReservationFixture},issued.ID)
	if state!="issued_unattested"||events!=1{
		t.Fatalf("failed reply transaction left a terminal audit: %s/%d",state,events)
	}
}
