package tasks

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"time"
)

// RuntimeRecoveryObservationChallenge is a future Manager-originated,
// separately authenticated request. It is NOT accepted over an HTTP/Agent
// endpoint today. The Manager must store/consume each nonce exactly once;
// a nonce alone is not proof that a Manager authorized recovery.
//
// The three hashes refer to the exact private Agent marker, acknowledged
// receipt, and JSON encoding of its saved runtime witness respectively.
type RuntimeRecoveryObservationChallenge struct {
	SchemaVersion             int                  `json:"schemaVersion"`
	ObservationID             string               `json:"observationId"`
	ReservationID             string               `json:"reservationId"`
	AttemptID                 string               `json:"attemptId"`
	AttemptEpoch              int64                `json:"attemptEpoch"`
	Nonce                     string               `json:"nonce"`
	IssuedAt                  time.Time            `json:"issuedAt"`
	ExpiresAt                 time.Time            `json:"expiresAt"`
	Binding                   RuntimeResultBinding `json:"binding"`
	TaskID                    string               `json:"taskId"`
	AgentCredentialGeneration int64                `json:"agentCredentialGeneration"`
	MarkerHash                string               `json:"markerHash"`
	ReceiptHash               string               `json:"receiptHash"`
	RuntimeWitnessHash        string               `json:"runtimeWitnessHash"`
}

// RuntimeRecoveryChallengedObservation contains only read-only evidence.
// Its nonce and attempt identifiers are echoed, NOT signed. The Manager
// must verify an authenticated Agent transport, persist nonce consumption,
// and independently check the current Agent credential generation. Neither
// this object nor its digest releases a local runtime mutation fence.
type RuntimeRecoveryChallengedObservation struct {
	SchemaVersion             int                        `json:"schemaVersion"`
	ObservationID             string                     `json:"observationId"`
	ReservationID             string                     `json:"reservationId"`
	AttemptID                 string                     `json:"attemptId"`
	AttemptEpoch              int64                      `json:"attemptEpoch"`
	Nonce                     string                     `json:"nonce"`
	AgentCredentialGeneration int64                      `json:"agentCredentialGeneration"`
	ObservedAt                time.Time                  `json:"observedAt"`
	Observation               RuntimeRecoveryObservation `json:"observation"`
}

// A small request window limits accidental reuse and refuses stale requests.
// A future Manager must perform its OWN database-time expiry checks and
// atomically consume the nonce: this Agent-side window is not authoritative.
const runtimeObservationMaxWindow = 60 * time.Second
const runtimeObservationClockSkew = 2 * time.Second

func validRuntimeObservationChallenge(c RuntimeRecoveryObservationChallenge, binding RuntimeResultBinding, now time.Time) bool {
	if c.SchemaVersion != 1 ||
		!canonicalTaskIDPattern.MatchString(c.ObservationID) ||
		!canonicalTaskIDPattern.MatchString(c.ReservationID) ||
		!canonicalTaskIDPattern.MatchString(c.AttemptID) ||
		!canonicalTaskIDPattern.MatchString(c.TaskID) ||
		c.AttemptEpoch <= 0 ||
		c.AgentCredentialGeneration <= 0 ||
		c.Binding != binding ||
		binding.ManagerURL == "" ||
		!canonicalTaskIDPattern.MatchString(binding.AgentID) ||
		!canonicalTaskIDPattern.MatchString(binding.ServerID) ||
		!validRemovalHash(c.MarkerHash) ||
		!validRemovalHash(c.ReceiptHash) ||
		!validRemovalHash(c.RuntimeWitnessHash) ||
		c.IssuedAt.IsZero() || c.ExpiresAt.IsZero() ||
		c.ExpiresAt.Sub(c.IssuedAt) <= 0 ||
		c.ExpiresAt.Sub(c.IssuedAt) > runtimeObservationMaxWindow ||
		now.Add(runtimeObservationClockSkew).Before(c.IssuedAt) ||
		!now.Before(c.ExpiresAt) {
		return false
	}
	nonce, err := base64.RawURLEncoding.DecodeString(c.Nonce)
	return err == nil && len(nonce) == 32 && base64.RawURLEncoding.EncodeToString(nonce) == c.Nonce
}

// ObserveChallengedRuntimeRecovery applies a short-lived request binding to
// the existing strictly read-only, exclusive-lock recovery observation.
// It never creates or edits a recovery receipt, service, task or fence.
// A matching historical witness is necessary but NOT sufficient for unlock.
func ObserveChallengedRuntimeRecovery(
	ctx context.Context,
	dir string,
	binding RuntimeResultBinding,
	challenge RuntimeRecoveryObservationChallenge,
	activePath string,
	adapter VPNCoreAdapter,
) (RuntimeRecoveryChallengedObservation, error) {
	deny := func() (RuntimeRecoveryChallengedObservation, error) {
		return RuntimeRecoveryChallengedObservation{}, ErrRuntimeMutationBlocked
	}
	if ctx.Err() != nil || !validRuntimeObservationChallenge(challenge, binding, time.Now().UTC()) {
		return deny()
	}

	// ObserveRuntimeRecovery obtains the SAME local exclusive lock used by
	// fenced runtime mutations, and brackets the live process, config and
	// persisted receipt against concurrent local changes.
	observation, err := ObserveRuntimeRecovery(ctx, dir, binding, challenge.TaskID, activePath, adapter)
	if err != nil {
		return deny()
	}

	encodedWitness, err := json.Marshal(observation.Witness)
	if err != nil || observation.MarkerHash != challenge.MarkerHash ||
		observation.ReceiptHash != challenge.ReceiptHash ||
		markerDigest(encodedWitness) != challenge.RuntimeWitnessHash {
		return deny()
	}
	observedAt := time.Now().UTC()
	if ctx.Err() != nil || !validRuntimeObservationChallenge(challenge, binding, observedAt) {
		return deny()
	}
	return RuntimeRecoveryChallengedObservation{
		SchemaVersion: 1,
		ObservationID: challenge.ObservationID,
		ReservationID: challenge.ReservationID,
		AttemptID: challenge.AttemptID,
		AttemptEpoch: challenge.AttemptEpoch,
		Nonce: challenge.Nonce,
		AgentCredentialGeneration: challenge.AgentCredentialGeneration,
		ObservedAt: observedAt,
		Observation: observation,
	}, nil
}
