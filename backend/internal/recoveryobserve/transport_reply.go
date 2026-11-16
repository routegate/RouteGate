package recoveryobserve

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

// observationReplyRequest deliberately contains NO Agent bearer token or
// caller-selected Agent identity. Token comes ONLY from Authorization header;
// its current DB identity is rechecked inside the atomic consumer.
type observationReplyRequest struct {
	Claim TokenBoundEvidenceV1 `json:"claim"`
	MAC string `json:"mac"`
	ReplyMAC string `json:"replyMac"`
	Reply ObservationReply `json:"reply"`
}

type observationReplyReceipt struct {
	State string `json:"state"`
}

// Reply is a deliberately UNREGISTERED, test-only Agent outbound HTTP
// adapter. It never calls Verifier.RecordOneReply (which accepts a
// caller-supplied identity), never dispatches a VPN task, never revokes
// credentials, and never removes Agent's local mutation-inflight marker.
//
// It invokes #758's single-transaction current-bearer+nonce consumer. Even
// HTTP 202 signifies ONLY 'reply_matched_unattested', NOT host attestation,
// operator authorization, or permission to unlock. Never route this method
// into the production Manager without separate security acceptance.
func (h *ObservationTransport) Reply(w http.ResponseWriter,r *http.Request) {
	noStoreObservation(w)
	if h==nil||h.pool==nil||r.Method!=http.MethodPost {
		observationHTTPError(w,http.StatusNotFound);return
	}
	token,ok:=strictObservationBearer(r.Header.Get("Authorization"))
	if !ok {observationHTTPError(w,http.StatusUnauthorized);return}
	// Reject duplicate and case-aliased JSON field names before checking MACs
	// or entering the atomic nonce consumer. encoding/json otherwise accepts
	// last-write-wins ambiguity in even deeply nested signed structures.
	raw,err:=io.ReadAll(http.MaxBytesReader(w,r.Body,16384))
	if err!=nil || rejectAmbiguousObservationJSON(raw)!=nil {
		observationHTTPError(w,http.StatusBadRequest);return
	}
	decoder:=json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var request observationReplyRequest
	if err:=decoder.Decode(&request);err!=nil {
		observationHTTPError(w,http.StatusBadRequest);return
	}
	var extra any
	if err:=decoder.Decode(&extra);!errors.Is(err,io.EOF) {
		observationHTTPError(w,http.StatusBadRequest);return
	}
	// The original claim HMAC binds digests; a second domain-separated HMAC
	// covers the FULL reply, including observedAt and runtime generation.
	// Bad MACs are rejected before any DB state is consumed.
	expectedReplyMAC,err:=FullReplyMACV1(token,request.Claim,request.Reply)
	if err!=nil || !canonicalHash.MatchString(request.ReplyMAC) ||
		subtle.ConstantTimeCompare([]byte(expectedReplyMAC),[]byte(request.ReplyMAC))!=1 {
		observationHTTPError(w,http.StatusConflict);return
	}
	// The DB transaction locks the nonce+Agent, checks current token hash,
	// generation, reservation, signed digests and reply before committing
	// the one-use response hash and append-only audit.
	receipt,err:=NewAuthenticatedReplyConsumer(h.pool).ConsumeTokenBoundReplyV1(
		r.Context(),token,request.MAC,request.Claim,request.Reply,
	)
	if err!=nil {
		if errors.Is(err,ErrTokenBoundEvidenceRefused) {
			observationHTTPError(w,http.StatusConflict)
		}else{
			observationHTTPError(w,http.StatusServiceUnavailable)
		}
		return
	}
	if receipt.State!="reply_matched_unattested" {
		// Defensive check: never expose a new status as runtime approval.
		observationHTTPError(w,http.StatusServiceUnavailable);return
	}
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(observationReplyReceipt{
		State:"reply_matched_unattested",
	})
}
