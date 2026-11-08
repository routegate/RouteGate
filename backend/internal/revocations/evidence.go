package revocations

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

// EvidenceBinding comes from the durable, authenticated dispatch, never from
// browser input or the report itself. This verifier does not confirm an account
// or update applied membership; the future coordinator must do that atomically.
type EvidenceBinding struct {
	OperationID, JobID, AccountID, ServerID, AgentID string
	BaselineVersionID, ConfigVersionID, ConfigHash   string
	BaselineRuntimeHash, CandidateRuntimeHash        string
	ListenerPort                                     int
}

var evidenceID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func VerifySuccess(data []byte, b EvidenceBinding) (CredentialRemovalReport, error) {
	r, err := decodeRemovalEvidence(data, b)
	if err != nil {
		return CredentialRemovalReport{}, err
	}
	deny := func() (CredentialRemovalReport, error) { return CredentialRemovalReport{}, ErrCredentialRemoval }
	if r.State != "succeeded" || r.Code != "" || r.SessionEffect != "shared_process_restart_observed" ||
		r.ActiveRuntimeHash != b.CandidateRuntimeHash || r.ListenerPort != b.ListenerPort ||
		!validGeneration(r.Before) || !validGeneration(r.After) || r.Before.InvocationID == r.After.InvocationID {
		return deny()
	}
	return r, nil
}
func decodeRemovalEvidence(data []byte, b EvidenceBinding) (CredentialRemovalReport, error) {
	deny := func() (CredentialRemovalReport, error) { return CredentialRemovalReport{}, ErrCredentialRemoval }
	if len(data) > 16384 {
		return deny()
	}
	if _, _, _, err := canonicalRemovalJSON(data); err != nil {
		return deny()
	}
	var r CredentialRemovalReport
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&r) != nil {
		return deny()
	}
	for _, id := range []string{b.OperationID, b.JobID, b.AccountID, b.ServerID, b.AgentID, b.BaselineVersionID, b.ConfigVersionID} {
		if !evidenceID.MatchString(id) {
			return deny()
		}
	}
	for _, hash := range []string{b.ConfigHash, b.BaselineRuntimeHash, b.CandidateRuntimeHash} {
		if !validRemovalHash(hash) {
			return deny()
		}
	}
	if b.BaselineVersionID == b.ConfigVersionID || b.BaselineRuntimeHash == b.CandidateRuntimeHash || b.ListenerPort < 1 || b.ListenerPort > 65535 {
		return deny()
	}
	if r.SchemaVersion != 1 || r.JobID != b.JobID || r.OperationID != b.OperationID ||
		r.AccountID != b.AccountID || r.ServerID != b.ServerID || r.AgentID != b.AgentID ||
		r.BaselineVersionID != b.BaselineVersionID || r.ConfigVersionID != b.ConfigVersionID ||
		r.ConfigHash != b.ConfigHash || r.BaselineRuntimeHash != b.BaselineRuntimeHash {
		return deny()
	}
	return r, nil
}

// VerifyRecoveryRequired admits a bounded failure report, never success or
// permission to release a reservation. Process mutation may have taken place.
func VerifyRecoveryRequired(data []byte, b EvidenceBinding) (CredentialRemovalReport, error) {
	r, err := decodeRemovalEvidence(data, b)
	if err != nil {
		return CredentialRemovalReport{}, err
	}
	switch r.Code {
	case "interrupted_or_failed_mutation", "promotion_failed", "restart_failed", "restart_not_verified", "runtime_not_enabled",
		"listener_not_verified", "applied_evidence_changed", "receipt_write_failed", "fence_clear_failed":
	default:
		return CredentialRemovalReport{}, ErrCredentialRemoval
	}
	if r.State != "recovery_required" || !validGeneration(r.Before) ||
		(r.After != (RuntimeGeneration{}) && !validGeneration(r.After)) ||
		(r.ActiveRuntimeHash != "" && r.ActiveRuntimeHash != b.CandidateRuntimeHash) ||
		(r.ListenerPort != 0 && r.ListenerPort != b.ListenerPort) ||
		(r.SessionEffect != "" && (r.SessionEffect != "shared_process_restart_observed" ||
			r.ActiveRuntimeHash != b.CandidateRuntimeHash || r.ListenerPort != b.ListenerPort ||
			!validGeneration(r.After) || r.After.InvocationID == r.Before.InvocationID)) {
		return CredentialRemovalReport{}, ErrCredentialRemoval
	}
	return r, nil
}

func validGeneration(g RuntimeGeneration) bool {
	raw, err := hex.DecodeString(g.InvocationID)
	started, timeErr := strconv.ParseUint(g.StartedMonotonic, 10, 64)
	return err == nil && len(raw) == 16 && g.InvocationID == strings.ToLower(g.InvocationID) && g.InvocationID != strings.Repeat("0", 32) && g.PID > 0 && timeErr == nil && started > 0
}
