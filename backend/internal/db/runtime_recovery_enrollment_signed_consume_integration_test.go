package db

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ikaevus/routegate/backend/internal/recoveryenroll"
)

// The signed payload is constructed from the synthetic disposable fixture.
// Its local genesis claim is NOT read from Agent disk. These tests exercise
// transactional MAC/replay behavior, not trusted local host provenance.
func signedEnrollmentRehearsalReplyV1(
	t *testing.T,f enrollmentOneShotFixtureV1,
	issued enrollmentOneShotRehearsalIssueV1,
) recoveryenroll.EnrollmentProbeReplyV1 {
	t.Helper()
	reply:=recoveryenroll.EnrollmentProbeReplyV1{
		SchemaVersion:1,AttemptID:issued.ID,Nonce:issued.Nonce,
		LocalGenesisSHA256:issued.ExpectedGenesisSHA256,
	}
	err:=f.pool.QueryRow(f.ctx,`
SELECT preparation_id::text,agent_id::text,server_id::text,credential_generation
  FROM rg140_test_enrollment_attempts WHERE id=$1::uuid
`,issued.ID).Scan(
		&reply.ManagerPreparationID,&reply.AgentID,&reply.ServerID,
		&reply.AgentCredentialGeneration,
	)
	if err!=nil{t.Fatal(err)}
	return reply
}

func signedEnrollmentRehearsalMACV1(t *testing.T,
	token string,reply recoveryenroll.EnrollmentProbeReplyV1,
) string {
	t.Helper()
	mac,err:=recoveryenroll.EnrollmentProbeReplyMACOfflineV1(token,reply)
	if err!=nil {t.Fatal("invalid test-only probe signing: ",err)}
	return mac
}

func assertUnconsumedSignedEnrollmentV1(t *testing.T,
	f enrollmentOneShotFixtureV1,id string,
) {
	t.Helper()
	state,events:=observedOneShotStateV1(t,f,id)
	if state!="issued_unattested"||events!=1 {
		t.Fatalf("refused signed probe consumed nonce or audit: %s/%d",state,events)
	}
}

func TestRuntimeRecoveryEnrollmentSignedConsumeV1MatchedRemainsUnattestedAndSingleUse(t *testing.T) {
	f:=newEnrollmentOneShotFixtureV1(t)
	defer f.done()
	issued,err:=issueEnrollmentOneShotRehearsalV1(f.ctx,f.pool,f.agentID)
	if err!=nil {t.Fatal(err)}
	reply:=signedEnrollmentRehearsalReplyV1(t,f,issued)
	mac:=signedEnrollmentRehearsalMACV1(t,recoveryFixtureAgentToken,reply)
	got,err:=consumeSignedEnrollmentProbeRehearsalV1(
		f.ctx,f.pool,recoveryFixtureAgentToken,reply,mac,
	)
	if err!=nil||!got.Unattested||got.State!="reply_matched_unattested"{
		t.Fatalf("signed historical reply not matched or elevated: %+v %v",got,err)
	}
	state,events:=observedOneShotStateV1(t,f,issued.ID)
	if state!="reply_matched_unattested"||events!=2 {
		t.Fatalf("signed challenge/audit not committed as one-use: %s/%d",state,events)
	}
	var digest string
	if err:=f.pool.QueryRow(f.ctx,`SELECT response_sha256
FROM rg140_test_enrollment_attempts WHERE id=$1::uuid`,issued.ID).Scan(&digest);
		err!=nil||len(digest)!=64 || strings.Contains(digest,mac) {
		t.Fatalf("response commitment missing or bad: %q %v",digest,err)
	}
	_,err=consumeSignedEnrollmentProbeRehearsalV1(
		f.ctx,f.pool,recoveryFixtureAgentToken,reply,mac,
	)
	requireOneShotRefusedV1(t,err)
	_,err=issueEnrollmentOneShotRehearsalV1(f.ctx,f.pool,f.agentID)
	requireOneShotRefusedV1(t,err)
	state,events=observedOneShotStateV1(t,f,issued.ID)
	if state!="reply_matched_unattested"||events!=2 {
		t.Fatalf("replay changed Manager history: %s/%d",state,events)
	}
}

