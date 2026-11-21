package client

import (
	"context"
	"time"

	"github.com/ikaevus/routegate/agent/internal/tasks"
)

// observationReplySubmission is a versioned JSON envelope. It intentionally
// omits the Agent bearer token, which is sent only in Authorization header.
// Its MAC covers the same exact claim JSON that Manager's private atomic
// nonce consumer validates against the still-current Agent credential.
type observationReplySubmission struct {
	Claim tasks.RuntimeRecoveryTokenBoundEvidenceV1 `json:"claim"`
	MAC string `json:"mac"`
	ReplyMAC string `json:"replyMac"`
	Reply tasks.RuntimeRecoveryChallengedObservation `json:"reply"`
}

type observationReplyResult struct {
	State string `json:"state"`
}

// SubmitRecoveryObservationReplyV1 sends a PREEXISTING, read-only challenged
// observation to Manager once using the normal Agent outbound bearer channel.
// It does NOT run an observation, acquire/release a local lock, dispatch a
// task, restart sing-box, retry HTTP, clear mutation-inflight.json or claim
// credentials were revoked. It is intentionally NOT used by Agent Runner.
//
// A network error/response loss may occur AFTER Manager consumes the nonce.
// Never retry automatically: the next request is a replay and must be denied.
// An HTTP 202 + reply_matched_unattested proves only a durable response match,
// not a trusted host/OS/process or authorization to resume mutations.
func (c *Client) SubmitRecoveryObservationReplyV1(
	ctx context.Context, token string,
	challenge tasks.RuntimeRecoveryObservationChallenge,
	observed tasks.RuntimeRecoveryChallengedObservation,
) error {
	if c==nil || ctx.Err()!=nil || token=="" || challenge.SchemaVersion!=1 ||
		!recoveryObservationUUID.MatchString(challenge.ObservationID) ||
		!recoveryObservationUUID.MatchString(challenge.ReservationID) ||
		!recoveryObservationUUID.MatchString(challenge.AttemptID) ||
		!recoveryObservationUUID.MatchString(challenge.TaskID) ||
		challenge.AttemptEpoch<=0 || challenge.AgentCredentialGeneration<=0 ||
		challenge.Binding.ManagerURL!=c.managerURL ||
		!recoveryObservationUUID.MatchString(challenge.Binding.AgentID) ||
		!recoveryObservationUUID.MatchString(challenge.Binding.ServerID) ||
		observed.SchemaVersion!=1 ||
		observed.ObservationID!=challenge.ObservationID ||
		observed.ReservationID!=challenge.ReservationID ||
		observed.AttemptID!=challenge.AttemptID ||
		observed.AttemptEpoch!=challenge.AttemptEpoch ||
		observed.Nonce!=challenge.Nonce ||
		observed.AgentCredentialGeneration!=challenge.AgentCredentialGeneration ||
		observed.Observation.TaskID!=challenge.TaskID ||
		observed.Observation.AgentID!=challenge.Binding.AgentID ||
		observed.Observation.ServerID!=challenge.Binding.ServerID ||
		challenge.IssuedAt.IsZero() || challenge.ExpiresAt.IsZero() ||
		challenge.ExpiresAt.Sub(challenge.IssuedAt)<=0 ||
		challenge.ExpiresAt.Sub(challenge.IssuedAt)>60*time.Second ||
		observed.ObservedAt.Before(challenge.IssuedAt.Add(-2*time.Second)) ||
		!observed.ObservedAt.Before(challenge.ExpiresAt) ||
		observed.ObservedAt.After(time.Now().UTC().Add(2*time.Second)) ||
		!time.Now().UTC().Before(challenge.ExpiresAt) {
		return ErrRecoveryObservationTransportRefused
	}
	evidence,err:=tasks.DeriveRuntimeRecoveryEvidenceDigestsV1(observed.Observation)
	if err!=nil || evidence.MarkerSHA256!=challenge.MarkerHash ||
		evidence.ReceiptSHA256!=challenge.ReceiptHash ||
		evidence.WitnessSHA256!=challenge.RuntimeWitnessHash {
		return ErrRecoveryObservationTransportRefused
	}
	claim:=tasks.RuntimeRecoveryTokenBoundEvidenceV1{
		SchemaVersion:1,ObservationID:challenge.ObservationID,
		ReservationID:challenge.ReservationID,AttemptID:challenge.AttemptID,
		AttemptEpoch:challenge.AttemptEpoch,Nonce:challenge.Nonce,
		AgentCredentialGeneration:challenge.AgentCredentialGeneration,
		Evidence:evidence,
	}
	mac,err:=tasks.RuntimeRecoveryTokenProofMACV1(token,claim)
	if err!=nil {return ErrRecoveryObservationTransportRefused}
	replyMAC,err:=tasks.RuntimeRecoveryFullReplyMACV1(token,claim,observed)
	if err!=nil {return ErrRecoveryObservationTransportRefused}
	var result observationReplyResult
	if err:=c.doJSON(ctx,"POST",
		"/api/v1/agent/recovery-observations/reply",token,
		observationReplySubmission{Claim:claim,MAC:mac,ReplyMAC:replyMAC,Reply:observed},&result);err!=nil{
		// No retries! The server may have committed and the HTTP reply lost.
		return err
	}
	if result.State!="reply_matched_unattested" {
		return ErrRecoveryObservationTransportRefused
	}
	return nil
}
