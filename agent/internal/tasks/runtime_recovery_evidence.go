package tasks

import (
	"context"
	"encoding/json"
)

// RuntimeRecoveryEvidenceDigestsV1 is a format contract, not authorization.
// The marker and receipt hashes refer to the EXACT bytes read under Agent's
// exclusive local runtime lock. The receipt hash is specifically from the
// acknowledged persisted file (acknowledgement rewrites its bytes).
// The witness hash is SHA-256(json.Marshal(RuntimeRecoveryWitness)), with
// explicit schema/order of Agent's v1 JSON fields; it is NOT the runtime
// configuration hash, raw receipt hash or Manager's opaque digest claim.
type RuntimeRecoveryEvidenceDigestsV1 struct {
	SchemaVersion int    `json:"schemaVersion"`
	TaskID        string `json:"taskId"`
	AgentID       string `json:"agentId"`
	ServerID      string `json:"serverId"`
	MarkerSHA256  string `json:"markerSha256"`
	ReceiptSHA256 string `json:"receiptSha256"`
	EnvelopeSHA256 string `json:"envelopeSha256"`
	WitnessSHA256 string `json:"witnessSha256"`
}

// RuntimeRecoveryWitnessDigestV1 is only a versioned JSON digest; it does not
// authenticate the witness, prove current process identity or release a fence.
func RuntimeRecoveryWitnessDigestV1(w RuntimeRecoveryWitness) (string, error) {
	if !validRuntimeWitness(w) {
		return "", ErrRuntimeMutationBlocked
	}
	raw, err := json.Marshal(w)
	if err != nil {
		return "", ErrRuntimeMutationBlocked
	}
	return markerDigest(raw), nil
}

// DeriveRuntimeRecoveryEvidenceDigestsV1 normalizes an existing observation.
// It does NOT check the provenance of a caller-supplied observation. Trusted
// consumers must obtain it from ObserveRuntimeRecoveryEvidenceV1 below.
func DeriveRuntimeRecoveryEvidenceDigestsV1(o RuntimeRecoveryObservation) (RuntimeRecoveryEvidenceDigestsV1, error) {
	if o.SchemaVersion != 1 ||
		!canonicalTaskIDPattern.MatchString(o.TaskID) ||
		!canonicalTaskIDPattern.MatchString(o.AgentID) ||
		!canonicalTaskIDPattern.MatchString(o.ServerID) ||
		!validRemovalHash(o.MarkerHash) ||
		!validRemovalHash(o.ReceiptHash) ||
		!validRemovalHash(o.EnvelopeHash) {
		return RuntimeRecoveryEvidenceDigestsV1{}, ErrRuntimeMutationBlocked
	}
	witnessHash, err := RuntimeRecoveryWitnessDigestV1(o.Witness)
	if err != nil {
		return RuntimeRecoveryEvidenceDigestsV1{}, ErrRuntimeMutationBlocked
	}
	return RuntimeRecoveryEvidenceDigestsV1{
		SchemaVersion:1,TaskID:o.TaskID,AgentID:o.AgentID,ServerID:o.ServerID,
		MarkerSHA256:o.MarkerHash,ReceiptSHA256:o.ReceiptHash,
		EnvelopeSHA256:o.EnvelopeHash,WitnessSHA256:witnessHash,
	},nil
}

// ObserveRuntimeRecoveryEvidenceV1 reuses the existing read-only observation,
// which validates the acknowledged durable receipt and live sing-box process
// while holding the SAME exclusive lock as runtime mutations. The result is
// still unsigned and is NOT transmitted or trusted by Manager at this stage.
func ObserveRuntimeRecoveryEvidenceV1(ctx context.Context, dir string, binding RuntimeResultBinding, taskID, activePath string, adapter VPNCoreAdapter) (RuntimeRecoveryEvidenceDigestsV1,error) {
	o,err:=ObserveRuntimeRecovery(ctx,dir,binding,taskID,activePath,adapter)
	if err!=nil {
		return RuntimeRecoveryEvidenceDigestsV1{},ErrRuntimeMutationBlocked
	}
	return DeriveRuntimeRecoveryEvidenceDigestsV1(o)
}
