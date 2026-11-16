package db

import (
	"strings"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ikaevus/routegate/backend/internal/recoveryenroll"
)

// RG-140: test-only checks on a disposable PostgreSQL schema. A real
// Agent has NOT made a network request; signatures come from synthetic
// fixture bearer secrets. No production role, HTTP, Agent Runner or
// recovery/mutation gate is connected to this protocol.
func issueGrantedReplyForRevocationV1(t *testing.T,
	f enrollmentOperatorGrantFixtureV1,
) (enrollmentOperatorGrantV1,enrollmentOneShotRehearsalIssueV1,
	recoveryenroll.EnrollmentProbeReplyV1,string) {
	t.Helper()
	g:=grantForFixtureV1(t,f)
	issued,err:=issueGrantedEnrollmentChallengeRehearsalV1(f.ctx,f.pool,g.ID,f.operator)
	if err!=nil{t.Fatal("initial one-shot challenge issuance:",err)}
	reply:=signedEnrollmentRehearsalReplyV1(
		t,enrollmentOneShotFixtureV1{f.recoveryReservationFixture},issued)
	mac:=signedEnrollmentRehearsalMACV1(t,recoveryFixtureAgentToken,reply)
	return g,issued,reply,mac
}

func requireRevokedGrantedReplyRefusedV1(t *testing.T,f enrollmentOperatorGrantFixtureV1,
	issued enrollmentOneShotRehearsalIssueV1,
	reply recoveryenroll.EnrollmentProbeReplyV1,mac string){
	t.Helper()
	receipt,err:=consumeSignedEnrollmentProbeRehearsalV1(
		f.ctx,f.pool,recoveryFixtureAgentToken,reply,mac)
	requireOneShotRefusedV1(t,err)
	if receipt!=(enrollmentOneShotRehearsalReplyV1{}) {
		t.Fatal("revoked grant produced a success receipt",receipt)
	}
	assertUnconsumedSignedEnrollmentV1(
		t,enrollmentOneShotFixtureV1{f.recoveryReservationFixture},issued.ID)
}

func TestRuntimeRecoveryEnrollmentReplyRevocationV1AfterIssueRefusesValidSignedReply(t *testing.T){
	for _,mode:=range []string{
		"session_revoked","session_expired","human_disabled",
		"role_removed","permission_removed",
	}{
		t.Run(mode,func(t *testing.T){
			f:=newEnrollmentOperatorGrantFixtureV1(t)
			defer f.done()
			_,issued,reply,mac:=issueGrantedReplyForRevocationV1(t,f)
			var err error
			switch mode{
			case "session_revoked":
				_,err=f.pool.Exec(f.ctx,`UPDATE auth_sessions SET revoked_at=clock_timestamp()
 WHERE id=$1::uuid`,f.operator.SessionID)
			case "session_expired":
				_,err=f.pool.Exec(f.ctx,`UPDATE auth_sessions
 SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1::uuid`,
					f.operator.SessionID)
			case "human_disabled":
				_,err=f.pool.Exec(f.ctx,`UPDATE users SET status='disabled' WHERE id=$1::uuid`,
					f.operator.ID)
			case "role_removed":
				_,err=f.pool.Exec(f.ctx,`DELETE FROM user_roles WHERE user_id=$1::uuid
 AND role_id=(SELECT id FROM roles WHERE code='super_admin')`,f.operator.ID)
			case "permission_removed":
				_,err=f.pool.Exec(f.ctx,`DELETE FROM role_permissions
 WHERE role_id=(SELECT id FROM roles WHERE code='super_admin')
 AND permission_id=(SELECT id FROM permissions WHERE code=$1)`,
					enrollmentChallengeIssuePermissionV1)
			}
			if err!=nil{t.Fatal("cannot revoke test operator permission:",err)}
			// Even though the answer is signed with the correct *Agent*
			// bearer, the earlier human authorization is no longer live.
			requireRevokedGrantedReplyRefusedV1(t,f,issued,reply,mac)
			g,e,a,au:=operatorGrantCountsV1(t,f)
			if g!=1||e!=1||a!=1||au!=2 {
				t.Fatalf("operator revocation rewrote immutable history: %d/%d/%d/%d",
					g,e,a,au)
			}
		})
	}
}