func TestRuntimeRecoveryEnrollmentSignedConsumeV1RejectsAlteredUnsignedFields(t *testing.T) {
	for name,alter:=range map[string]func(*recoveryenroll.EnrollmentProbeReplyV1){
		"attempt":func(r *recoveryenroll.EnrollmentProbeReplyV1){r.AttemptID=r.AgentID},
		"preparation":func(r *recoveryenroll.EnrollmentProbeReplyV1){r.ManagerPreparationID=r.AgentID},
		"agent":func(r *recoveryenroll.EnrollmentProbeReplyV1){r.AgentID=r.ServerID},
		"server":func(r *recoveryenroll.EnrollmentProbeReplyV1){r.ServerID=r.AgentID},
		"generation":func(r *recoveryenroll.EnrollmentProbeReplyV1){r.AgentCredentialGeneration++},
		"nonce":func(r *recoveryenroll.EnrollmentProbeReplyV1){
			r.Nonce=base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x59},32))
		},
		"genesis":func(r *recoveryenroll.EnrollmentProbeReplyV1){r.LocalGenesisSHA256=strings.Repeat("a",64)},
		"bad_shape":func(r *recoveryenroll.EnrollmentProbeReplyV1){r.SchemaVersion++},
	}{
		t.Run(name,func(t *testing.T){
			f:=newEnrollmentOneShotFixtureV1(t);defer f.done()
			issued,err:=issueEnrollmentOneShotRehearsalV1(f.ctx,f.pool,f.agentID)
			if err!=nil{t.Fatal(err)}
			reply:=signedEnrollmentRehearsalReplyV1(t,f,issued)
			mac:=signedEnrollmentRehearsalMACV1(t,recoveryFixtureAgentToken,reply)
			alter(&reply)
			_,err=consumeSignedEnrollmentProbeRehearsalV1(
				f.ctx,f.pool,recoveryFixtureAgentToken,reply,mac,
			)
			requireOneShotRefusedV1(t,err)
			assertUnconsumedSignedEnrollmentV1(t,f,issued.ID)
		})
	}
}

func TestRuntimeRecoveryEnrollmentSignedConsumeV1WrongCredentialNonceAndMACRefused(t *testing.T) {
	for _,mode:=range []string{
		"incorrect_token","registration_token","bad_mac","different_signed_nonce",
	} {
		t.Run(mode,func(t *testing.T){
			f:=newEnrollmentOneShotFixtureV1(t);defer f.done()
			issued,err:=issueEnrollmentOneShotRehearsalV1(f.ctx,f.pool,f.agentID)
			if err!=nil{t.Fatal(err)}
			reply:=signedEnrollmentRehearsalReplyV1(t,f,issued)
			token:=recoveryFixtureAgentToken
			mac:=signedEnrollmentRehearsalMACV1(t,token,reply)
			switch mode{
			case "incorrect_token":token="wrong-bearer"
			case "registration_token":token="rg_reg_"+strings.TrimPrefix(token,"rg_agent_")
			case "bad_mac":mac=strings.Repeat("0",64)
			case "different_signed_nonce":
				reply.Nonce=base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x66},32))
				mac=signedEnrollmentRehearsalMACV1(t,token,reply)
			}
			_,err=consumeSignedEnrollmentProbeRehearsalV1(
				f.ctx,f.pool,token,reply,mac,
			)
			requireOneShotRefusedV1(t,err)
			assertUnconsumedSignedEnrollmentV1(t,f,issued.ID)
		})
	}
}

func TestRuntimeRecoveryEnrollmentSignedConsumeV1AuthenticatedWrongDigestIsPermanentRejection(t *testing.T) {
	f:=newEnrollmentOneShotFixtureV1(t);defer f.done()
	issued,err:=issueEnrollmentOneShotRehearsalV1(f.ctx,f.pool,f.agentID)
	if err!=nil{t.Fatal(err)}
	reply:=signedEnrollmentRehearsalReplyV1(t,f,issued)
	reply.LocalGenesisSHA256=strings.Repeat("a",64)
	mac:=signedEnrollmentRehearsalMACV1(t,recoveryFixtureAgentToken,reply)
	got,err:=consumeSignedEnrollmentProbeRehearsalV1(
		f.ctx,f.pool,recoveryFixtureAgentToken,reply,mac,
	)
	if err!=nil||got.State!="reply_rejected"||!got.Unattested{
		t.Fatalf("authenticated but wrong digest not rejected: %+v %v",got,err)
	}
	state,audit:=observedOneShotStateV1(t,f,issued.ID)
	if state!="reply_rejected"||audit!=2 {t.Fatalf("wrong digest not consumed: %s/%d",state,audit)}
	reply.LocalGenesisSHA256=issued.ExpectedGenesisSHA256
	mac=signedEnrollmentRehearsalMACV1(t,recoveryFixtureAgentToken,reply)
	_,err=consumeSignedEnrollmentProbeRehearsalV1(
		f.ctx,f.pool,recoveryFixtureAgentToken,reply,mac,
	)
	requireOneShotRefusedV1(t,err)
}

