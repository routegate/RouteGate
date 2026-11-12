package client

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ikaevus/routegate/agent/internal/tasks"
)

func clientRecoveryReplyFixture(t *testing.T, managerURL string) (
	tasks.RuntimeRecoveryObservationChallenge,tasks.RuntimeRecoveryChallengedObservation,
) {
	t.Helper()
	p:=testRecoveryPendingAttempt()
	witness:=tasks.RuntimeRecoveryWitness{
		SchemaVersion:1,RuntimeHash:strings.Repeat("e",64),
		Generation:tasks.RuntimeGeneration{InvocationID:strings.Repeat("b",32),PID:311,StartedMonotonic:"700"},
		PreviousGeneration:tasks.RuntimeGeneration{InvocationID:strings.Repeat("a",32),PID:310,StartedMonotonic:"600"},
		ListenerPort:443,
	}
	obs:=tasks.RuntimeRecoveryObservation{
		SchemaVersion:1,TaskID:p.TaskID,AgentID:p.AgentID,ServerID:p.ServerID,
		MarkerHash:strings.Repeat("c",64),ReceiptHash:strings.Repeat("d",64),
		EnvelopeHash:strings.Repeat("f",64),Witness:witness,
	}
	evidence,err:=tasks.DeriveRuntimeRecoveryEvidenceDigestsV1(obs)
	if err!=nil{t.Fatal(err)}
	now:=time.Now().UTC().Add(-time.Second)
	challenge:=tasks.RuntimeRecoveryObservationChallenge{
		SchemaVersion:1,ObservationID:"66666666-6666-4666-8666-666666666666",
		ReservationID:p.ReservationID,AttemptID:p.AttemptID,
		AttemptEpoch:p.AttemptEpoch,
		Nonce:base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x91},32)),
		IssuedAt:now,ExpiresAt:now.Add(45*time.Second),
		Binding:tasks.RuntimeResultBinding{
			ManagerURL:managerURL,AgentID:p.AgentID,ServerID:p.ServerID,
		},
		TaskID:p.TaskID,AgentCredentialGeneration:p.AgentCredentialGeneration,
		MarkerHash:evidence.MarkerSHA256,ReceiptHash:evidence.ReceiptSHA256,
		RuntimeWitnessHash:evidence.WitnessSHA256,
	}
	reply:=tasks.RuntimeRecoveryChallengedObservation{
		SchemaVersion:1,ObservationID:challenge.ObservationID,
		ReservationID:challenge.ReservationID,AttemptID:challenge.AttemptID,
		AttemptEpoch:challenge.AttemptEpoch,Nonce:challenge.Nonce,
		AgentCredentialGeneration:challenge.AgentCredentialGeneration,
		ObservedAt:now.Add(500*time.Millisecond),Observation:obs,
	}
	return challenge,reply
}

func TestRecoveryObservationReplyTransportSignedOneShotAgentSubmission(t *testing.T){
	var calls atomic.Int32
	var server *httptest.Server
	server=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
		calls.Add(1)
		if r.Method!=http.MethodPost ||
			r.URL.Path!="/api/v1/agent/recovery-observations/reply" ||
			r.Header.Get("Authorization")!="Bearer "+syntheticRecoveryAgentBearer {
			t.Errorf("recovery reply did not use existing Agent outbound bearer path: %s %s",r.Method,r.URL)
			w.WriteHeader(http.StatusUnauthorized);return
		}
		var submitted observationReplySubmission
		decoder:=json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err:=decoder.Decode(&submitted);err!=nil {t.Error(err);w.WriteHeader(http.StatusBadRequest);return}
		expectedMAC,err:=tasks.RuntimeRecoveryTokenProofMACV1(syntheticRecoveryAgentBearer,submitted.Claim)
		if err!=nil || submitted.MAC!=expectedMAC ||
			submitted.Claim.Evidence.MarkerSHA256!=submitted.Reply.Observation.MarkerHash ||
			submitted.Claim.Evidence.ReceiptSHA256!=submitted.Reply.Observation.ReceiptHash ||
			submitted.Claim.Nonce!=submitted.Reply.Nonce ||
			submitted.Claim.ObservationID!=submitted.Reply.ObservationID {
			t.Errorf("claim / HMAC does not bind Agent's read-only reply: %+v %v",submitted.Claim,err)
			w.WriteHeader(http.StatusConflict);return
		}
		w.Header().Set("Content-Type","application/json")
		w.WriteHeader(http.StatusAccepted)
		_,_ = w.Write([]byte(`{"state":"reply_matched_unattested"}`))
	}))
	defer server.Close()
	challenge,reply:=clientRecoveryReplyFixture(t,server.URL)
	client:=New(server.URL)
	if err:=client.SubmitRecoveryObservationReplyV1(
		context.Background(),syntheticRecoveryAgentBearer,challenge,reply);err!=nil{
		t.Fatal(err)
	}
	if calls.Load()!=1{t.Fatalf("expected exactly one signed reply HTTP request; got %d",calls.Load())}
}

func TestRecoveryObservationReplyTransportLostReplyDoesNotRetry(t *testing.T){
	var calls atomic.Int32
	server:=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
		calls.Add(1)
		// Manager may already have committed both nonce consumption and
		// audit when its response is lost on the wire.
		w.WriteHeader(http.StatusAccepted)
		_,_=w.Write([]byte(`{"state":`))
	}))
	defer server.Close()
	challenge,reply:=clientRecoveryReplyFixture(t,server.URL)
	client:=New(server.URL)
	err:=client.SubmitRecoveryObservationReplyV1(
		context.Background(),syntheticRecoveryAgentBearer,challenge,reply)
	if err==nil{t.Fatal("truncated response was accepted as durable success")}
	if calls.Load()!=1{t.Fatalf("ambiguous reply triggered automatic replay: %d",calls.Load())}
}