func TestRuntimeRecoveryEnrollmentReplyRevocationV1ExpiredGrantRefusesValidSignedReply(t *testing.T){
	f:=newEnrollmentOperatorGrantFixtureV1(t)
	defer f.done()
	// Issue while the one-off human approval remains valid, then let
	// *only* that approval expire. The Agent nonce still has 30 seconds.
	g,err:=grantEnrollmentChallengeWithTTLV1(
		f.ctx,f.pool,f.preparationID,explicitEnrollmentChallengeActionV1,
		f.operator,250*time.Millisecond)
	if err!=nil{t.Fatal(err)}
	issued,err:=issueGrantedEnrollmentChallengeRehearsalV1(f.ctx,f.pool,g.ID,f.operator)
	if err!=nil{t.Fatal(err)}
	reply:=signedEnrollmentRehearsalReplyV1(
		t,enrollmentOneShotFixtureV1{f.recoveryReservationFixture},issued)
	mac:=signedEnrollmentRehearsalMACV1(t,recoveryFixtureAgentToken,reply)
	time.Sleep(340*time.Millisecond)
	var grantExpired,attemptLive bool
	err=f.pool.QueryRow(f.ctx,`SELECT
 clock_timestamp()>=g.expires_at,clock_timestamp()<o.expires_at
 FROM rg140_test_enrollment_challenge_grants g
 JOIN rg140_test_enrollment_attempts o ON o.grant_id=g.id
 WHERE o.id=$1::uuid`,issued.ID).Scan(&grantExpired,&attemptLive)
	if err!=nil||!grantExpired||!attemptLive {
		t.Fatalf("expiry test preconditions not met: grant expired %t, nonce live %t, %v",
			grantExpired,attemptLive,err)
	}
	requireRevokedGrantedReplyRefusedV1(t,f,issued,reply,mac)
}

func TestRuntimeRecoveryEnrollmentReplyRevocationV1OlderConsumerAlsoRefused(t *testing.T){
	f:=newEnrollmentOperatorGrantFixtureV1(t)
	defer f.done()
	_,issued,reply,mac:=issueGrantedReplyForRevocationV1(t,f)
	_,err:=f.pool.Exec(f.ctx,`UPDATE auth_sessions
 SET revoked_at=clock_timestamp() WHERE id=$1::uuid`,f.operator.SessionID)
	if err!=nil{t.Fatal(err)}
	// This ADR-0037 consumer deliberately lacks human-operator checks in
	// its Go code. The PostgreSQL BEFORE UPDATE gate protects it too.
	result,err:=consumeEnrollmentOneShotRehearsalV1(
		f.ctx,f.pool,issued.ID,issued.Nonce,recoveryFixtureAgentToken,
		issued.ExpectedGenesisSHA256)
	requireOneShotRefusedV1(t,err)
	if result!=(enrollmentOneShotRehearsalReplyV1{}){
		t.Fatal("old unsigned test-only consumer bypassed revocation",result)
	}
	requireRevokedGrantedReplyRefusedV1(t,f,issued,reply,mac)
}

func TestRuntimeRecoveryEnrollmentReplyRevocationV1DirectSQLCannotBypassRevocation(t *testing.T){
	f:=newEnrollmentOperatorGrantFixtureV1(t)
	defer f.done()
	_,issued,reply,mac:=issueGrantedReplyForRevocationV1(t,f)
	_,err:=f.pool.Exec(f.ctx,`DELETE FROM role_permissions
 WHERE role_id=(SELECT id FROM roles WHERE code='super_admin')
 AND permission_id=(SELECT id FROM permissions WHERE code=$1)`,
		enrollmentChallengeIssuePermissionV1)
	if err!=nil{t.Fatal(err)}
	_,err=f.pool.Exec(f.ctx,`
UPDATE rg140_test_enrollment_attempts
 SET state='reply_matched_unattested',
     resolved_at=clock_timestamp(),response_sha256=$2
 WHERE id=$1::uuid AND state='issued_unattested'
`,issued.ID,strings.Repeat("a",64))
	if err==nil {t.Fatal("direct SQL resolution bypassed revoked operator permission")}
	requireRevokedGrantedReplyRefusedV1(t,f,issued,reply,mac)
}

