package client

import (
	"context"
	"encoding/base64"
	"errors"
	"regexp"
	"time"

	"github.com/ikaevus/routegate/agent/internal/tasks"
)

// RecoveryPendingAttempt is an Agent-owned, read-only Manager poll result.
// It is not an executable task and must never enter task dispatch/admission.
type RecoveryPendingAttempt struct {
	SchemaVersion int `json:"schemaVersion"`
	AttemptID string `json:"attemptId"`
	ReservationID string `json:"reservationId"`
	AttemptEpoch int64 `json:"attemptEpoch"`
	TaskID string `json:"taskId"`
	AgentID string `json:"agentId"`
	ServerID string `json:"serverId"`
	AgentCredentialGeneration int64 `json:"agentCredentialGeneration"`
}

type recoveryPendingResponse struct {
	Pending *RecoveryPendingAttempt `json:"pending"`
}

type recoveryIssueRequest struct {
	AttemptID string `json:"attemptId"`
	Evidence tasks.RuntimeRecoveryEvidenceDigestsV1 `json:"evidence"`
}

type recoveryIssuedResponse struct {
	SchemaVersion int `json:"schemaVersion"`
	ObservationID string `json:"observationId"`
	ReservationID string `json:"reservationId"`
	AttemptID string `json:"attemptId"`
	AttemptEpoch int64 `json:"attemptEpoch"`
	Nonce string `json:"nonce"`
	IssuedAt time.Time `json:"issuedAt"`
	ExpiresAt time.Time `json:"expiresAt"`
	TaskID string `json:"taskId"`
	AgentID string `json:"agentId"`
	ServerID string `json:"serverId"`
	AgentCredentialGeneration int64 `json:"agentCredentialGeneration"`
	ExpectedEvidence tasks.RuntimeRecoveryEvidenceDigestsV1 `json:"expectedEvidence"`
}

var ErrRecoveryObservationTransportRefused = errors.New("recovery observation transport refused")

// A Manager-provided identity is only usable when it matches Agent's
// canonical local v4 identity contract; malformed values fail closed.
var recoveryObservationUUID = regexp.MustCompile(
	`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`,
)

// PollRecoveryPendingAttempt uses Agent's existing outbound Manager bearer
// client but is deliberately NOT called by heartbeat, task polling or the
// Runner. It does not mint a nonce, mutate VPN state or bypass a fence.
func (c *Client) PollRecoveryPendingAttempt(ctx context.Context, token string) (*RecoveryPendingAttempt,error) {
	if c==nil || ctx.Err()!=nil || token=="" {
		return nil,ErrRecoveryObservationTransportRefused
	}
	var response recoveryPendingResponse
	if err:=c.doJSON(ctx,"GET","/api/v1/agent/recovery-observations/pending",token,nil,&response);err!=nil{
		return nil,err
	}
	p:=response.Pending
	if p==nil{return nil,nil}
	if p.SchemaVersion!=1 ||
		!recoveryObservationUUID.MatchString(p.AttemptID) ||
		!recoveryObservationUUID.MatchString(p.ReservationID) ||
		!recoveryObservationUUID.MatchString(p.TaskID) ||
		!recoveryObservationUUID.MatchString(p.AgentID) ||
		!recoveryObservationUUID.MatchString(p.ServerID) ||
		p.AttemptEpoch<=0 || p.AgentCredentialGeneration<=0 {
		return nil,ErrRecoveryObservationTransportRefused
	}
	return p,nil
}

