package recoveryenroll

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
)

// OFFLINE CONTRACT ONLY. No HTTP, dispatcher, Manager challenge consumer,
// Agent Runner or installer calls this. A matching HMAC demonstrates only
// possession of the EXISTING Agent bearer, NOT real host attestation.
const enrollmentProbeReplyDomainV1 = "routegate/rg140/enrollment-probe-reply/v1"

var ErrEnrollmentProbeReplyRefusedV1 = errors.New("offline enrollment probe reply refused")

var enrollmentProbeHashV1 = regexp.MustCompile(`^[0-9a-f]{64}$`)

type EnrollmentProbeReplyV1 struct {
	SchemaVersion             int    `json:"schemaVersion"`
	AttemptID                 string `json:"attemptId"`
	ManagerPreparationID      string `json:"managerPreparationId"`
	AgentID                   string `json:"agentId"`
	ServerID                  string `json:"serverId"`
	AgentCredentialGeneration int64  `json:"agentCredentialGeneration"`
	Nonce                     string `json:"nonce"`
	LocalGenesisSHA256        string `json:"localGenesisSHA256"`
}

func validEnrollmentProbeTokenV1(token string) bool {
	if !strings.HasPrefix(token, "rg_agent_") ||
		len(token) != len("rg_agent_")+43 {
		return false
	}
	encoded := strings.TrimPrefix(token, "rg_agent_")
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	return err == nil && len(raw) == 32 &&
		base64.RawURLEncoding.EncodeToString(raw) == encoded
}

func validEnrollmentProbeReplyV1(v EnrollmentProbeReplyV1) bool {
	if v.SchemaVersion != 1 ||
		!enrollmentCanonicalUUID.MatchString(v.AttemptID) ||
		!enrollmentCanonicalUUID.MatchString(v.ManagerPreparationID) ||
		!enrollmentCanonicalUUID.MatchString(v.AgentID) ||
		!enrollmentCanonicalUUID.MatchString(v.ServerID) ||
		v.AgentCredentialGeneration <= 0 ||
		!enrollmentProbeHashV1.MatchString(v.LocalGenesisSHA256) {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(v.Nonce)
	return err == nil && len(raw) == 32 &&
		base64.RawURLEncoding.EncodeToString(raw) == v.Nonce
}

// EnrollmentProbeReplyMACOfflineV1 authenticates EXACT JSON bytes of a
// test-only response including attempt, preparation, Agent, server,
// credential generation, nonce and claimed LOCAL genesis digest.
//
// On a future Manager consumer, validation of THIS MAC MUST happen inside
// the same protected, one-use transaction as fresh Agent identity/token
// lookup, nonce check and audit commit. A caller-supplied token/tuple is
// not trusted by this function, and digest equality cannot grant enrollment.
func EnrollmentProbeReplyMACOfflineV1(token string, reply EnrollmentProbeReplyV1) (string, error) {
	if !validEnrollmentProbeTokenV1(token) || !validEnrollmentProbeReplyV1(reply) {
		return "", ErrEnrollmentProbeReplyRefusedV1
	}
	encoded, err := json.Marshal(reply)
	if err != nil {
		return "", ErrEnrollmentProbeReplyRefusedV1
	}
	mac := hmac.New(sha256.New, []byte(token))
	_, _ = mac.Write([]byte(enrollmentProbeReplyDomainV1))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write(encoded)
	return hex.EncodeToString(mac.Sum(nil)), nil
}

// VerifyEnrollmentProbeReplyMACOfflineV1 is ONLY a pure wire verifier.
// It does not look up the Agent's CURRENT token, preparation, nonce, deadline
// or attempt state. The caller must validate all those Manager-owned values
// and consume a one-use attempt inside a separately reviewed DB transaction.
func VerifyEnrollmentProbeReplyMACOfflineV1(token string, reply EnrollmentProbeReplyV1, claimedMAC string) bool {
	if !enrollmentProbeHashV1.MatchString(claimedMAC) {
		return false
	}
	expected, err := EnrollmentProbeReplyMACOfflineV1(token, reply)
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(expected), []byte(claimedMAC)) == 1
}
