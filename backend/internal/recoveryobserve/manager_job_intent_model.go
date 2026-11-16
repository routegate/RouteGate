package recoveryobserve

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
)

// ManagerJobIntentV1 is a snapshot of a Manager-side job identity. A future
// caller must source ALL fields from a locked, Manager-persisted job and Agent
// generation, before dispatch. Never construct it from an Agent HTTP claim.
// The type alone does not establish that provenance.
type ManagerJobIntentV1 struct {
	TaskID                    string
	AgentID                   string
	ServerID                  string
	Kind                      string
	AgentCredentialGeneration int64
}

// ManagerMarkerComparisonV1 is deliberately NOT a checkpoint enrollment,
// authorization, host attestation, proof of delivery or recovery clearance.
// The sole comparison says a presented marker digest matches bytes that a
// correctly implemented Agent would use for this Manager job.
type ManagerMarkerComparisonV1 struct {
	ExpectedMarkerSHA256 string
	State                string // only "marker_consistent_unattested"
}

const managerMarkerConsistentUnattestedV1 = "marker_consistent_unattested"

// ManagerExpectedMutationMarkerV1 derives the EXACT canonical marker JSON
// bytes independently of Agent-supplied marker/receipt/witness hashes.
// Keep the field layout/order aligned with Agent's BeginRuntimeMutation.
// It does NOT establish that the marker exists on the remote filesystem.
func ManagerExpectedMutationMarkerV1(job ManagerJobIntentV1) (string, error) {
	if !canonicalUUID.MatchString(job.TaskID) ||
		!canonicalUUID.MatchString(job.AgentID) ||
		!canonicalUUID.MatchString(job.ServerID) ||
		job.AgentCredentialGeneration <= 0 ||
		(job.Kind != "config_apply" && job.Kind != "vpn_core_service") {
		return "", ErrObservationRefused
	}
	marker, err := json.Marshal(struct {
		SchemaVersion int    `json:"schemaVersion"`
		TaskID        string `json:"taskId"`
		Kind          string `json:"kind"`
	}{SchemaVersion: 1, TaskID: job.TaskID, Kind: job.Kind})
	if err != nil {
		return "", ErrObservationRefused
	}
	sum := sha256.Sum256(marker)
	return hex.EncodeToString(sum[:]), nil
}

// CompareManagerExpectedMutationMarkerV1 rejects mismatched task/Agent/server
// identity and locally recomputes the expected marker digest. A match is
// STILL UNATTESTED. Neither a forged Agent response nor a stolen bearer can
// transform this function into an unlock decision.
//
// The caller MUST separately prove job fields came from Manager's durable
// pre-dispatch state. Current RG-140 reservations are NOT such a checkpoint;
// their evidence SHA-256 values may originate from an Agent holder's claim.
func CompareManagerExpectedMutationMarkerV1(job ManagerJobIntentV1, reported EvidenceDigestsV1) (ManagerMarkerComparisonV1, error) {
	deny := func() (ManagerMarkerComparisonV1, error) {
		return ManagerMarkerComparisonV1{}, ErrObservationRefused
	}
	if !validObservationIssueClaim(reported) ||
		reported.TaskID != job.TaskID ||
		reported.AgentID != job.AgentID ||
		reported.ServerID != job.ServerID {
		return deny()
	}
	expected, err := ManagerExpectedMutationMarkerV1(job)
	if err != nil {
		return deny()
	}
	if subtle.ConstantTimeCompare([]byte(expected), []byte(reported.MarkerSHA256)) != 1 {
		return deny()
	}
	return ManagerMarkerComparisonV1{
		ExpectedMarkerSHA256: expected,
		State:                managerMarkerConsistentUnattestedV1,
	}, nil
}