// RequestRecoveryObservationNonce is deliberately one attempt with NO retry:
// a Manager may have committed the nonce while the HTTP response was lost.
// Evidence MUST come from Agent's read-only
// tasks.ObserveRuntimeRecoveryEvidenceV1, not caller-generated fake digests.
//
// This method is NOT wired into Runner. The Manager endpoints exist only
// as unregistered/test-only adapters until independent transport review.
func (c *Client) RequestRecoveryObservationNonce(
	ctx context.Context, token string, pending RecoveryPendingAttempt,
	evidence tasks.RuntimeRecoveryEvidenceDigestsV1,
) (tasks.RuntimeRecoveryObservationChallenge,error) {
	var empty tasks.RuntimeRecoveryObservationChallenge
	if c==nil || ctx.Err()!=nil || token=="" || pending.SchemaVersion!=1 ||
		!recoveryObservationUUID.MatchString(pending.AttemptID) ||
		!recoveryObservationUUID.MatchString(pending.ReservationID) ||
		!recoveryObservationUUID.MatchString(pending.AgentID) ||
		!recoveryObservationUUID.MatchString(pending.ServerID) ||
		!recoveryObservationUUID.MatchString(pending.TaskID) ||
		pending.AttemptEpoch<=0 || pending.AgentCredentialGeneration<=0 ||
		evidence.SchemaVersion!=1 || evidence.TaskID!=pending.TaskID ||
		evidence.AgentID!=pending.AgentID || evidence.ServerID!=pending.ServerID {
		return empty,ErrRecoveryObservationTransportRefused
	}
	var issued recoveryIssuedResponse
	err:=c.doJSON(ctx,"POST","/api/v1/agent/recovery-observations/issue",
		token,recoveryIssueRequest{AttemptID:pending.AttemptID,Evidence:evidence},&issued)
	if err!=nil {return empty,err}

	// Check EVERY identity/digest before using a server response to observe
	// the process. A lost or mismatched response is never retried in-place.
	now:=time.Now().UTC()
	rawNonce,nonceErr:=base64.RawURLEncoding.DecodeString(issued.Nonce)
	if issued.SchemaVersion!=1 ||
		!recoveryObservationUUID.MatchString(issued.ObservationID) ||
		nonceErr!=nil || len(rawNonce)!=32 ||
		base64.RawURLEncoding.EncodeToString(rawNonce)!=issued.Nonce ||
		issued.ReservationID!=pending.ReservationID ||
		issued.AttemptID!=pending.AttemptID ||
		issued.AttemptEpoch!=pending.AttemptEpoch ||
		issued.TaskID!=pending.TaskID ||
		issued.AgentID!=pending.AgentID || issued.ServerID!=pending.ServerID ||
		issued.AgentCredentialGeneration!=pending.AgentCredentialGeneration ||
		issued.ExpectedEvidence!=evidence ||
		issued.IssuedAt.IsZero() || issued.ExpiresAt.IsZero() ||
		issued.ExpiresAt.Sub(issued.IssuedAt)<=0 ||
		issued.ExpiresAt.Sub(issued.IssuedAt)>60*time.Second ||
		!now.Before(issued.ExpiresAt) ||
		now.Add(2*time.Second).Before(issued.IssuedAt) {
		return empty,ErrRecoveryObservationTransportRefused
	}
	// Only data fields. The Agent observer independently verifies canonical
	// 32-byte nonce, UUID, hash and process binding under its local lock.
	return tasks.RuntimeRecoveryObservationChallenge{
		SchemaVersion:1,ObservationID:issued.ObservationID,
		ReservationID:issued.ReservationID,AttemptID:issued.AttemptID,
		AttemptEpoch:issued.AttemptEpoch,Nonce:issued.Nonce,
		IssuedAt:issued.IssuedAt,ExpiresAt:issued.ExpiresAt,
		Binding:tasks.RuntimeResultBinding{
			ManagerURL:c.managerURL,AgentID:pending.AgentID,ServerID:pending.ServerID,
		},
		TaskID:pending.TaskID,
		AgentCredentialGeneration:pending.AgentCredentialGeneration,
		MarkerHash:evidence.MarkerSHA256,ReceiptHash:evidence.ReceiptSHA256,
		RuntimeWitnessHash:evidence.WitnessSHA256,
	},nil
}
