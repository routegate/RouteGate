package recoveryobserve

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
)

// EvidenceDigestsV1 defines the Agent/Manager SHA-256 wire contract.
// Every value is a DOMAIN-SPECIFIC byte digest:
//   marker: raw mutation-inflight.json bytes
//   receipt: raw ACKNOWLEDGED mutation-result.json bytes
//   envelope: raw success-envelope JSON bytes from inside receipt
//   witness: json.Marshal(v1 RuntimeRecoveryWitness)
//
// This format validator does not authenticate an Agent. Until a trusted
// source binds these hashes to a durable reservation, they remain claims.
type EvidenceDigestsV1 struct {
	SchemaVersion int    `json:"schemaVersion"`
	TaskID string         `json:"taskId"`
	AgentID string        `json:"agentId"`
	ServerID string       `json:"serverId"`
	MarkerSHA256 string   `json:"markerSha256"`
	ReceiptSHA256 string  `json:"receiptSha256"`
	EnvelopeSHA256 string `json:"envelopeSha256"`
	WitnessSHA256 string  `json:"witnessSha256"`
}

// RuntimeRecoveryWitnessDigestV1 encodes precisely the Agent v1 JSON witness
// field names and their order. This must remain in sync with the Agent test
// vector. A digest from Manager's rendered config is NOT a substitute.
func RuntimeRecoveryWitnessDigestV1(w RuntimeWitness) (string,error) {
	if w.SchemaVersion != 1 ||
		!canonicalHash.MatchString(w.RuntimeHash) ||
		!validGeneration(w.Generation) || !validGeneration(w.PreviousGeneration) ||
		w.Generation.InvocationID==w.PreviousGeneration.InvocationID ||
		w.ListenerPort<1 || w.ListenerPort>65535 {
		return "",ErrObservationRefused
	}
	encoded,err:=json.Marshal(w)
	if err!=nil{return "",ErrObservationRefused}
	sum:=sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]),nil
}

// DeriveEvidenceDigestsV1 is pure formatting/consistency validation. It
// deliberately does NOT trust an observation supplied by a remote caller.
func DeriveEvidenceDigestsV1(o RuntimeObservation) (EvidenceDigestsV1,error) {
	if o.SchemaVersion!=1 || !canonicalUUID.MatchString(o.TaskID) ||
		!canonicalUUID.MatchString(o.AgentID) || !canonicalUUID.MatchString(o.ServerID) ||
		!canonicalHash.MatchString(o.MarkerHash) ||
		!canonicalHash.MatchString(o.ReceiptHash) ||
		!canonicalHash.MatchString(o.EnvelopeHash) {
		return EvidenceDigestsV1{},ErrObservationRefused
	}
	witnessHash,err:=RuntimeRecoveryWitnessDigestV1(o.Witness)
	if err!=nil{return EvidenceDigestsV1{},ErrObservationRefused}
	return EvidenceDigestsV1{
		SchemaVersion:1,TaskID:o.TaskID,AgentID:o.AgentID,ServerID:o.ServerID,
		MarkerSHA256:o.MarkerHash,ReceiptSHA256:o.ReceiptHash,
		EnvelopeSHA256:o.EnvelopeHash,WitnessSHA256:witnessHash,
	},nil
}

// MatchesEvidenceDigestsV1 compares claims to the structure of ONE observed
// response in constant time for digest fields. This is necessary for format
// compatibility, but cannot attest that marker/receipt bytes really existed
// on a remote Agent without authenticated transport and source provenance.
func MatchesEvidenceDigestsV1(expected EvidenceDigestsV1,observed RuntimeObservation) bool {
	if expected.SchemaVersion!=1 || !canonicalUUID.MatchString(expected.TaskID) ||
		!canonicalUUID.MatchString(expected.AgentID) ||
		!canonicalUUID.MatchString(expected.ServerID) ||
		!canonicalHash.MatchString(expected.MarkerSHA256) ||
		!canonicalHash.MatchString(expected.ReceiptSHA256) ||
		!canonicalHash.MatchString(expected.EnvelopeSHA256) ||
		!canonicalHash.MatchString(expected.WitnessSHA256){
		return false
	}
	actual,err:=DeriveEvidenceDigestsV1(observed)
	if err!=nil || actual.SchemaVersion!=expected.SchemaVersion ||
		actual.TaskID!=expected.TaskID || actual.AgentID!=expected.AgentID ||
		actual.ServerID!=expected.ServerID {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(actual.MarkerSHA256),[]byte(expected.MarkerSHA256))==1 &&
		subtle.ConstantTimeCompare([]byte(actual.ReceiptSHA256),[]byte(expected.ReceiptSHA256))==1 &&
		subtle.ConstantTimeCompare([]byte(actual.EnvelopeSHA256),[]byte(expected.EnvelopeSHA256))==1 &&
		subtle.ConstantTimeCompare([]byte(actual.WitnessSHA256),[]byte(expected.WitnessSHA256))==1
}