func TestRuntimeRecoveryEnrollmentReplyRevocationV1CurrentGrantStillAllowsUnattestedReply(t *testing.T){
	f:=newEnrollmentOperatorGrantFixtureV1(t)
	defer f.done()
	_,issued,reply,mac:=issueGrantedReplyForRevocationV1(t,f)
	result,err:=consumeSignedEnrollmentProbeRehearsalV1(
		f.ctx,f.pool,recoveryFixtureAgentToken,reply,mac)
	if err!=nil||!result.Unattested||result.State!="reply_matched_unattested"{
		t.Fatalf("valid still-current grant incorrectly refused or elevated: %+v %v",result,err)
	}
	state,count:=observedOneShotStateV1(
		t,enrollmentOneShotFixtureV1{f.recoveryReservationFixture},issued.ID)
	if state!="reply_matched_unattested"||count!=2 {
		t.Fatalf("successful reply lacked atomic attempt event: %s/%d",state,count)
	}
	// The completed reply remains historical evidence, even if the
	// human permission is later revoked.
	_,err=f.pool.Exec(f.ctx,`UPDATE auth_sessions SET revoked_at=clock_timestamp()
 WHERE id=$1::uuid`,f.operator.SessionID)
	if err!=nil{t.Fatal(err)}
	_,err=consumeSignedEnrollmentProbeRehearsalV1(
		f.ctx,f.pool,recoveryFixtureAgentToken,reply,mac)
	requireOneShotRefusedV1(t,err)
}

func TestRuntimeRecoveryEnrollmentReplyRevocationV1FailedResolutionAuditLeavesUnattestedPending(t *testing.T){
	f:=newEnrollmentOperatorGrantFixtureV1(t)
	defer f.done()
	_,issued,reply,mac:=issueGrantedReplyForRevocationV1(t,f)
	blockOneShotAuditV1(t,enrollmentOneShotFixtureV1{f.recoveryReservationFixture},
		"reply_matched_unattested")
	_,err:=consumeSignedEnrollmentProbeRehearsalV1(
		f.ctx,f.pool,recoveryFixtureAgentToken,reply,mac)
	requireOneShotRefusedV1(t,err)
	assertUnconsumedSignedEnrollmentV1(
		t,enrollmentOneShotFixtureV1{f.recoveryReservationFixture},issued.ID)
}

func TestRuntimeRecoveryEnrollmentReplyRevocationV1ConcurrentCommittedRevocationWins(t *testing.T){
	f:=newEnrollmentOperatorGrantFixtureV1(t)
	defer f.done()
	_,issued,reply,mac:=issueGrantedReplyForRevocationV1(t,f)
	// Hold an uncommitted operator-session revocation while a different
	// transaction tries to resolve the already-issued challenge. The
	// database trigger's FOR SHARE must not accept stale session data
	// after the revoker commits.
	tx,err:=f.pool.Begin(f.ctx)
	if err!=nil{t.Fatal(err)}
	defer tx.Rollback(f.ctx)
	var lockedSession string
	if err:=tx.QueryRow(f.ctx,`SELECT id::text FROM auth_sessions
 WHERE id=$1::uuid FOR UPDATE`,f.operator.SessionID).Scan(&lockedSession);
		err!=nil||lockedSession!=f.operator.SessionID {
		t.Fatalf("could not lock synthetic human session: %q %v",lockedSession,err)
	}
	if _,err:=tx.Exec(f.ctx,`UPDATE auth_sessions
 SET revoked_at=clock_timestamp() WHERE id=$1::uuid`,f.operator.SessionID);
		err!=nil{t.Fatal(err)}
	ctx,cancel:=context.WithTimeout(f.ctx,10*time.Second)
	defer cancel()
	started:=make(chan struct{})
	done:=make(chan error,1)
	go func(){
		close(started)
		_,err:=consumeSignedEnrollmentProbeRehearsalV1(
			ctx,f.pool,recoveryFixtureAgentToken,reply,mac,
		)
		done<-err
	}()
	<-started
	// The consumer has a chance to reach its row-locking UPDATE. A
	// premature success is never permitted while revocation is pending.
	select {
	case err:=<-done:
		t.Fatalf("consumer bypassed held session revocation lock (early result): %v",err)
	case <-time.After(80*time.Millisecond):
	}
	if err:=tx.Commit(f.ctx);err!=nil{t.Fatal(err)}
	select{
	case err:=<-done:
		if !errors.Is(err,errEnrollmentOneShotRehearsalRefusedV1){
			t.Fatalf("consumer accepted after committed revocation: %v",err)
		}
	case <-ctx.Done():
		t.Fatal("signed consumer never resolved after session revocation committed")
	}
	assertUnconsumedSignedEnrollmentV1(
		t,enrollmentOneShotFixtureV1{f.recoveryReservationFixture},issued.ID)
}