func TestRecoveryObservationReplyTransportRejectsMismatchedSnapshotWithoutHTTP(t *testing.T){
	for name,change:=range map[string]func(*tasks.RuntimeRecoveryObservationChallenge,*tasks.RuntimeRecoveryChallengedObservation){
		"other_nonce":func(c *tasks.RuntimeRecoveryObservationChallenge,r *tasks.RuntimeRecoveryChallengedObservation){r.Nonce=base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x92},32))},
		"other_attempt":func(c *tasks.RuntimeRecoveryObservationChallenge,r *tasks.RuntimeRecoveryChallengedObservation){r.AttemptID=c.ReservationID},
		"other_observation":func(c *tasks.RuntimeRecoveryObservationChallenge,r *tasks.RuntimeRecoveryChallengedObservation){r.ObservationID=c.AttemptID},
		"other_epoch":func(c *tasks.RuntimeRecoveryObservationChallenge,r *tasks.RuntimeRecoveryChallengedObservation){r.AttemptEpoch++},
		"other_generation":func(c *tasks.RuntimeRecoveryObservationChallenge,r *tasks.RuntimeRecoveryChallengedObservation){r.AgentCredentialGeneration++},
		"other_server":func(c *tasks.RuntimeRecoveryObservationChallenge,r *tasks.RuntimeRecoveryChallengedObservation){r.Observation.ServerID=r.Observation.AgentID},
		"other_task":func(c *tasks.RuntimeRecoveryObservationChallenge,r *tasks.RuntimeRecoveryChallengedObservation){r.Observation.TaskID=c.AttemptID},
		"changed_marker":func(c *tasks.RuntimeRecoveryObservationChallenge,r *tasks.RuntimeRecoveryChallengedObservation){r.Observation.MarkerHash=strings.Repeat("1",64)},
		"changed_receipt":func(c *tasks.RuntimeRecoveryObservationChallenge,r *tasks.RuntimeRecoveryChallengedObservation){r.Observation.ReceiptHash=strings.Repeat("1",64)},
		"changed_witness":func(c *tasks.RuntimeRecoveryObservationChallenge,r *tasks.RuntimeRecoveryChallengedObservation){r.Observation.Witness.Generation.InvocationID=strings.Repeat("f",32)},
		"bad_generation":func(c *tasks.RuntimeRecoveryObservationChallenge,r *tasks.RuntimeRecoveryChallengedObservation){r.Observation.Witness.PreviousGeneration=r.Observation.Witness.Generation},
		"stale_observation":func(c *tasks.RuntimeRecoveryObservationChallenge,r *tasks.RuntimeRecoveryChallengedObservation){r.ObservedAt=r.ObservedAt.Add(-3*time.Minute)},
		"future_observation":func(c *tasks.RuntimeRecoveryObservationChallenge,r *tasks.RuntimeRecoveryChallengedObservation){r.ObservedAt=r.ObservedAt.Add(3*time.Minute)},
		"expired":func(c *tasks.RuntimeRecoveryObservationChallenge,r *tasks.RuntimeRecoveryChallengedObservation){c.ExpiresAt=time.Now().Add(-time.Second)},
		"wrong_manager":func(c *tasks.RuntimeRecoveryObservationChallenge,r *tasks.RuntimeRecoveryChallengedObservation){c.Binding.ManagerURL="https://other-manager.invalid"},
		"invalid_nonce":func(c *tasks.RuntimeRecoveryObservationChallenge,r *tasks.RuntimeRecoveryChallengedObservation){c.Nonce="abc";r.Nonce="abc"},
	}{
		t.Run(name,func(t *testing.T){
			var calls atomic.Int32
			server:=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
				calls.Add(1)
				w.WriteHeader(http.StatusAccepted)
				_,_=w.Write([]byte(`{"state":"reply_matched_unattested"}`))
			}))
			defer server.Close()
			challenge,reply:=clientRecoveryReplyFixture(t,server.URL)
			change(&challenge,&reply)
			err:=New(server.URL).SubmitRecoveryObservationReplyV1(
				context.Background(),syntheticRecoveryAgentBearer,challenge,reply)
			if !errors.Is(err,ErrRecoveryObservationTransportRefused) ||
				calls.Load()!=0 {
				t.Fatalf("unbound local snapshot reached Manager: calls=%d err=%v",calls.Load(),err)
			}
		})
	}
}

func TestRecoveryObservationReplyTransportRefusesInventedApproval(t *testing.T){
	var calls atomic.Int32
	server:=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
		calls.Add(1)
		w.Header().Set("Content-Type","application/json")
		w.WriteHeader(http.StatusAccepted)
		_,_=w.Write([]byte(`{"state":"recovered"}`))
	}))
	defer server.Close()
	challenge,reply:=clientRecoveryReplyFixture(t,server.URL)
	err:=New(server.URL).SubmitRecoveryObservationReplyV1(
		context.Background(),syntheticRecoveryAgentBearer,challenge,reply)
	if !errors.Is(err,ErrRecoveryObservationTransportRefused) ||
		calls.Load()!=1 {
		t.Fatalf("unknown manager approval state accepted: %v; calls=%d",err,calls.Load())
	}
}
