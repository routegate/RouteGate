package db

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ikaevus/routegate/backend/internal/recoveryobserve"
)

func authenticatedObservationFixture(t *testing.T, expiry time.Duration) (
	recoveryLedgerFixture, string, recoveryobserve.TokenBoundEvidenceV1, string, recoveryobserve.ObservationReply,
) {
	t.Helper()
	f,id,nonce,_,reply:=observationFixture(t,expiry)
	witnessBytes,err:=json.Marshal(reply.Observation.Witness)
	if err!=nil{f.done();t.Fatal(err)}
	claim:=recoveryobserve.TokenBoundEvidenceV1{
		SchemaVersion:1,ObservationID:id,ReservationID:reply.ReservationID,
		AttemptID:reply.AttemptID,AttemptEpoch:reply.AttemptEpoch,
		Nonce:nonce,AgentCredentialGeneration:reply.AgentCredentialGeneration,
		Evidence:recoveryobserve.EvidenceDigestsV1{
			SchemaVersion:1,TaskID:reply.Observation.TaskID,
			AgentID:reply.Observation.AgentID,ServerID:reply.Observation.ServerID,
			MarkerSHA256:reply.Observation.MarkerHash,ReceiptSHA256:reply.Observation.ReceiptHash,
			EnvelopeSHA256:reply.Observation.EnvelopeHash,WitnessSHA256:nonceHash(witnessBytes),
		},
	}
	mac,err:=recoveryobserve.TokenProofMACV1(recoveryFixtureAgentToken,claim)
	if err!=nil{f.done();t.Fatal(err)}
	return f,id,claim,mac,reply
}

func TestRuntimeRecoveryAuthenticatedConsumeMatchesWithAuditAndNoUnlock(t *testing.T){
	f,id,claim,mac,reply:=authenticatedObservationFixture(t,30*time.Second)
	defer f.done()
	c:=recoveryobserve.NewAuthenticatedReplyConsumer(f.pool)
	result,err:=c.ConsumeTokenBoundReplyV1(f.ctx,recoveryFixtureAgentToken,mac,claim,reply)
	if err!=nil || result.ObservationID!=id || result.State!="reply_matched_unattested"{
		t.Fatalf("expected only matched_unattested: %+v %v",result,err)
	}
	nonceAuditState(t,f,id,"reply_matched_unattested",2)
	data,_:=json.Marshal(reply)
	sum:=sha256.Sum256(data)
	var persisted string
	if err:=f.pool.QueryRow(f.ctx,
		`SELECT response_sha256 FROM vpn_runtime_recovery_observation_nonces WHERE id=$1::uuid`,id).
		Scan(&persisted);err!=nil||persisted!=hex.EncodeToString(sum[:]){
		t.Fatal("reply digest was not committed with nonce audit",err)
	}
	if _,err:=c.ConsumeTokenBoundReplyV1(f.ctx,recoveryFixtureAgentToken,mac,claim,reply);
		!errors.Is(err,recoveryobserve.ErrTokenBoundEvidenceRefused) {
		t.Fatalf("same nonce replay accepted: %v",err)
	}
	nonceAuditState(t,f,id,"reply_matched_unattested",2)
}

func TestRuntimeRecoveryAuthenticatedConsumeRejectsBadReplyAsTerminal(t *testing.T){
	for _,mode:=range []string{"bad_generation","bad_envelope","bad_witness","bad_nonce","bad_observed_time","bad_agent"} {
		t.Run(mode,func(t *testing.T){
			f,id,claim,mac,reply:=authenticatedObservationFixture(t,30*time.Second)
			defer f.done()
			switch mode {
			case "bad_generation": reply.Observation.Witness.Generation.InvocationID=reply.Observation.Witness.PreviousGeneration.InvocationID
			case "bad_envelope": reply.Observation.EnvelopeHash=strings.Repeat("1",64)
			case "bad_witness": reply.Observation.Witness.ListenerPort=0
			case "bad_nonce": reply.Nonce=base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x99},32))
			case "bad_observed_time": reply.ObservedAt=reply.ObservedAt.Add(-4*time.Minute)
			case "bad_agent": reply.Observation.AgentID=reply.Observation.ServerID
			}
			c:=recoveryobserve.NewAuthenticatedReplyConsumer(f.pool)
			if _,err:=c.ConsumeTokenBoundReplyV1(f.ctx,recoveryFixtureAgentToken,mac,claim,reply);
				!errors.Is(err,recoveryobserve.ErrTokenBoundEvidenceRefused) {
				t.Fatalf("invalid reply accepted: %v",err)
			}
			// Authenticated bad runtime observation consumes the nonce as a
			// durable failure, preventing a corrected/replayed second try.
			nonceAuditState(t,f,id,"reply_rejected",2)
			if _,err:=c.ConsumeTokenBoundReplyV1(f.ctx,recoveryFixtureAgentToken,mac,claim,reply);
				!errors.Is(err,recoveryobserve.ErrTokenBoundEvidenceRefused){
				t.Fatal("terminal rejected reply was retried",err)
			}
		})
	}
}

