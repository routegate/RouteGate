package tasks

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
)

const recoveryEvidenceProofDomainV1 = "routegate/rg140/agent-token-bound-evidence/v1"

// RuntimeRecoveryTokenBoundEvidenceV1 is an internal read-only proof-of-token
// possession. A holder of the Agent bearer token can fabricate this message;
// it is NOT proof of the live process or a recovery/unlock instruction.
type RuntimeRecoveryTokenBoundEvidenceV1 struct {
	SchemaVersion             int                              `json:"schemaVersion"`
	ObservationID             string                           `json:"observationId"`
	ReservationID             string                           `json:"reservationId"`
	AttemptID                 string                           `json:"attemptId"`
	AttemptEpoch              int64                            `json:"attemptEpoch"`
	Nonce                     string                           `json:"nonce"`
	AgentCredentialGeneration int64                            `json:"agentCredentialGeneration"`
	Evidence                  RuntimeRecoveryEvidenceDigestsV1 `json:"evidence"`
}

func validRecoveryAgentToken(token string) bool {
	if !strings.HasPrefix(token, "rg_agent_") || len(token) != len("rg_agent_")+43 {
		return false
	}
	suffix := strings.TrimPrefix(token, "rg_agent_")
	raw, err := base64.RawURLEncoding.DecodeString(suffix)
	return err == nil && len(raw) == 32 && base64.RawURLEncoding.EncodeToString(raw) == suffix
}

func validRuntimeRecoveryTokenProofV1(p RuntimeRecoveryTokenBoundEvidenceV1) bool {
	e := p.Evidence
	if p.SchemaVersion != 1 || e.SchemaVersion != 1 ||
		!canonicalTaskIDPattern.MatchString(p.ObservationID) ||
		!canonicalTaskIDPattern.MatchString(p.ReservationID) ||
		!canonicalTaskIDPattern.MatchString(p.AttemptID) ||
		!canonicalTaskIDPattern.MatchString(e.TaskID) ||
		!canonicalTaskIDPattern.MatchString(e.AgentID) ||
		!canonicalTaskIDPattern.MatchString(e.ServerID) ||
		p.AttemptEpoch <= 0 || p.AgentCredentialGeneration <= 0 ||
		!validRemovalHash(e.MarkerSHA256) ||
		!validRemovalHash(e.ReceiptSHA256) ||
		!validRemovalHash(e.EnvelopeSHA256) ||
		!validRemovalHash(e.WitnessSHA256) {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(p.Nonce)
	return err == nil && len(raw) == 32 &&
		base64.RawURLEncoding.EncodeToString(raw) == p.Nonce
}

// RuntimeRecoveryTokenProofMACV1 signs exact v1 JSON field names/order with
// the Agent's EXISTING bearer secret. This adds request-integrity binding,
// not a new credential or an independent signing identity.
//
// Callers must first obtain Evidence from the read-only, lock-bracketed
// ObserveRuntimeRecoveryEvidenceV1; never sign a free-form caller-supplied
// digest as if it were local observation. Never log or persist the secret.
// Manager must recheck the current token hash, Agent generation, nonce and
// held attempt in PostgreSQL. No current network-facing caller exists.
func RuntimeRecoveryTokenProofMACV1(token string, p RuntimeRecoveryTokenBoundEvidenceV1) (string, error) {
	if !validRecoveryAgentToken(token) || !validRuntimeRecoveryTokenProofV1(p) {
		return "", ErrRuntimeMutationBlocked
	}
	encoded, err := json.Marshal(p)
	if err != nil {
		return "", ErrRuntimeMutationBlocked
	}
	mac := hmac.New(sha256.New, []byte(token))
	_, _ = mac.Write([]byte(recoveryEvidenceProofDomainV1))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write(encoded)
	return hex.EncodeToString(mac.Sum(nil)), nil
}
