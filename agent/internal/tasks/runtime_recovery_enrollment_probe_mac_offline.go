package tasks

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
)

// OFFLINE, package-private reference implementation only. No Agent Runner,
// installer, task claim or mutation gate invokes this function. The token
// holder can fabricate an arbitrary reply: HMAC is NOT host attestation.
const recoveryEnrollmentProbeReplyDomainV1 = "routegate/rg140/enrollment-probe-reply/v1"

type recoveryEnrollmentProbeReplyV1 struct {
	SchemaVersion             int    `json:"schemaVersion"`
	AttemptID                 string `json:"attemptId"`
	ManagerPreparationID      string `json:"managerPreparationId"`
	AgentID                   string `json:"agentId"`
	ServerID                  string `json:"serverId"`
	AgentCredentialGeneration int64  `json:"agentCredentialGeneration"`
	Nonce                     string `json:"nonce"`
	LocalGenesisSHA256        string `json:"localGenesisSHA256"`
}

func validRecoveryEnrollmentProbeReplyV1(v recoveryEnrollmentProbeReplyV1) bool {
	if v.SchemaVersion != 1 ||
		!canonicalTaskIDPattern.MatchString(v.AttemptID) ||
		!canonicalTaskIDPattern.MatchString(v.ManagerPreparationID) ||
		!canonicalTaskIDPattern.MatchString(v.AgentID) ||
		!canonicalTaskIDPattern.MatchString(v.ServerID) ||
		v.AgentCredentialGeneration <= 0 ||
		!validRemovalHash(v.LocalGenesisSHA256) {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(v.Nonce)
	return err == nil && len(raw) == 32 &&
		base64.RawURLEncoding.EncodeToString(raw) == v.Nonce
}

// This helper does NOT read either local anchor or authenticate a Manager
// command. A future Agent path must acquire the actual runtime.lock then
// journal lock, inspect BOTH on-disk anchors, and ONLY then construct and
// sign a response to a separately authenticated one-time Manager nonce.
func recoveryEnrollmentProbeReplyMACOfflineV1(token string, reply recoveryEnrollmentProbeReplyV1) (string, error) {
	if !validRecoveryAgentToken(token) || !validRecoveryEnrollmentProbeReplyV1(reply) {
		return "", ErrRuntimeMutationBlocked
	}
	encoded, err := json.Marshal(reply)
	if err != nil {
		return "", ErrRuntimeMutationBlocked
	}
	mac := hmac.New(sha256.New, []byte(token))
	_, _ = mac.Write([]byte(recoveryEnrollmentProbeReplyDomainV1))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write(encoded)
	return hex.EncodeToString(mac.Sum(nil)), nil
}
