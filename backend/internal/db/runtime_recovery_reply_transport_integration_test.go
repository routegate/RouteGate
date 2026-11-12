package db

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ikaevus/routegate/backend/internal/recoveryobserve"
)

func recoveryReplyBody(claim recoveryobserve.TokenBoundEvidenceV1,mac string,reply recoveryobserve.ObservationReply) map[string]any{
	fullMAC,_:=recoveryobserve.FullReplyMACV1(recoveryFixtureAgentToken,claim,reply)
	return map[string]any{"claim":claim,"mac":mac,"replyMac":fullMAC,"reply":reply}
}

func TestRuntimeRecoveryReplyTransportAuthenticatedMatchAuditAndReplay(t *testing.T) {
	f,id,claim,mac,reply:=authenticatedObservationFixture(t,30*time.Second)
	defer f.done()
	handler:=recoveryobserve.NewObservationTransport(f.pool)
	rec:=requestRecoveryTransport(t,handler.Reply,http.MethodPost,
		recoveryFixtureAgentToken,recoveryReplyBody(claim,mac,reply))
	if rec.Code!=http.StatusAccepted ||
		rec.Header().Get("Cache-Control")!="no-store, no-cache, max-age=0" ||
		rec.Header().Get("Vary")!="Authorization" ||
		!strings.Contains(rec.Body.String(),"reply_matched_unattested") {
		t.Fatalf("valid Agent signed reply not recorded: %d %q",rec.Code,rec.Body.String())
	}
	if strings.Contains(rec.Body.String(),claim.Nonce) ||
		strings.Contains(rec.Body.String(),recoveryFixtureAgentToken) ||
		strings.Contains(rec.Body.String(),claim.Evidence.MarkerSHA256) {
		t.Fatal("HTTP result leaked secret or local digest")
	}
	nonceAuditState(t,f,id,"reply_matched_unattested",2)
	replay:=requestRecoveryTransport(t,handler.Reply,http.MethodPost,
		recoveryFixtureAgentToken,recoveryReplyBody(claim,mac,reply))
	if replay.Code!=http.StatusConflict ||
		strings.Contains(replay.Body.String(),claim.Nonce) ||
		strings.Contains(replay.Body.String(),id) {
		t.Fatalf("replayed Agent response accepted or reflected: %d %q",replay.Code,replay.Body.String())
	}
	nonceAuditState(t,f,id,"reply_matched_unattested",2)
}

func TestRuntimeRecoveryReplyTransportWrongBearerOrMACDoesNotConsume(t *testing.T) {
	for _,mode:=range []string{"missing_token","foreign_token","wrong_mac","wrong_nonce","wrong_attempt","wrong_agent","wrong_witness_digest","wrong_reply_mac"} {
		t.Run(mode,func(t *testing.T){
			f,id,claim,mac,reply:=authenticatedObservationFixture(t,30*time.Second)
			defer f.done()
			token:=recoveryFixtureAgentToken
			switch mode {
			case "missing_token":token=""
			case "foreign_token":token="rg_agent_"+strings.Repeat("a",43)
			case "wrong_mac":mac=strings.Repeat("a",64)
			case "wrong_nonce":claim.Nonce="AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
			case "wrong_attempt":claim.AttemptID=claim.ReservationID
			case "wrong_agent":claim.Evidence.AgentID=claim.Evidence.ServerID
			case "wrong_witness_digest":claim.Evidence.WitnessSHA256=strings.Repeat("1",64)
			}
			// The legitimate bearer holder can recompute a HMAC for a
			// forged claim; immutable PostgreSQL binding must still win.
			if mode=="wrong_nonce"||mode=="wrong_attempt"||mode=="wrong_agent"||mode=="wrong_witness_digest" {
				var err error
				mac,err=recoveryobserve.TokenProofMACV1(recoveryFixtureAgentToken,claim)
				if err!=nil{t.Fatal(err)}
			}
			body:=recoveryReplyBody(claim,mac,reply)
			if mode=="wrong_reply_mac"{body["replyMac"]=strings.Repeat("0",64)}
			rec:=requestRecoveryTransport(t,recoveryobserve.NewObservationTransport(f.pool).Reply,
				http.MethodPost,token,body)
			if rec.Code==http.StatusAccepted ||
				strings.Contains(rec.Body.String(),claim.Nonce) ||
				strings.Contains(rec.Body.String(),recoveryFixtureAgentToken) {
				t.Fatalf("unauthenticated/falsified reply accepted or reflected: %d %q",rec.Code,rec.Body.String())
			}
			nonceAuditState(t,f,id,"issued",1)
		})
	}
}

