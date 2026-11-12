package client

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ikaevus/routegate/agent/internal/tasks"
)

const syntheticRecoveryAgentBearer = "rg_agent_dnZ2dnZ2dnZ2dnZ2dnZ2dnZ2dnZ2dnZ2dnZ2dnZ2dnY"

func testRecoveryPendingAttempt() RecoveryPendingAttempt {
	return RecoveryPendingAttempt{
		SchemaVersion:1,
		AttemptID:"11111111-1111-4111-8111-111111111111",
		ReservationID:"22222222-2222-4222-8222-222222222222",
		AttemptEpoch:17,
		TaskID:"33333333-3333-4333-8333-333333333333",
		AgentID:"44444444-4444-4444-8444-444444444444",
		ServerID:"55555555-5555-4555-8555-555555555555",
		AgentCredentialGeneration:3,
	}
}

func testRecoveryEvidence(p RecoveryPendingAttempt) tasks.RuntimeRecoveryEvidenceDigestsV1 {
	return tasks.RuntimeRecoveryEvidenceDigestsV1{
		SchemaVersion:1,TaskID:p.TaskID,AgentID:p.AgentID,ServerID:p.ServerID,
		MarkerSHA256:strings.Repeat("a",64),
		ReceiptSHA256:strings.Repeat("b",64),
		EnvelopeSHA256:strings.Repeat("c",64),
		WitnessSHA256:strings.Repeat("d",64),
	}
}

func TestRecoveryObservationTransportAuthenticatedPollingAndNonceDelivery(t *testing.T) {
	pending:=testRecoveryPendingAttempt()
	evidence:=testRecoveryEvidence(pending)
	var polls,issues atomic.Int32
	var server *httptest.Server
	server=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
		if r.Header.Get("Authorization")!="Bearer "+syntheticRecoveryAgentBearer {
			t.Error("client did not use existing Agent bearer header")
			w.WriteHeader(http.StatusUnauthorized);return
		}
		w.Header().Set("Content-Type","application/json")
		switch r.URL.Path {
		case "/api/v1/agent/recovery-observations/pending":
			if r.Method!=http.MethodGet {t.Error("unexpected poll method")}
			polls.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"pending":pending})
		case "/api/v1/agent/recovery-observations/issue":
			if r.Method!=http.MethodPost {t.Error("unexpected issuer method")}
			issues.Add(1)
			var req struct{
				AttemptID string `json:"attemptId"`
				Evidence tasks.RuntimeRecoveryEvidenceDigestsV1 `json:"evidence"`
			}
			if err:=json.NewDecoder(r.Body).Decode(&req);err!=nil{
				t.Error(err)
			}
			if req.AttemptID!=pending.AttemptID || req.Evidence!=evidence {
				t.Error("Agent did not send exact read-only evidence binding")
			}
			now:=time.Now().UTC().Add(-time.Second)
			_ = json.NewEncoder(w).Encode(recoveryIssuedResponse{
				SchemaVersion:1,
				ObservationID:"66666666-6666-4666-8666-666666666666",
				ReservationID:pending.ReservationID,
				AttemptID:pending.AttemptID,AttemptEpoch:pending.AttemptEpoch,
				Nonce:base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x78},32)),
				IssuedAt:now,ExpiresAt:now.Add(45*time.Second),
				TaskID:pending.TaskID,AgentID:pending.AgentID,ServerID:pending.ServerID,
				AgentCredentialGeneration:pending.AgentCredentialGeneration,
				ExpectedEvidence:evidence,
			})
		default:
			t.Errorf("unexpected recovery transport URL %s",r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	c:=New(server.URL)
	got,err:=c.PollRecoveryPendingAttempt(context.Background(),syntheticRecoveryAgentBearer)
	if err!=nil||got==nil||*got!=pending{t.Fatalf("unsafe pending attempt: %+v %v",got,err)}
	challenge,err:=c.RequestRecoveryObservationNonce(
		context.Background(),syntheticRecoveryAgentBearer,*got,evidence)
	if err!=nil {t.Fatal(err)}
	if challenge.SchemaVersion!=1||
		challenge.AttemptID!=pending.AttemptID||
		challenge.ReservationID!=pending.ReservationID||
		challenge.Binding.ManagerURL!=server.URL||
		challenge.Binding.AgentID!=pending.AgentID||
		challenge.Binding.ServerID!=pending.ServerID||
		challenge.AgentCredentialGeneration!=pending.AgentCredentialGeneration||
		challenge.MarkerHash!=evidence.MarkerSHA256||
		challenge.ReceiptHash!=evidence.ReceiptSHA256||
		challenge.RuntimeWitnessHash!=evidence.WitnessSHA256||
		challenge.ExpiresAt.Sub(challenge.IssuedAt)!=45*time.Second {
		t.Fatalf("client accepted an unbound nonce: %+v",challenge)
	}
	if polls.Load()!=1||issues.Load()!=1 {
		t.Fatalf("unexpected automatic polling/retries: poll=%d issue=%d",polls.Load(),issues.Load())
	}
}

