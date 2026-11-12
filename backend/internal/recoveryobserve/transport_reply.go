package recoveryobserve

import (
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
	decoder:=json.NewDecoder(http.MaxBytesReader(w,r.Body,16384))
	decoder.DisallowUnknownFields()
	var request observationReplyRequest
	if err:=decoder.Decode(&request);err!=nil {
		observationHTTPError(w,http.StatusBadRequest);return
	}
	var extra any
	if err:=decoder.Decode(&extra);!errors.Is(err,io.EOF) {
		observationHTTPError(w,http.StatusBadRequest);return
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
