package tasks

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type tokenProofVectorV1 struct {
	SchemaVersion int `json:"schemaVersion"`
	TestAgentToken string `json:"testAgentToken"`
	Claim RuntimeRecoveryTokenBoundEvidenceV1 `json:"claim"`
	ExpectedMAC string `json:"expectedMAC"`
	Reply RuntimeRecoveryChallengedObservation `json:"reply"`
	ExpectedFullReplyMAC string `json:"expectedFullReplyMAC"`
}

func loadRecoveryTokenVectorV1(t *testing.T) tokenProofVectorV1 {
	t.Helper()
	raw,err:=os.ReadFile("../../../testdata/rg140/token_bound_evidence_v1.json")
	if err!=nil{t.Fatal(err)}
	var v tokenProofVectorV1
	if err=json.Unmarshal(raw,&v);err!=nil||v.SchemaVersion!=1{t.Fatalf("bad token proof vector: %v",err)}
	return v
}

func TestRuntimeRecoveryTokenProofSharedV1MAC(t *testing.T) {
	v:=loadRecoveryTokenVectorV1(t)
	mac,err:=RuntimeRecoveryTokenProofMACV1(v.TestAgentToken,v.Claim)
	if err!=nil||mac!=v.ExpectedMAC{
		t.Fatalf("shared Agent HMAC encoding mismatch: %s %v",mac,err)
	}
	// Signing a claimed proof never requires altering or releasing the
	// original private runtime state.
	e,task,adapter,binding:=recoveryFixture(t)
	live,err:=ObserveRuntimeRecoveryEvidenceV1(context.Background(),e.receiptDir,binding,task.ID,e.activePath,adapter)
	if err!=nil{t.Fatal(err)}
	p:=v.Claim
	p.Evidence=live
	liveMAC,err:=RuntimeRecoveryTokenProofMACV1(v.TestAgentToken,p)
	if err!=nil||len(liveMAC)!=64{t.Fatal("failed to sign read-only live evidence",err)}
	raw,err:=os.ReadFile(filepath.Join(e.receiptDir,"mutation-inflight.json"))
	if err!=nil||len(raw)==0{t.Fatal("signing cleared durable mutation intent",err)}
	if lease,err:=BeginRuntimeMutation(e.receiptDir,task.ID,TaskKindVPNCoreService);err==nil{
		_ = lease.Close()
		t.Fatal("signing unlocked Agent")
	}
}

func TestRuntimeRecoveryTokenProofRejectsMalformedInput(t *testing.T) {
	v:=loadRecoveryTokenVectorV1(t)
	for name,token:=range map[string]string{
		"blank":"", "registration_token":"rg_reg_"+strings.TrimPrefix(v.TestAgentToken,"rg_agent_"),
		"not_32_bytes":"rg_agent_"+base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{1},31)),
		"whitespace":v.TestAgentToken+" ", "padded":v.TestAgentToken+"=",
	}{
		t.Run(name,func(t *testing.T){
			if signed,err:=RuntimeRecoveryTokenProofMACV1(token,v.Claim);!errors.Is(err,ErrRuntimeMutationBlocked)||signed!=""{
				t.Fatalf("invalid Agent credential accepted: %q %v",signed,err)
			}
		})
	}
	for name,edit:=range map[string]func(*RuntimeRecoveryTokenBoundEvidenceV1){
		"wrong_version":func(p *RuntimeRecoveryTokenBoundEvidenceV1){p.SchemaVersion++},
		"wrong_observation":func(p *RuntimeRecoveryTokenBoundEvidenceV1){p.ObservationID="" },
		"wrong_reservation":func(p *RuntimeRecoveryTokenBoundEvidenceV1){p.ReservationID="" },
		"wrong_attempt":func(p *RuntimeRecoveryTokenBoundEvidenceV1){p.AttemptID="missing" },
		"zero_epoch":func(p *RuntimeRecoveryTokenBoundEvidenceV1){p.AttemptEpoch=0},
		"zero_generation":func(p *RuntimeRecoveryTokenBoundEvidenceV1){p.AgentCredentialGeneration=0},
		"short_nonce":func(p *RuntimeRecoveryTokenBoundEvidenceV1){p.Nonce=base64.RawURLEncoding.EncodeToString([]byte{1})},
		"padded_nonce":func(p *RuntimeRecoveryTokenBoundEvidenceV1){p.Nonce+="="},
		"wrong_evidence_schema":func(p *RuntimeRecoveryTokenBoundEvidenceV1){p.Evidence.SchemaVersion++},
		"bad_receipt_hash":func(p *RuntimeRecoveryTokenBoundEvidenceV1){p.Evidence.ReceiptSHA256="not-a-hash"},
		"bad_witness_hash":func(p *RuntimeRecoveryTokenBoundEvidenceV1){p.Evidence.WitnessSHA256=""},
		"wrong_task":func(p *RuntimeRecoveryTokenBoundEvidenceV1){p.Evidence.TaskID=""},
		"wrong_agent":func(p *RuntimeRecoveryTokenBoundEvidenceV1){p.Evidence.AgentID=""},
		"wrong_server":func(p *RuntimeRecoveryTokenBoundEvidenceV1){p.Evidence.ServerID=""},
	}{
		t.Run(name,func(t *testing.T){
			p:=v.Claim
			edit(&p)
			if signed,err:=RuntimeRecoveryTokenProofMACV1(v.TestAgentToken,p);!errors.Is(err,ErrRuntimeMutationBlocked)||signed!=""{
				t.Fatalf("invalid claim admitted: %s %v",signed,err)
			}
		})
	}
}

