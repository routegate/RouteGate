package recoveryobserve

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// This shared fixture is the exact Agent-produced v1 marker byte contract.
// Its expected digest is checked against a Manager-only reconstruction from
// known job fields, never against an Agent-provided hash to derive truth.
func managerJobIntentFixtureV1(t *testing.T) (ManagerJobIntentV1, EvidenceDigestsV1, string) {
	t.Helper()
	v := loadEvidenceVectorV1(t)
	var marker struct {
		TaskID string `json:"taskId"`
		Kind   string `json:"kind"`
	}
	if err := json.Unmarshal(v.Marker, &marker); err != nil {
		t.Fatal(err)
	}
	var receipt struct {
		Binding struct {
			AgentID string `json:"agentId"`
			ServerID string `json:"serverId"`
		} `json:"binding"`
	}
	if err := json.Unmarshal(v.AcknowledgedReceipt, &receipt); err != nil {
		t.Fatal(err)
	}
	job := ManagerJobIntentV1{
		TaskID: marker.TaskID, AgentID: receipt.Binding.AgentID,
		ServerID: receipt.Binding.ServerID, Kind: marker.Kind,
		AgentCredentialGeneration: 4,
	}
	reported := EvidenceDigestsV1{
		SchemaVersion: 1, TaskID: job.TaskID, AgentID: job.AgentID,
		ServerID: job.ServerID, MarkerSHA256: v.Expected.MarkerSHA256,
		ReceiptSHA256: v.Expected.AcknowledgedReceiptSHA256,
		EnvelopeSHA256: v.Expected.EnvelopeSHA256,
		WitnessSHA256: v.Expected.WitnessSHA256,
	}
	return job, reported, v.Expected.MarkerSHA256
}

func TestManagerExpectedMutationMarkerV1MatchesAgentGoldenBytes(t *testing.T) {
	job, reported, golden := managerJobIntentFixtureV1(t)
	got, err := ManagerExpectedMutationMarkerV1(job)
	if err != nil || got != golden {
		t.Fatalf("Manager did not independently reproduce Agent marker: %q expected %q, err=%v", got, golden, err)
	}
	comparison, err := CompareManagerExpectedMutationMarkerV1(job, reported)
	if err != nil || comparison.ExpectedMarkerSHA256 != golden ||
		comparison.State != managerMarkerConsistentUnattestedV1 {
		t.Fatalf("legitimate marker comparison must still be UNATTESTED: %+v %v", comparison, err)
	}

	// A different but permitted task kind produces a different exact marker.
	job.Kind = "config_apply"
	other, err := ManagerExpectedMutationMarkerV1(job)
	if err != nil || other == golden {
		t.Fatal("Manager reused marker across mutation kinds", err)
	}
	raw := []byte(`{"schemaVersion":1,"taskId":"44444444-4444-4444-8444-444444444444","kind":"config_apply"}`)
	sum := sha256.Sum256(raw)
	if other != hex.EncodeToString(sum[:]) {
		t.Fatal("config_apply marker encoder diverged from Agent")
	}
}

func TestManagerExpectedMutationMarkerV1RejectsInvalidManagerJob(t *testing.T) {
	job, reported, _ := managerJobIntentFixtureV1(t)
	mutations := map[string]func(*ManagerJobIntentV1){
		"missing_task": func(j *ManagerJobIntentV1) { j.TaskID = "" },
		"noncanonical_task": func(j *ManagerJobIntentV1) { j.TaskID = strings.ToUpper(j.TaskID) },
		"other_agent": func(j *ManagerJobIntentV1) { j.AgentID = "" },
		"other_server": func(j *ManagerJobIntentV1) { j.ServerID = "" },
		"missing_generation": func(j *ManagerJobIntentV1) { j.AgentCredentialGeneration = 0 },
		"negative_generation": func(j *ManagerJobIntentV1) { j.AgentCredentialGeneration = -1 },
		"missing_kind": func(j *ManagerJobIntentV1) { j.Kind = "" },
		"unsupported_kind": func(j *ManagerJobIntentV1) { j.Kind = "platform_update" },
	}
	for name, change := range mutations {
		t.Run(name, func(t *testing.T) {
			bad := job
			change(&bad)
			if digest, err := ManagerExpectedMutationMarkerV1(bad); digest != "" || !errors.Is(err, ErrObservationRefused) {
				t.Fatalf("invalid manager job encoded: %q %v", digest, err)
			}
			if proof, err := CompareManagerExpectedMutationMarkerV1(bad, reported); proof != (ManagerMarkerComparisonV1{}) || !errors.Is(err, ErrObservationRefused) {
				t.Fatalf("invalid manager job granted evidence: %+v %v", proof, err)
			}
		})
	}
}

func TestManagerExpectedMutationMarkerV1RefusesAgentClaimDrift(t *testing.T) {
	job, reported, _ := managerJobIntentFixtureV1(t)
	changes := map[string]func(*EvidenceDigestsV1){
		"wrong_task": func(e *EvidenceDigestsV1) { e.TaskID = job.AgentID },
		"wrong_agent": func(e *EvidenceDigestsV1) { e.AgentID = job.ServerID },
		"wrong_server": func(e *EvidenceDigestsV1) { e.ServerID = job.AgentID },
		"wrong_marker": func(e *EvidenceDigestsV1) { e.MarkerSHA256 = strings.Repeat("0", 64) },
		"pre_ack_receipt": func(e *EvidenceDigestsV1) { e.ReceiptSHA256 = "" },
		"schema_changed": func(e *EvidenceDigestsV1) { e.SchemaVersion++ },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			candidate := reported
			change(&candidate)
			matched, err := CompareManagerExpectedMutationMarkerV1(job, candidate)
			// This helper only compares the marker and identity. A well-formed
			// but substituted receipt hash is NOT tested here: it needs its
			// own independent historical checkpoint, not a forged promise.
			if !errors.Is(err, ErrObservationRefused) || matched != (ManagerMarkerComparisonV1{}) {
				t.Fatalf("untrusted digest admitted: %+v %v", matched, err)
			}
		})
	}
	// Same identity and valid digest in a different job kind must fail.
	job.Kind = "config_apply"
	if matched, err := CompareManagerExpectedMutationMarkerV1(job, reported); !errors.Is(err, ErrObservationRefused) || matched != (ManagerMarkerComparisonV1{}) {
		t.Fatal("claim from a different job type was matched", matched, err)
	}
}

func TestManagerExpectedMutationMarkerV1ConsistentForgedEvidenceStillUnattested(t *testing.T) {
	job, reported, _ := managerJobIntentFixtureV1(t)
	// A holder of a stolen Agent bearer can copy the deterministic marker
	// while fabricating any syntactically valid receipt, envelope or witness.
	// This comparison MUST NEVER grant release or state attestation.
	reported.ReceiptSHA256 = strings.Repeat("a", 64)
	reported.EnvelopeSHA256 = strings.Repeat("b", 64)
	reported.WitnessSHA256 = strings.Repeat("c", 64)
	comparison, err := CompareManagerExpectedMutationMarkerV1(job, reported)
	if err != nil || comparison.State != managerMarkerConsistentUnattestedV1 {
		t.Fatalf("marker was confused with full provenance: %+v %v", comparison, err)
	}
}