func TestRuntimeRecoveryAuthenticatedConsumeInvalidBearerAndBindingNeverConsume(t *testing.T) {
	for _,mode:=range []string{
		"wrong_token","wrong_mac","wrong_nonce","wrong_epoch","wrong_reservation",
		"wrong_attempt","wrong_generation","wrong_agent","wrong_server",
		"wrong_task","wrong_marker","wrong_receipt","wrong_witness",
	}{
		t.Run(mode,func(t *testing.T){
			f,id,claim,_,reply:=authenticatedObservationFixture(t,30*time.Second)
			defer f.done()
			token:=recoveryFixtureAgentToken
			switch mode {
			case "wrong_token": token="rg_agent_"+base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x18},32))
			case "wrong_nonce":claim.Nonce=base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x19},32))
			case "wrong_epoch":claim.AttemptEpoch++
			case "wrong_reservation":claim.ReservationID=claim.AttemptID
			case "wrong_attempt":claim.AttemptID=claim.ReservationID
			case "wrong_generation":claim.AgentCredentialGeneration++
			case "wrong_agent":claim.Evidence.AgentID=claim.Evidence.ServerID
			case "wrong_server":claim.Evidence.ServerID=claim.Evidence.AgentID
			case "wrong_task":claim.Evidence.TaskID=claim.AttemptID
			case "wrong_marker":claim.Evidence.MarkerSHA256=strings.Repeat("1",64)
			case "wrong_receipt":claim.Evidence.ReceiptSHA256=strings.Repeat("1",64)
			case "wrong_witness":claim.Evidence.WitnessSHA256=strings.Repeat("1",64)
			}
			// An adversarial bearer holder gets to recompute a correct MAC.
			mac,err:=recoveryobserve.TokenProofMACV1(token,claim)
			if err!=nil{t.Fatal(err)}
			if mode=="wrong_mac"{mac=strings.Repeat("f",64)}
			if _,err=cConsume(f,token,mac,claim,reply);
				!errors.Is(err,recoveryobserve.ErrTokenBoundEvidenceRefused){
				t.Fatalf("invalid Agent binding consumed nonce: %v",err)
			}
			nonceAuditState(t,f,id,"issued",1)
		})
	}
}

func cConsume(f recoveryLedgerFixture,token,mac string,claim recoveryobserve.TokenBoundEvidenceV1,reply recoveryobserve.ObservationReply)(recoveryobserve.RecordedReply,error){
	return recoveryobserve.NewAuthenticatedReplyConsumer(f.pool).ConsumeTokenBoundReplyV1(f.ctx,token,mac,claim,reply)
}

func TestRuntimeRecoveryAuthenticatedConsumeConcurrentSingleWinner(t *testing.T){
	f,id,claim,mac,reply:=authenticatedObservationFixture(t,30*time.Second)
	defer f.done()
	ctx,cancel:=context.WithTimeout(f.ctx,12*time.Second)
	defer cancel()
	start:=make(chan struct{})
	out:=make(chan error,2)
	var wg sync.WaitGroup
	for i:=0;i<2;i++ {
		wg.Add(1)
		go func(){
			defer wg.Done()
			<-start
			_,err:=recoveryobserve.NewAuthenticatedReplyConsumer(f.pool).ConsumeTokenBoundReplyV1(
				ctx,recoveryFixtureAgentToken,mac,claim,reply)
			out<-err
		}()
	}
	close(start);wg.Wait();close(out)
	accepted,refused:=0,0
	for err:=range out{
		if err==nil {accepted++} else if errors.Is(err,recoveryobserve.ErrTokenBoundEvidenceRefused){refused++} else {
			t.Fatal("unexpected competing consumer result:",err)
		}
	}
	if accepted!=1||refused!=1{
		t.Fatalf("one-use nonce accepted twice: success=%d refusal=%d",accepted,refused)
	}
	nonceAuditState(t,f,id,"reply_matched_unattested",2)
}

func TestRuntimeRecoveryAuthenticatedConsumeAuditFailureRollsBackNonce(t *testing.T){
	f,id,claim,mac,reply:=authenticatedObservationFixture(t,30*time.Second)
	defer f.done()
	_,err:=f.pool.Exec(f.ctx,`
CREATE FUNCTION rg140_fail_atomic_observation_audit()
RETURNS trigger LANGUAGE plpgsql AS
'BEGIN IF NEW.action = ''reply_matched_unattested'' THEN
 RAISE EXCEPTION ''injected atomic observation audit failure'';
 END IF; RETURN NEW; END';
`)
	if err!=nil{t.Fatal(err)}
	_,err=f.pool.Exec(f.ctx,`
CREATE TRIGGER rg140_fail_atomic_observation_audit
BEFORE INSERT ON vpn_runtime_recovery_observation_nonce_events
FOR EACH ROW EXECUTE FUNCTION rg140_fail_atomic_observation_audit();
`)
	if err!=nil{t.Fatal(err)}
	if _,err:=cConsume(f,recoveryFixtureAgentToken,mac,claim,reply);err==nil {
		t.Fatal("observation was accepted without durable audit")
	}
	nonceAuditState(t,f,id,"issued",1)
}

func TestRuntimeRecoveryAuthenticatedConsumeExpiredNonceFailsClosed(t *testing.T){
	f,id,claim,mac,reply:=authenticatedObservationFixture(t,700*time.Millisecond)
	defer f.done()
	time.Sleep(time.Second)
	if _,err:=cConsume(f,recoveryFixtureAgentToken,mac,claim,reply);
		!errors.Is(err,recoveryobserve.ErrTokenBoundEvidenceRefused){
		t.Fatalf("expired nonce was accepted: %v",err)
	}
	nonceAuditState(t,f,id,"issued",1)
}
