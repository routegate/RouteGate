package tasks

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
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

func loadRecoveryEvidenceVectorV1(t *testing.T) recoveryEvidenceVectorV1 {
	t.Helper()
	data,err:=os.ReadFile("../../../testdata/rg140/runtime_recovery_evidence_v1.json")
	if err!=nil {t.Fatal(err)}
	var v recoveryEvidenceVectorV1
	if err:=json.Unmarshal(data,&v);err!=nil||v.SchemaVersion!=1 {
		t.Fatalf("invalid shared recovery evidence vector: %v",err)
	}
	return v
}
func compactRecoveryVectorJSON(t *testing.T, raw []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	if err:=json.Compact(&out,raw);err!=nil{t.Fatal(err)}
	return out.Bytes()
}
func digestRecoveryVectorBytes(raw []byte) string {
	sum:=sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func TestRuntimeRecoveryEvidenceV1SharedByteContract(t *testing.T) {
	v:=loadRecoveryEvidenceVectorV1(t)
	marker:=compactRecoveryVectorJSON(t,v.Marker)
	unack:=compactRecoveryVectorJSON(t,v.UnacknowledgedReceipt)
	ack:=compactRecoveryVectorJSON(t,v.AcknowledgedReceipt)
	if digestRecoveryVectorBytes(marker)!=v.Expected.MarkerSHA256 ||
		digestRecoveryVectorBytes(unack)!=v.Expected.UnacknowledgedReceiptSHA256 ||
		digestRecoveryVectorBytes(ack)!=v.Expected.AcknowledgedReceiptSHA256 ||
		v.Expected.UnacknowledgedReceiptSHA256==v.Expected.AcknowledgedReceiptSHA256 {
		t.Fatal("receipt/marker SHA256 changed, or pre-ACK receipt confused with acknowledged file")
	}
	var before,after runtimeResultReceipt
	if err:=json.Unmarshal(unack,&before);err!=nil {t.Fatal(err)}
	if err:=json.Unmarshal(ack,&after);err!=nil {t.Fatal(err)}
	if before.Acknowledged || !after.Acknowledged || before.MarkerHash!=v.Expected.MarkerSHA256 ||
		after.MarkerHash!=v.Expected.MarkerSHA256 || after.RuntimeWitness==nil ||
		!validRuntimeResult(after.Envelope) || after.TaskID!=before.TaskID ||
		after.Binding!=before.Binding || !bytes.Equal(before.Envelope,after.Envelope) {
		t.Fatal("golden receipt does not match Agent durable v1 structure")
	}
	if digestRecoveryVectorBytes(after.Envelope)!=v.Expected.EnvelopeSHA256 {
		t.Fatal("result envelope is not hashed by its original success JSON bytes")
	}
	witnessSHA256,err:=RuntimeRecoveryWitnessDigestV1(*after.RuntimeWitness)
	if err!=nil||witnessSHA256!=v.Expected.WitnessSHA256{
		t.Fatal("witness JSON digest deviated from cross-module format",err)
	}
	obs:=RuntimeRecoveryObservation{
		SchemaVersion:1,TaskID:after.TaskID,AgentID:after.Binding.AgentID,
		ServerID:after.Binding.ServerID,MarkerHash:v.Expected.MarkerSHA256,
		ReceiptHash:v.Expected.AcknowledgedReceiptSHA256,
		EnvelopeHash:v.Expected.EnvelopeSHA256,Witness:*after.RuntimeWitness,
	}
	d,err:=DeriveRuntimeRecoveryEvidenceDigestsV1(obs)
	if err!=nil || d.MarkerSHA256!=v.Expected.MarkerSHA256 ||
		d.ReceiptSHA256!=v.Expected.AcknowledgedReceiptSHA256 ||
		d.EnvelopeSHA256!=v.Expected.EnvelopeSHA256 ||
		d.WitnessSHA256!=v.Expected.WitnessSHA256 ||
		d.TaskID!=after.TaskID || d.AgentID!=after.Binding.AgentID ||
		d.ServerID!=after.Binding.ServerID {
		t.Fatalf("Agent evidence digest format drift: %+v %v",d,err)
	}
	if d.MarkerSHA256==d.ReceiptSHA256 || d.ReceiptSHA256==d.WitnessSHA256 ||
		d.WitnessSHA256==after.RuntimeWitness.RuntimeHash {
		t.Fatal("distinct SHA256 evidence domains were conflated")
	}
}

func TestRuntimeRecoveryEvidenceV1ReadsOnlyAcknowledgedLockedFileBytes(t *testing.T) {
	e,task,a,binding:=recoveryFixture(t)
	manifest,err:=ObserveRuntimeRecoveryEvidenceV1(context.Background(),e.receiptDir,binding,task.ID,e.activePath,a)
	if err!=nil {t.Fatal(err)}
	markerPath:=filepath.Join(e.receiptDir,"mutation-inflight.json")
	receiptPath:=filepath.Join(e.receiptDir,"mutation-result.json")
	marker,err:=os.ReadFile(markerPath)
	if err!=nil{t.Fatal(err)}
	receiptBytes,err:=os.ReadFile(receiptPath)
	if err!=nil{t.Fatal(err)}
	var receipt runtimeResultReceipt
	if err:=json.Unmarshal(receiptBytes,&receipt);err!=nil||!receipt.Acknowledged||receipt.RuntimeWitness==nil{
		t.Fatal("file-byte hash requires acknowledged generation-linked receipt",err)
	}
	if manifest.MarkerSHA256!=markerDigest(marker) ||
		manifest.ReceiptSHA256!=markerDigest(receiptBytes) ||
		manifest.EnvelopeSHA256!=markerDigest(receipt.Envelope) {
		t.Fatal("Agent manifest was not derived from exact locked file/result bytes")
	}
	wh,err:=RuntimeRecoveryWitnessDigestV1(*receipt.RuntimeWitness)
	if err!=nil||manifest.WitnessSHA256!=wh{t.Fatal("saved witness digest differs from live observation",err)}
	if a.restarts!=1||a.validations!=0{t.Fatal("read-only evidence snapshot ran a mutation")}
	if lease,err:=BeginRuntimeMutation(e.receiptDir,task.ID,TaskKindVPNCoreService);err==nil{
		_ = lease.Close()
		t.Fatal("evidence read unlocked mutation admission")
	}
	markerAgain,_:=os.ReadFile(markerPath)
	receiptAgain,_:=os.ReadFile(receiptPath)
	if !bytes.Equal(marker,markerAgain)||!bytes.Equal(receiptBytes,receiptAgain){
		t.Fatal("read-only manifest changed durable state")
	}
}

func TestRuntimeRecoveryEvidenceV1RejectsUnacknowledgedOrMutatedState(t *testing.T){
	for _,mode:=range []string{"unacknowledged","stale_process","changed_config","busy_lock","cancelled"} {
		t.Run(mode,func(t *testing.T){
			e,task,a,binding:=recoveryFixture(t)
			ctx:=context.Background()
			switch mode{
			case "unacknowledged":
				path:=filepath.Join(e.receiptDir,"mutation-result.json")
				raw,err:=os.ReadFile(path)
				if err!=nil{t.Fatal(err)}
				var r runtimeResultReceipt
				if err=json.Unmarshal(raw,&r);err!=nil{t.Fatal(err)}
				r.Acknowledged=false
				changed,err:=json.Marshal(r)
				if err!=nil||os.WriteFile(path,changed,0600)!=nil{t.Fatal("failed to stage unacknowledged fixture",err)}
			case "stale_process":
				a.generation.InvocationID=strings.Repeat("c",32)
			case "changed_config":
				if err:=os.WriteFile(e.activePath,[]byte(`{"inbounds":[]}`),0600);err!=nil{t.Fatal(err)}
			case "busy_lock":
				lock,err:=lockRuntimeMutation(e.receiptDir)
				if err!=nil{t.Fatal(err)}
				defer lock.Close()
			case "cancelled":
				var cancel context.CancelFunc
				ctx,cancel=context.WithCancel(ctx)
				cancel()
			}
			proof,err:=ObserveRuntimeRecoveryEvidenceV1(ctx,e.receiptDir,binding,task.ID,e.activePath,a)
			if !errors.Is(err,ErrRuntimeMutationBlocked)||proof.SchemaVersion!=0{
				t.Fatalf("untrusted local bytes were treated as verified: %+v %v",proof,err)
			}
			if runtimeMarkerAbsent(filepath.Join(e.receiptDir,"mutation-inflight.json")){
				t.Fatal("failed provenance check removed the protective marker")
			}
		})
	}
}