func TestRuntimeRecoveryEnrollmentSignedConsumeV1ConcurrentOnlyOneCommit(t *testing.T) {
	f:=newEnrollmentOneShotFixtureV1(t);defer f.done()
	issued,err:=issueEnrollmentOneShotRehearsalV1(f.ctx,f.pool,f.agentID)
	if err!=nil{t.Fatal(err)}
	reply:=signedEnrollmentRehearsalReplyV1(t,f,issued)
	mac:=signedEnrollmentRehearsalMACV1(t,recoveryFixtureAgentToken,reply)
	ctx,cancel:=context.WithTimeout(f.ctx,12*time.Second)
	defer cancel()
	start:=make(chan struct{})
	out:=make(chan error,2)
	var wg sync.WaitGroup
	for i:=0;i<2;i++{
		wg.Add(1)
		go func(){
			defer wg.Done();<-start
			_,err:=consumeSignedEnrollmentProbeRehearsalV1(
				ctx,f.pool,recoveryFixtureAgentToken,reply,mac,
			)
			out<-err
		}()
	}
	close(start);wg.Wait();close(out)
	ok,refused:=0,0
	for err:=range out{
		if err==nil{ok++}else if errors.Is(err,errEnrollmentOneShotRehearsalRefusedV1){
			refused++
		}else{t.Fatalf("unexpected signed competitor result: %v",err)}
	}
	if ok!=1||refused!=1{
		t.Fatalf("two concurrent signed replies committed: success=%d refused=%d",ok,refused)
	}
	state,audit:=observedOneShotStateV1(t,f,issued.ID)
	if state!="reply_matched_unattested"||audit!=2 {
		t.Fatalf("concurrent challenge audit inconsistent: %s/%d",state,audit)
	}
}

func TestRuntimeRecoveryEnrollmentSignedConsumeV1ExpiredAndRotatedRefused(t *testing.T){
	for _,mode:=range []string{"expired","rotated_generation","new_job","recovery_hold"}{
		t.Run(mode,func(t *testing.T){
			f:=newEnrollmentOneShotFixtureV1(t);defer f.done()
			var issued enrollmentOneShotRehearsalIssueV1
			var err error
			if mode=="expired"{
				issued,err=issueEnrollmentOneShotRehearsalWithTTLV1(
					f.ctx,f.pool,f.agentID,5*time.Millisecond,
				)
			}else{
				issued,err=issueEnrollmentOneShotRehearsalV1(f.ctx,f.pool,f.agentID)
			}
			if err!=nil{t.Fatal(err)}
			reply:=signedEnrollmentRehearsalReplyV1(t,f,issued)
			mac:=signedEnrollmentRehearsalMACV1(t,recoveryFixtureAgentToken,reply)
			switch mode{
			case "expired":time.Sleep(40*time.Millisecond)
			case "rotated_generation":
				_,err=f.pool.Exec(f.ctx,`UPDATE agents SET credential_generation=credential_generation+1,
last_authenticated_heartbeat_at=NULL,last_authenticated_heartbeat_generation=NULL WHERE id=$1::uuid`,f.agentID)
			case "new_job":
				_,err=f.pool.Exec(f.ctx,`INSERT INTO config_apply_jobs(
server_id,agent_id,config_version_id,action,status)
VALUES($1::uuid,$2::uuid,$3::uuid,'apply','pending')`,f.serverID,f.agentID,f.configVersionID)
			case "recovery_hold":
				err=f.reserve(f.ctx,f.generation,"config_apply")
			}
			if err!=nil{t.Fatal(err)}
			_,err=consumeSignedEnrollmentProbeRehearsalV1(
				f.ctx,f.pool,recoveryFixtureAgentToken,reply,mac,
			)
			requireOneShotRefusedV1(t,err)
			assertUnconsumedSignedEnrollmentV1(t,f,issued.ID)
		})
	}
}

func TestRuntimeRecoveryEnrollmentSignedConsumeV1AuditFailureRollsBack(t *testing.T){
	f:=newEnrollmentOneShotFixtureV1(t);defer f.done()
	issued,err:=issueEnrollmentOneShotRehearsalV1(f.ctx,f.pool,f.agentID)
	if err!=nil{t.Fatal(err)}
	reply:=signedEnrollmentRehearsalReplyV1(t,f,issued)
	mac:=signedEnrollmentRehearsalMACV1(t,recoveryFixtureAgentToken,reply)
	blockOneShotAuditV1(t,f,"reply_matched_unattested")
	got,err:=consumeSignedEnrollmentProbeRehearsalV1(
		f.ctx,f.pool,recoveryFixtureAgentToken,reply,mac,
	)
	requireOneShotRefusedV1(t,err)
	if got!=(enrollmentOneShotRehearsalReplyV1{}){
		t.Fatal("failed Manager audit leaked success receipt",got)
	}
	assertUnconsumedSignedEnrollmentV1(t,f,issued.ID)
}
