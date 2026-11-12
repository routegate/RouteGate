package recoveryobserve

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

const recoveryFullReplyDomainV1 = "routegate/rg140/agent-observation-reply/v1"

// FullReplyMACV1 mirrors Agent's independent HMAC over the ENTIRE response,
// including time and generation fields not directly covered by the digest
// claim. Manager still checks current bearer, immutable attempt and nonce in
// its single PostgreSQL transaction before recording a one-use audit.
func FullReplyMACV1(token string,claim TokenBoundEvidenceV1,reply ObservationReply)(string,error){
	if !validAgentBearerV1(token)||!validTokenBoundEvidenceV1(claim){
		return "",ErrTokenBoundEvidenceRefused
	}
	payload:=struct {
		Claim TokenBoundEvidenceV1 `json:"claim"`
		Reply ObservationReply `json:"reply"`
	}{Claim:claim,Reply:reply}
	encoded,err:=json.Marshal(payload)
	if err!=nil{return "",ErrTokenBoundEvidenceRefused}
	mac:=hmac.New(sha256.New,[]byte(token))
	_,_=mac.Write([]byte(recoveryFullReplyDomainV1))
	_,_=mac.Write([]byte{0})
	_,_=mac.Write(encoded)
	return hex.EncodeToString(mac.Sum(nil)),nil
}