func TestRuntimeRecoveryReplyTransportAuthenticatedBadObservationIsTerminal(t *testing.T){
	f,id,claim,mac,reply:=authenticatedObservationFixture(t,30*time.Second)
	defer f.done()
	reply.Observation.Witness.Generation.InvocationID=reply.Observation.Witness.PreviousGeneration.InvocationID
	handler:=recoveryobserve.NewObservationTransport(f.pool)
	rec:=requestRecoveryTransport(t,handler.Reply,http.MethodPost,recoveryFixtureAgentToken,
		recoveryReplyBody(claim,mac,reply))
	if rec.Code!=http.StatusConflict ||
		strings.Contains(rec.Body.String(),claim.Nonce) {
		t.Fatalf("invalid authenticated runtime reply was not refused: %d %q",rec.Code,rec.Body.String())
	}
	nonceAuditState(t,f,id,"reply_rejected",2)
	// A retry with a corrected observation MUST NOT convert failure into
	// matched_unattested after the nonce was consumed.
	reply.Observation.Witness.Generation.InvocationID=strings.Repeat("b",32)
	rec=requestRecoveryTransport(t,handler.Reply,http.MethodPost,recoveryFixtureAgentToken,
		recoveryReplyBody(claim,mac,reply))
	if rec.Code!=http.StatusConflict{t.Fatalf("terminal nonce replay accepted: %d",rec.Code)}
	nonceAuditState(t,f,id,"reply_rejected",2)
}

func TestRuntimeRecoveryReplyTransportParallelOneWinner(t *testing.T){
	f,id,claim,mac,reply:=authenticatedObservationFixture(t,30*time.Second)
	defer f.done()
	handler:=recoveryobserve.NewObservationTransport(f.pool)
	start:=make(chan struct{})
	out:=make(chan int,2)
	var wg sync.WaitGroup
	for i:=0;i<2;i++{
		wg.Add(1)
		go func(){
			defer wg.Done()
			<-start
			raw,err:=json.Marshal(recoveryReplyBody(claim,mac,reply))
			if err!=nil{out<-0;return}
			req:=httptest.NewRequest(http.MethodPost,"https://manager.invalid/api/v1/agent/recovery-observations/reply",bytes.NewReader(raw))
			req.Header.Set("Authorization","Bearer "+recoveryFixtureAgentToken)
			rec:=httptest.NewRecorder()
			handler.Reply(rec,req)
			out<-rec.Code
		}()
	}
	close(start);wg.Wait();close(out)
	accepted,refused:=0,0
	for status:=range out {
		if status==http.StatusAccepted {accepted++} else if status==http.StatusConflict{refused++} else{
			t.Fatalf("unexpected concurrent HTTP response: %d",status)
		}
	}
	if accepted!=1||refused!=1 {
		t.Fatalf("atomic reply HTTP admitted replay: accept=%d refuse=%d",accepted,refused)
	}
	nonceAuditState(t,f,id,"reply_matched_unattested",2)
}

func TestRuntimeRecoveryReplyTransportStrictJSONNeverConsumes(t *testing.T){
	f,id,claim,mac,reply:=authenticatedObservationFixture(t,30*time.Second)
	defer f.done()
	handler:=recoveryobserve.NewObservationTransport(f.pool)
	good,err:=json.Marshal(recoveryReplyBody(claim,mac,reply))
	if err!=nil{t.Fatal(err)}
	for name,body:=range map[string]string{
		"oversized":strings.Repeat("X",18000),
		"unknown_body_bearer":string(good[:len(good)-1])+`,"agentToken":"SECRET"}`,
		"two_documents":string(good)+string(good),
		"malformed":string(good[:len(good)-1]),
	}{
		t.Run(name,func(t *testing.T){
			req:=httptest.NewRequest(http.MethodPost,
				"https://manager.invalid/api/v1/agent/recovery-observations/reply",
				strings.NewReader(body))
			req.Header.Set("Authorization","Bearer "+recoveryFixtureAgentToken)
			rec:=httptest.NewRecorder()
			handler.Reply(rec,req)
			if rec.Code!=http.StatusBadRequest ||
				strings.Contains(rec.Body.String(),claim.Nonce) ||
				strings.Contains(rec.Body.String(),recoveryFixtureAgentToken) {
				t.Fatalf("invalid JSON accepted or reflected: %d %q",rec.Code,rec.Body.String())
			}
			nonceAuditState(t,f,id,"issued",1)
		})
	}
}

func TestRuntimeRecoveryReplyTransportAuditErrorRollsBack(t *testing.T){
	f,id,claim,mac,reply:=authenticatedObservationFixture(t,30*time.Second)
	defer f.done()
	_,err:=f.pool.Exec(f.ctx,`
CREATE FUNCTION rg140_fail_observation_reply_http_audit()
RETURNS trigger LANGUAGE plpgsql AS
'BEGIN IF NEW.action = ''reply_matched_unattested'' THEN
 RAISE EXCEPTION ''injected HTTP reply audit failure'';
 END IF; RETURN NEW; END';
`)
	if err!=nil{t.Fatal(err)}
	_,err=f.pool.Exec(f.ctx,`
CREATE TRIGGER rg140_fail_observation_reply_http_audit
BEFORE INSERT ON vpn_runtime_recovery_observation_nonce_events
FOR EACH ROW EXECUTE FUNCTION rg140_fail_observation_reply_http_audit();
`)
	if err!=nil{t.Fatal(err)}
	rec:=requestRecoveryTransport(t,recoveryobserve.NewObservationTransport(f.pool).Reply,
		http.MethodPost,recoveryFixtureAgentToken,recoveryReplyBody(claim,mac,reply))
	if rec.Code==http.StatusAccepted {t.Fatal("accepted HTTP result with failed audit")}
	nonceAuditState(t,f,id,"issued",1)
}
