package recoveryobserve

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

type tokenProvenanceVectorV1 struct {
	SchemaVersion int `json:"schemaVersion"`
	TestAgentToken string `json:"testAgentToken"`
	Claim TokenBoundEvidenceV1 `json:"claim"`
	ExpectedMAC string `json:"expectedMAC"`
	Reply ObservationReply `json:"reply"`
	ExpectedFullReplyMAC string `json:"expectedFullReplyMAC"`
}
func readTokenProvenanceVector(t *testing.T)tokenProvenanceVectorV1{
	t.Helper()
	raw,err:=os.ReadFile("../../../testdata/rg140/token_bound_evidence_v1.json")
	if err!=nil{t.Fatal(err)}
	var v tokenProvenanceVectorV1
	if err=json.Unmarshal(raw,&v);err!=nil||v.SchemaVersion!=1{t.Fatal("shared token vector invalid",err)}
	return v
}

func TestRuntimeRecoveryTokenProvenanceV1SharedMAC(t *testing.T) {
	v:=readTokenProvenanceVector(t)
	got,err:=TokenProofMACV1(v.TestAgentToken,v.Claim)
	if err!=nil || got!=v.ExpectedMAC {
		t.Fatalf("Agent/Manager HMAC wire drift: %s %v",got,err)
	}
	if !validTokenBoundEvidenceV1(v.Claim){t.Fatal("known-good synthetic claim rejected")}
}

func TestRuntimeRecoveryTokenProvenanceV1RejectsMalformedOrAlteredClaims(t *testing.T) {
	v:=readTokenProvenanceVector(t)
	for name,token:=range map[string]string{
		"missing":"", "registration":"rg_reg_"+strings.TrimPrefix(v.TestAgentToken,"rg_agent_"),
		"padded":v.TestAgentToken+"=","space":v.TestAgentToken+" ",
		"short":"rg_agent_"+base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{1},31)),
	}{
		t.Run(name,func(t *testing.T) {
			if result,err:=TokenProofMACV1(token,v.Claim);!errors.Is(err,ErrTokenBoundEvidenceRefused)||result!=""{
				t.Fatalf("invalid token accepted: %s %v",result,err)
			}
		})
	}
	for name,change:=range map[string]func(*TokenBoundEvidenceV1){
		"schema":func(p *TokenBoundEvidenceV1){p.SchemaVersion++},
		"attempt":func(p *TokenBoundEvidenceV1){p.AttemptID="" },
		"reservation":func(p *TokenBoundEvidenceV1){p.ReservationID="invalid" },
		"observation":func(p *TokenBoundEvidenceV1){p.ObservationID="" },
		"epoch":func(p *TokenBoundEvidenceV1){p.AttemptEpoch=0},
		"generation":func(p *TokenBoundEvidenceV1){p.AgentCredentialGeneration=0},
		"nonce":func(p *TokenBoundEvidenceV1){p.Nonce="!" },
		"evidence_schema":func(p *TokenBoundEvidenceV1){p.Evidence.SchemaVersion++},
		"agent":func(p *TokenBoundEvidenceV1){p.Evidence.AgentID="" },
		"server":func(p *TokenBoundEvidenceV1){p.Evidence.ServerID="" },
		"task":func(p *TokenBoundEvidenceV1){p.Evidence.TaskID="" },
		"hash":func(p *TokenBoundEvidenceV1){p.Evidence.MarkerSHA256="no" },
	}{
		t.Run(name,func(t *testing.T){
			claim:=v.Claim
			change(&claim)
			if mac,err:=TokenProofMACV1(v.TestAgentToken,claim);!errors.Is(err,ErrTokenBoundEvidenceRefused)||mac!=""{
				t.Fatalf("invalid token-bound evidence accepted: %s %v",mac,err)
			}
		})
	}
	for name,change:=range map[string]func(*TokenBoundEvidenceV1){
		"nonce":func(p *TokenBoundEvidenceV1){p.Nonce=base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{3},32))},
		"attempt":func(p *TokenBoundEvidenceV1){p.AttemptID=p.ReservationID},
		"epoch":func(p *TokenBoundEvidenceV1){p.AttemptEpoch++},
		"generation":func(p *TokenBoundEvidenceV1){p.AgentCredentialGeneration++},
		"server":func(p *TokenBoundEvidenceV1){p.Evidence.ServerID=p.Evidence.AgentID},
		"receipt":func(p *TokenBoundEvidenceV1){p.Evidence.ReceiptSHA256=strings.Repeat("1",64)},
		"witness":func(p *TokenBoundEvidenceV1){p.Evidence.WitnessSHA256=strings.Repeat("1",64)},
	}{
		t.Run(name,func(t *testing.T){
			claim:=v.Claim
			change(&claim)
			got,err:=TokenProofMACV1(v.TestAgentToken,claim)
			if err!=nil||got==v.ExpectedMAC{t.Fatal("changed claim retained original HMAC",err)}
		})
	}
}

func TestRuntimeRecoveryFullReplyMACV1SharedManagerVectorAndTamper(t *testing.T){
	v:=readTokenProvenanceVector(t)
	mac,err:=FullReplyMACV1(v.TestAgentToken,v.Claim,v.Reply)
	if err!=nil || mac!=v.ExpectedFullReplyMAC{
		t.Fatalf("Manager full-response MAC diverged from Agent: %s %v",mac,err)
	}
	changes:=map[string]func(*ObservationReply){
		"observed_at":func(r *ObservationReply){r.ObservedAt=r.ObservedAt.Add(time.Second)},
		"nonce":func(r *ObservationReply){r.Nonce=base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x51},32))},
		"generation":func(r *ObservationReply){r.Observation.Witness.Generation.InvocationID=strings.Repeat("c",32)},
		"runtime_hash":func(r *ObservationReply){r.Observation.Witness.RuntimeHash=strings.Repeat("a",64)},
		"receipt":func(r *ObservationReply){r.Observation.ReceiptHash=strings.Repeat("1",64)},
	}
	for name,mutate:=range changes{
		t.Run(name,func(t *testing.T){
			changed:=v.Reply
			mutate(&changed)
			out,err:=FullReplyMACV1(v.TestAgentToken,v.Claim,changed)
			if err!=nil||out==v.ExpectedFullReplyMAC{
				t.Fatalf("changed full reply kept original MAC: %v",err)
			}
		})
	}
}
