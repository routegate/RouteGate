package recoveryobserve

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

type recoveryEvidenceVectorV1 struct {
	SchemaVersion int `json:"schemaVersion"`
	Marker json.RawMessage `json:"marker"`
	UnacknowledgedReceipt json.RawMessage `json:"unacknowledgedReceipt"`
	AcknowledgedReceipt json.RawMessage `json:"acknowledgedReceipt"`
	Expected struct {
		MarkerSHA256 string `json:"markerSHA256"`
		UnacknowledgedReceiptSHA256 string `json:"unacknowledgedReceiptSHA256"`
		AcknowledgedReceiptSHA256 string `json:"acknowledgedReceiptSHA256"`
		EnvelopeSHA256 string `json:"envelopeSHA256"`
		WitnessSHA256 string `json:"witnessSHA256"`
	} `json:"expected"`
}
func loadEvidenceVectorV1(t *testing.T) recoveryEvidenceVectorV1 {
	t.Helper()
	raw,err:=os.ReadFile("../../../testdata/rg140/runtime_recovery_evidence_v1.json")
	if err!=nil {t.Fatal(err)}
	var fixture recoveryEvidenceVectorV1
	if err=json.Unmarshal(raw,&fixture);err!=nil||fixture.SchemaVersion!=1{
		t.Fatalf("invalid shared evidence fixture: %v",err)
	}
	return fixture
}
func compactEvidenceJSON(t *testing.T,raw []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	if err:=json.Compact(&out,raw);err!=nil{t.Fatal(err)}
	return out.Bytes()
}
func hashEvidenceBytes(raw []byte) string {
	sum:=sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func TestRuntimeRecoveryEvidenceManagerV1SharedByteContract(t *testing.T) {
	v:=loadEvidenceVectorV1(t)
	marker:=compactEvidenceJSON(t,v.Marker)
	beforeBytes:=compactEvidenceJSON(t,v.UnacknowledgedReceipt)
	afterBytes:=compactEvidenceJSON(t,v.AcknowledgedReceipt)
	if hashEvidenceBytes(marker)!=v.Expected.MarkerSHA256 ||
		hashEvidenceBytes(beforeBytes)!=v.Expected.UnacknowledgedReceiptSHA256 ||
		hashEvidenceBytes(afterBytes)!=v.Expected.AcknowledgedReceiptSHA256 ||
		v.Expected.AcknowledgedReceiptSHA256==v.Expected.UnacknowledgedReceiptSHA256{
		t.Fatal("Agent/Manager v1 marker or acknowledged receipt byte contract drift")
	}
	var before,after struct {
		SchemaVersion int `json:"schemaVersion"`
		Binding struct {
			ManagerURL string `json:"managerUrl"`
			AgentID string `json:"agentId"`
			ServerID string `json:"serverId"`
		} `json:"binding"`
		MarkerHash string `json:"markerHash"`
		TaskID string `json:"taskId"`
		Envelope json.RawMessage `json:"envelope"`
		Acknowledged bool `json:"acknowledged"`
		RuntimeWitness RuntimeWitness `json:"runtimeWitness"`
	}
	if err:=json.Unmarshal(beforeBytes,&before);err!=nil{t.Fatal(err)}
	if err:=json.Unmarshal(afterBytes,&after);err!=nil{t.Fatal(err)}
	if before.Acknowledged || !after.Acknowledged ||
		after.MarkerHash!=v.Expected.MarkerSHA256 ||
		after.Binding.ManagerURL!="https://isolated.invalid" ||
		!bytes.Equal(before.Envelope,after.Envelope){
		t.Fatal("only ACKNOWLEDGED receipt bytes may be used for recovery evidence")
	}
	if hashEvidenceBytes(after.Envelope)!=v.Expected.EnvelopeSHA256 {
		t.Fatal("original result envelope digest differs between Agent and Manager")
	}
	witnessHash,err:=RuntimeRecoveryWitnessDigestV1(after.RuntimeWitness)
	if err!=nil||witnessHash!=v.Expected.WitnessSHA256{
		t.Fatal("Agent/Manager witness JSON field encoding drift",err)
	}
	o:=RuntimeObservation{
		SchemaVersion:1,TaskID:after.TaskID,
		AgentID:after.Binding.AgentID,ServerID:after.Binding.ServerID,
		MarkerHash:v.Expected.MarkerSHA256,
		ReceiptHash:v.Expected.AcknowledgedReceiptSHA256,
		EnvelopeHash:v.Expected.EnvelopeSHA256,Witness:after.RuntimeWitness,
	}
	d,err:=DeriveEvidenceDigestsV1(o)
	if err!=nil {t.Fatal(err)}
	expected:=EvidenceDigestsV1{
		SchemaVersion:1,TaskID:after.TaskID,AgentID:after.Binding.AgentID,
		ServerID:after.Binding.ServerID,MarkerSHA256:v.Expected.MarkerSHA256,
		ReceiptSHA256:v.Expected.AcknowledgedReceiptSHA256,
		EnvelopeSHA256:v.Expected.EnvelopeSHA256,
		WitnessSHA256:v.Expected.WitnessSHA256,
	}
	if d!=expected || !MatchesEvidenceDigestsV1(expected,o) {
		t.Fatalf("shared v1 digest wire mismatch: %+v want %+v",d,expected)
	}
}

func TestRuntimeRecoveryEvidenceManagerRejectsTypeAndHashConflation(t *testing.T) {
	v:=loadEvidenceVectorV1(t)
	var receipt struct{
		Binding struct{
			AgentID string `json:"agentId"`
			ServerID string `json:"serverId"`
		} `json:"binding"`
		TaskID string `json:"taskId"`
		RuntimeWitness RuntimeWitness `json:"runtimeWitness"`
	}
	if err:=json.Unmarshal(v.AcknowledgedReceipt,&receipt);err!=nil{t.Fatal(err)}
	obs:=RuntimeObservation{
		SchemaVersion:1,TaskID:receipt.TaskID,AgentID:receipt.Binding.AgentID,
		ServerID:receipt.Binding.ServerID,MarkerHash:v.Expected.MarkerSHA256,
		ReceiptHash:v.Expected.AcknowledgedReceiptSHA256,
		EnvelopeHash:v.Expected.EnvelopeSHA256,Witness:receipt.RuntimeWitness,
	}
	valid,err:=DeriveEvidenceDigestsV1(obs)
	if err!=nil{t.Fatal(err)}
	for name,mutate:=range map[string]func(*EvidenceDigestsV1){
		"pre_ack_receipt":func(m *EvidenceDigestsV1){m.ReceiptSHA256=v.Expected.UnacknowledgedReceiptSHA256},
		"marker_receipt_swapped":func(m *EvidenceDigestsV1){m.MarkerSHA256=m.ReceiptSHA256},
		"witness_config_hash":func(m *EvidenceDigestsV1){m.WitnessSHA256=obs.Witness.RuntimeHash},
		"witness_envelope_hash":func(m *EvidenceDigestsV1){m.WitnessSHA256=m.EnvelopeSHA256},
		"changed_envelope":func(m *EvidenceDigestsV1){m.EnvelopeSHA256=strings.Repeat("1",64)},
		"uppercase_digest":func(m *EvidenceDigestsV1){m.MarkerSHA256=strings.Repeat("A",64)},
		"changed_task":func(m *EvidenceDigestsV1){m.TaskID=m.AgentID},
		"changed_agent":func(m *EvidenceDigestsV1){m.AgentID=m.ServerID},
		"changed_server":func(m *EvidenceDigestsV1){m.ServerID=m.AgentID},
		"schema_drift":func(m *EvidenceDigestsV1){m.SchemaVersion++},
	}{
		t.Run(name,func(t *testing.T){
			candidate:=valid
			mutate(&candidate)
			if MatchesEvidenceDigestsV1(candidate,obs){
				t.Fatalf("mismatched evidence domain unexpectedly accepted: %+v",candidate)
			}
		})
	}
	for name,mutate:=range map[string]func(*RuntimeObservation){
		"wrong_runtime_schema":func(o *RuntimeObservation){o.Witness.SchemaVersion++},
		"changed_generation":func(o *RuntimeObservation){o.Witness.Generation.InvocationID=strings.Repeat("c",32)},
		"same_generation":func(o *RuntimeObservation){o.Witness.PreviousGeneration=o.Witness.Generation},
		"invalid_monotonic":func(o *RuntimeObservation){o.Witness.Generation.StartedMonotonic="not-a-number"},
		"invalid_listener":func(o *RuntimeObservation){o.Witness.ListenerPort=0},
		"empty_agent":func(o *RuntimeObservation){o.AgentID=""},
	}{
		t.Run(name,func(t *testing.T){
			candidate:=obs
			mutate(&candidate)
			if MatchesEvidenceDigestsV1(valid,candidate){
				t.Fatal("invalid runtime observation accepted")
			}
		})
	}
}
