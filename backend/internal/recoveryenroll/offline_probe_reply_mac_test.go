package recoveryenroll

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

type enrollmentProbeVectorV1 struct {
	SchemaVersion int `json:"schemaVersion"`
	TestAgentToken string `json:"testAgentToken"`
	Reply EnrollmentProbeReplyV1 `json:"reply"`
	ExpectedMAC string `json:"expectedMAC"`
}

func readEnrollmentProbeVectorV1(t *testing.T) enrollmentProbeVectorV1 {
	t.Helper()
	raw,err:=os.ReadFile("../../../testdata/rg140/enrollment_probe_reply_v1.json")
	if err!=nil {t.Fatal(err)}
	var v enrollmentProbeVectorV1
	if err=json.Unmarshal(raw,&v);err!=nil||v.SchemaVersion!=1 {t.Fatal("invalid shared MAC vector",err)}
	return v
}

func TestEnrollmentProbeReplyMACOfflineV1SharedAgentManagerVector(t *testing.T) {
	v:=readEnrollmentProbeVectorV1(t)
	got,err:=EnrollmentProbeReplyMACOfflineV1(v.TestAgentToken,v.Reply)
	if err!=nil||got!=v.ExpectedMAC {
		t.Fatalf("Manager/Agent enrollment wire MAC drift: got=%q want=%q err=%v",got,v.ExpectedMAC,err)
	}
	if got==""||len(got)!=64||strings.Contains(got,v.TestAgentToken) {
		t.Fatalf("unexpected signature or secret exposure: %q",got)
	}
}

func TestEnrollmentProbeReplyMACOfflineV1EveryIdentityFieldBound(t *testing.T) {
	v:=readEnrollmentProbeVectorV1(t)
	changes:=map[string]func(*EnrollmentProbeReplyV1){
		"attempt":func(p *EnrollmentProbeReplyV1){p.AttemptID=p.ManagerPreparationID},
		"preparation":func(p *EnrollmentProbeReplyV1){p.ManagerPreparationID=p.AgentID},
		"agent":func(p *EnrollmentProbeReplyV1){p.AgentID=p.ServerID},
		"server":func(p *EnrollmentProbeReplyV1){p.ServerID=p.AgentID},
		"generation":func(p *EnrollmentProbeReplyV1){p.AgentCredentialGeneration++},
		"nonce":func(p *EnrollmentProbeReplyV1){p.Nonce=base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x17},32))},
		"local_genesis":func(p *EnrollmentProbeReplyV1){p.LocalGenesisSHA256=strings.Repeat("1",64)},
	}
	for name,mutate:=range changes {
		t.Run(name,func(t *testing.T){
			changed:=v.Reply
			mutate(&changed)
			got,err:=EnrollmentProbeReplyMACOfflineV1(v.TestAgentToken,changed)
			if err!=nil||got==v.ExpectedMAC {
				t.Fatalf("altered %s retained original MAC: %q %v",name,got,err)
			}
		})
	}
}

func TestEnrollmentProbeReplyMACOfflineV1RejectsInvalidTokenAndShape(t *testing.T) {
	v:=readEnrollmentProbeVectorV1(t)
	for name,token:=range map[string]string{
		"empty":"",
		"registration":"rg_reg_"+strings.TrimPrefix(v.TestAgentToken,"rg_agent_"),
		"padded":v.TestAgentToken+"=",
		"short":"rg_agent_"+base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x01},31)),
		"space":v.TestAgentToken+" ",
	}{
		t.Run(name,func(t *testing.T){
			got,err:=EnrollmentProbeReplyMACOfflineV1(token,v.Reply)
			if got!=""||!errors.Is(err,ErrEnrollmentProbeReplyRefusedV1) {
				t.Fatalf("invalid token signed: %q %v",got,err)
			}
		})
	}
	for name,mutate:=range map[string]func(*EnrollmentProbeReplyV1){
		"schema":func(p *EnrollmentProbeReplyV1){p.SchemaVersion++},
		"attempt":func(p *EnrollmentProbeReplyV1){p.AttemptID="not-uuid"},
		"preparation":func(p *EnrollmentProbeReplyV1){p.ManagerPreparationID=""},
		"agent":func(p *EnrollmentProbeReplyV1){p.AgentID=""},
		"server":func(p *EnrollmentProbeReplyV1){p.ServerID=""},
		"generation":func(p *EnrollmentProbeReplyV1){p.AgentCredentialGeneration=0},
		"nonce_invalid":func(p *EnrollmentProbeReplyV1){p.Nonce="!!!"},
		"nonce_short":func(p *EnrollmentProbeReplyV1){p.Nonce=base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x02},31))},
		"nonce_padded":func(p *EnrollmentProbeReplyV1){p.Nonce+="="},
		"digest_invalid":func(p *EnrollmentProbeReplyV1){p.LocalGenesisSHA256="bad"},
		"digest_uppercase":func(p *EnrollmentProbeReplyV1){p.LocalGenesisSHA256=strings.Repeat("A",64)},
	}{
		t.Run(name,func(t *testing.T){
			changed:=v.Reply
			mutate(&changed)
			got,err:=EnrollmentProbeReplyMACOfflineV1(v.TestAgentToken,changed)
			if got!=""||!errors.Is(err,ErrEnrollmentProbeReplyRefusedV1) {
				t.Fatalf("invalid offline reply signed: %q %v",got,err)
			}
		})
	}
}
