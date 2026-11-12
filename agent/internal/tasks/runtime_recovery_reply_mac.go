package tasks

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

const recoveryFullReplyDomainV1 = "routegate/rg140/agent-observation-reply/v1"

// RuntimeRecoveryFullReplyMACV1 separately authenticates the COMPLETE
// challenged observation, including observedAt, process generation and
// nonce, in addition to the old commitment-digest claim MAC. This remains
// a proof of possession of Agent's existing bearer, NOT host attestation.
//
// A compromised token holder can still sign a fabricated observation.
// Exact evidence matching and current token validation happen in Manager's
// atomic DB consumer; this function does NOT authorize unlock.
func RuntimeRecoveryFullReplyMACV1(
	token string, claim RuntimeRecoveryTokenBoundEvidenceV1,
	reply RuntimeRecoveryChallengedObservation,
) (string,error) {
	if !validRecoveryAgentToken(token) || !validRuntimeRecoveryTokenProofV1(claim) {
		return "",ErrRuntimeMutationBlocked
	}
	payload:=struct {
		Claim RuntimeRecoveryTokenBoundEvidenceV1 `json:"claim"`
		Reply RuntimeRecoveryChallengedObservation `json:"reply"`
	}{Claim:claim,Reply:reply}
	encoded,err:=json.Marshal(payload)
	if err!=nil{return "",ErrRuntimeMutationBlocked}
	mac:=hmac.New(sha256.New,[]byte(token))
	_,_=mac.Write([]byte(recoveryFullReplyDomainV1))
	_,_=mac.Write([]byte{0})
	_,_=mac.Write(encoded)
	return hex.EncodeToString(mac.Sum(nil)),nil
}
