package tasks

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

type recoveryEnrollmentProbeVectorV1 struct {
	SchemaVersion int `json:"schemaVersion"`
	TestAgentToken string `json:"testAgentToken"`
	Reply recoveryEnrollmentProbeReplyV1 `json:"reply"`
	ExpectedMAC string `json:"expectedMAC"`
}

func readRecoveryEnrollmentProbeVectorV1(t *testing.T) recoveryEnrollmentProbeVectorV1 {
	t.Helper()
	raw,err:=os.ReadFile("../../../testdata/rg140/enrollment_probe_reply_v1.json")
	if err!=nil{t.Fatal(err)}
	var v recoveryEnrollmentProbeVectorV1
	if err=json.Unmarshal(raw,&v);err!=nil||v.SchemaVersion!=1 {
		t.Fatal("invalid shared Agent enrollment probe vector",err)
	}
	return v
}

func TestRuntimeRecoveryEnrollmentProbeReplyMACOfflineV1ManagerWireParity(t *testing.T) {
	v:=readRecoveryEnrollmentProbeVectorV1(t)
	got,err:=recoveryEnrollmentProbeReplyMACOfflineV1(v.TestAgentToken,v.Reply)
	if err!=nil||got!=v.ExpectedMAC {
		t.Fatalf("Agent/Manager offline HMAC mismatch: got=%q want=%q err=%v",got,v.ExpectedMAC,err)
	}
}

func TestRuntimeRecoveryEnrollmentProbeReplyMACOfflineV1TamperChangesMAC(t *testing.T) {
	v:=readRecoveryEnrollmentProbeVectorV1(t)
	for name,mutate:=range map[string]func(*recoveryEnrollmentProbeReplyV1){
		"attempt":func(p *recoveryEnrollmentProbeReplyV1){p.AttemptID=p.AgentID},
		"preparation":func(p *recoveryEnrollmentProbeReplyV1){p.ManagerPreparationID=p.ServerID},
		"agent":func(p *recoveryEnrollmentProbeReplyV1){p.AgentID=p.ServerID},
		"server":func(p *recoveryEnrollmentProbeReplyV1){p.ServerID=p.AgentID},
		"generation":func(p *recoveryEnrollmentProbeReplyV1){p.AgentCredentialGeneration++},
		"nonce":func(p *recoveryEnrollmentProbeReplyV1){p.Nonce=base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x44},32))},
		"digest":func(p *recoveryEnrollmentProbeReplyV1){p.LocalGenesisSHA256=strings.Repeat("2",64)},
	}{
		t.Run(name,func(t *testing.T){
			changed:=v.Reply
			mutate(&changed)
			got,err:=recoveryEnrollmentProbeReplyMACOfflineV1(v.TestAgentToken,changed)
			if err!=nil||got==v.ExpectedMAC {
				t.Fatalf("Agent reply mutation retained signed MAC: %q %v",got,err)
			}
		})
	}
}

func TestRuntimeRecoveryEnrollmentProbeReplyMACOfflineV1RejectsInvalidInput(t *testing.T) {
	v:=readRecoveryEnrollmentProbeVectorV1(t)
	for name,mutate:=range map[string]func(*recoveryEnrollmentProbeReplyV1){
		"schema":func(p *recoveryEnrollmentProbeReplyV1){p.SchemaVersion++},
		"attempt":func(p *recoveryEnrollmentProbeReplyV1){p.AttemptID="bad"},
		"agent":func(p *recoveryEnrollmentProbeReplyV1){p.AgentID=""},
		"server":func(p *recoveryEnrollmentProbeReplyV1){p.ServerID=""},
		"generation":func(p *recoveryEnrollmentProbeReplyV1){p.AgentCredentialGeneration=0},
		"nonce":func(p *recoveryEnrollmentProbeReplyV1){p.Nonce="bad"},
		"nonce_length":func(p *recoveryEnrollmentProbeReplyV1){p.Nonce=base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x55},31))},
		"digest":func(p *recoveryEnrollmentProbeReplyV1){p.LocalGenesisSHA256="bad"},
	}{
		t.Run(name,func(t *testing.T){
			changed:=v.Reply
			mutate(&changed)
			got,err:=recoveryEnrollmentProbeReplyMACOfflineV1(v.TestAgentToken,changed)
			if got!=""||!errors.Is(err,ErrRuntimeMutationBlocked) {
				t.Fatalf("invalid Agent offline probe signed: %q %v",got,err)
			}
		})
	}
	for name,token:=range map[string]string{
		"missing":"",
		"registration":"rg_reg_"+strings.TrimPrefix(v.TestAgentToken,"rg_agent_"),
		"padded":v.TestAgentToken+"=",
	}{
		t.Run(name,func(t *testing.T){
			got,err:=recoveryEnrollmentProbeReplyMACOfflineV1(token,v.Reply)
			if got!=""||!errors.Is(err,ErrRuntimeMutationBlocked) {
				t.Fatalf("invalid Agent token signed: %q %v",got,err)
			}
		})
	}
}