func TestRuntimeRecoveryTokenProofBindsEveryField(t *testing.T) {
	v:=loadRecoveryTokenVectorV1(t)
	changes:=map[string]func(*RuntimeRecoveryTokenBoundEvidenceV1){
		"nonce":func(p *RuntimeRecoveryTokenBoundEvidenceV1){p.Nonce=base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0xa5},32))},
		"observation_id":func(p *RuntimeRecoveryTokenBoundEvidenceV1){p.ObservationID=p.ReservationID},
		"reservation_id":func(p *RuntimeRecoveryTokenBoundEvidenceV1){p.ReservationID=p.AttemptID},
		"attempt_id":func(p *RuntimeRecoveryTokenBoundEvidenceV1){p.AttemptID=p.ReservationID},
		"attempt_epoch":func(p *RuntimeRecoveryTokenBoundEvidenceV1){p.AttemptEpoch++},
		"credential_generation":func(p *RuntimeRecoveryTokenBoundEvidenceV1){p.AgentCredentialGeneration++},
		"task_id":func(p *RuntimeRecoveryTokenBoundEvidenceV1){p.Evidence.TaskID=p.AttemptID},
		"agent_id":func(p *RuntimeRecoveryTokenBoundEvidenceV1){p.Evidence.AgentID=p.Evidence.ServerID},
		"server_id":func(p *RuntimeRecoveryTokenBoundEvidenceV1){p.Evidence.ServerID=p.Evidence.AgentID},
		"marker":func(p *RuntimeRecoveryTokenBoundEvidenceV1){p.Evidence.MarkerSHA256=strings.Repeat("1",64)},
		"receipt":func(p *RuntimeRecoveryTokenBoundEvidenceV1){p.Evidence.ReceiptSHA256=strings.Repeat("1",64)},
		"envelope":func(p *RuntimeRecoveryTokenBoundEvidenceV1){p.Evidence.EnvelopeSHA256=strings.Repeat("1",64)},
		"witness":func(p *RuntimeRecoveryTokenBoundEvidenceV1){p.Evidence.WitnessSHA256=strings.Repeat("1",64)},
	}
	for name,edit:=range changes {
		t.Run(name,func(t *testing.T){
			p:=v.Claim
			edit(&p)
			got,err:=RuntimeRecoveryTokenProofMACV1(v.TestAgentToken,p)
			if err!=nil||got==v.ExpectedMAC{t.Fatalf("mutation not bound by token MAC: %v",err)}
		})
	}
}

func TestRuntimeRecoveryFullReplyMACV1SharedAgentVectorAndTamper(t *testing.T) {
	v:=loadRecoveryTokenVectorV1(t)
	mac,err:=RuntimeRecoveryFullReplyMACV1(v.TestAgentToken,v.Claim,v.Reply)
	if err!=nil||mac!=v.ExpectedFullReplyMAC{
		t.Fatalf("Agent full-response HMAC protocol drift: %s %v",mac,err)
	}
	changes:=map[string]func(*RuntimeRecoveryChallengedObservation){
		"observed_at":func(r *RuntimeRecoveryChallengedObservation){r.ObservedAt=r.ObservedAt.Add(time.Second)},
		"nonce":func(r *RuntimeRecoveryChallengedObservation){r.Nonce=base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x51},32))},
		"generation":func(r *RuntimeRecoveryChallengedObservation){r.Observation.Witness.Generation.InvocationID=strings.Repeat("c",32)},
		"runtime_hash":func(r *RuntimeRecoveryChallengedObservation){r.Observation.Witness.RuntimeHash=strings.Repeat("a",64)},
		"receipt":func(r *RuntimeRecoveryChallengedObservation){r.Observation.ReceiptHash=strings.Repeat("1",64)},
	}
	for name,mutate:=range changes {
		t.Run(name,func(t *testing.T){
			changed:=v.Reply
			mutate(&changed)
			out,err:=RuntimeRecoveryFullReplyMACV1(v.TestAgentToken,v.Claim,changed)
			if err!=nil||out==v.ExpectedFullReplyMAC {
				t.Fatalf("modified complete observation kept original MAC: %v",err)
			}
		})
	}
}