func TestRecoveryObservationTransportLostResponseNeverAutomaticallyReissues(t *testing.T) {
	pending:=testRecoveryPendingAttempt()
	var calls atomic.Int32
	server:=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
		calls.Add(1)
		// Simulate a committed issuance followed by lost/truncated response.
		w.Header().Set("Content-Type","application/json")
		_,_ = w.Write([]byte(`{"schemaVersion":1,"nonce":`))
	}))
	defer server.Close()
	c:=New(server.URL)
	issued,err:=c.RequestRecoveryObservationNonce(
		context.Background(),syntheticRecoveryAgentBearer,pending,testRecoveryEvidence(pending))
	if err==nil || issued.Nonce!="" {
		t.Fatal("lost issuer HTTP reply was mistaken for a valid, replayable nonce")
	}
	if calls.Load()!=1 {
		t.Fatalf("ambiguous COMMIT caused an automatic reissue: %d requests",calls.Load())
	}
}

func TestRecoveryObservationTransportRefusesCrossBoundOrMalformedResponse(t *testing.T) {
	pending:=testRecoveryPendingAttempt()
	evidence:=testRecoveryEvidence(pending)
	for name,edit:=range map[string]func(*recoveryIssuedResponse){
		"wrong_attempt":func(x *recoveryIssuedResponse){x.AttemptID=x.ReservationID},
		"wrong_reservation":func(x *recoveryIssuedResponse){x.ReservationID=x.AttemptID},
		"wrong_agent":func(x *recoveryIssuedResponse){x.AgentID=x.ServerID},
		"wrong_server":func(x *recoveryIssuedResponse){x.ServerID=x.AgentID},
		"wrong_task":func(x *recoveryIssuedResponse){x.TaskID=x.AttemptID},
		"wrong_epoch":func(x *recoveryIssuedResponse){x.AttemptEpoch++},
		"wrong_generation":func(x *recoveryIssuedResponse){x.AgentCredentialGeneration++},
		"wrong_marker":func(x *recoveryIssuedResponse){x.ExpectedEvidence.MarkerSHA256=strings.Repeat("e",64)},
		"wrong_witness":func(x *recoveryIssuedResponse){x.ExpectedEvidence.WitnessSHA256=strings.Repeat("e",64)},
		"invalid_nonce":func(x *recoveryIssuedResponse){x.Nonce="short"},
		"wrong_nonce_encoding":func(x *recoveryIssuedResponse){x.Nonce+="="},
		"expired":func(x *recoveryIssuedResponse){x.ExpiresAt=time.Now().Add(-time.Second)},
		"excessive_ttl":func(x *recoveryIssuedResponse){x.ExpiresAt=x.IssuedAt.Add(5*time.Minute)},
		"future_issue":func(x *recoveryIssuedResponse){x.IssuedAt=time.Now().Add(5*time.Minute);x.ExpiresAt=x.IssuedAt.Add(45*time.Second)},
		"bad_observation_id":func(x *recoveryIssuedResponse){x.ObservationID="not-a-uuid"},
	}{
		t.Run(name,func(t *testing.T) {
			now:=time.Now().UTC().Add(-time.Second)
			issued:=recoveryIssuedResponse{
				SchemaVersion:1,
				ObservationID:"66666666-6666-4666-8666-666666666666",
				ReservationID:pending.ReservationID,AttemptID:pending.AttemptID,
				AttemptEpoch:pending.AttemptEpoch,
				Nonce:base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x78},32)),
				IssuedAt:now,ExpiresAt:now.Add(45*time.Second),
				TaskID:pending.TaskID,AgentID:pending.AgentID,
				ServerID:pending.ServerID,
				AgentCredentialGeneration:pending.AgentCredentialGeneration,
				ExpectedEvidence:evidence,
			}
			edit(&issued)
			var calls atomic.Int32
			server:=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
				calls.Add(1)
				w.Header().Set("Content-Type","application/json")
				_ = json.NewEncoder(w).Encode(issued)
			}))
			defer server.Close()
			c:=New(server.URL)
			challenge,err:=c.RequestRecoveryObservationNonce(
				context.Background(),syntheticRecoveryAgentBearer,pending,evidence)
			if err==nil||challenge.Nonce!=""{
				t.Fatalf("cross-bound challenge accepted: %+v err=%v",challenge,err)
			}
			if calls.Load()!=1{t.Fatal("invalid nonce response was automatically reissued")}
		})
	}
}

func TestRecoveryObservationTransportRefusesMalformedPendingMetadata(t *testing.T){
	pending:=testRecoveryPendingAttempt()
	pending.AttemptID="invalid"
	server:=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
		w.Header().Set("Content-Type","application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"pending":pending})
	}))
	defer server.Close()
	c:=New(server.URL)
	value,err:=c.PollRecoveryPendingAttempt(context.Background(),syntheticRecoveryAgentBearer)
	if err==nil||value!=nil {t.Fatalf("untrusted pending metadata accepted: %+v %v",value,err)}
}

func TestRecoveryObservationTransportNotCalledByDefaultAgentRunner(t *testing.T){
	raw,err:=os.ReadFile("../heartbeat/runner.go")
	if err!=nil{t.Fatal(err)}
	for _,name:=range []string{
		"PollRecoveryPendingAttempt(",
		"RequestRecoveryObservationNonce(",
	}{
		if strings.Contains(string(raw),name) {
			t.Fatalf("private recovery transport unexpectedly active in Agent polling: %s",name)
		}
	}
}
