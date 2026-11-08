package tasks

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// RemovalRecoveryDecision shares the signed v1 contract with Manager. The key,
// audience and generation are supplied by trusted local configuration.
type RemovalRecoveryDecision struct {
	Audience             string `json:"audience"`
	AttemptID            string `json:"attemptId"`
	PreviousAttemptID    string `json:"previousAttemptId,omitempty"`
	OperationID          string `json:"operationId"`
	JobID                string `json:"jobId"`
	AccountID            string `json:"accountId"`
	ServerID             string `json:"serverId"`
	AgentID              string `json:"agentId"`
	AgentGeneration      int64  `json:"agentGeneration"`
	CandidateVersionID   string `json:"candidateVersionId"`
	CandidateRuntimeHash string `json:"candidateRuntimeHash"`
	FailureReportDigest  string `json:"failureReportDigest"`
	ActorID              string `json:"actorId"`
	SessionID            string `json:"sessionId"`
	Nonce                string `json:"nonce"`
	SharedRestartConsent bool   `json:"sharedRestartConsent"`
	IssuedAtUnixMicros   int64  `json:"issuedAtUnixMicros"`
	ExpiresAtUnixMicros  int64  `json:"expiresAtUnixMicros"`
}

func (d RemovalRecoveryDecision) SigningBytes() ([]byte, error) {
	body, err := json.Marshal(d)
	if err != nil {
		return nil, err
	}
	return append([]byte("routegate/v1/approve-held-vless-removal-reconciliation\x00"), body...), nil
}

type RemovalRecoveryObservation struct {
	AttemptID            string            `json:"attemptId"`
	OperationID          string            `json:"operationId"`
	FailureReportDigest  string            `json:"failureReportDigest"`
	CandidateRuntimeHash string            `json:"candidateRuntimeHash"`
	RuntimeState         string            `json:"runtimeState"`
	Generation           RuntimeGeneration `json:"generation"`
	ListenerPort         int               `json:"listenerPort,omitempty"`
	ObservedAtUnixMicros int64             `json:"observedAtUnixMicros"`
}

type removalRecoveryCheckpoint struct {
	Decision    RemovalRecoveryDecision    `json:"decision"`
	Signature   []byte                     `json:"signature"`
	Observation RemovalRecoveryObservation `json:"observation"`
}

// RemovalRecoveryObserver captures diagnostics while retaining the runtime
// lock. Its lease grants no service mutation or reservation release.
type RemovalRecoveryObserver struct {
	executor   CredentialRemovalExecutor
	audience   string
	key        ed25519.PublicKey
	generation int64
}

func NewRemovalRecoveryObserver(executor CredentialRemovalExecutor, audience string, key ed25519.PublicKey, generation int64) (RemovalRecoveryObserver, error) {
	if !canonicalTaskIDPattern.MatchString(audience) || len(key) != ed25519.PublicKeySize || generation < 1 ||
		!SupportsRuntimeRecovery(executor.adapter) {
		return RemovalRecoveryObserver{}, ErrCredentialRemoval
	}
	return RemovalRecoveryObserver{executor, audience, bytes.Clone(key), generation}, nil
}

type RemovalRecoveryObservationLease struct {
	Observation RemovalRecoveryObservation
	lock        *os.File
}

func (l *RemovalRecoveryObservationLease) Close() error {
	if l == nil || l.lock == nil {
		return nil
	}
	err := l.lock.Close()
	l.lock = nil
	return err
}

func (o RemovalRecoveryObserver) Begin(ctx context.Context, decision []byte, signature []byte) (*RemovalRecoveryObservationLease, error) {
	return o.begin(ctx, decision, signature, time.Now)
}

func (o RemovalRecoveryObserver) begin(ctx context.Context, data []byte, signature []byte, now func() time.Time) (*RemovalRecoveryObservationLease, error) {
	d, err := o.decodeDecision(data, signature)
	started := now()
	if err != nil || ctx.Err() != nil || !recoveryObservationLive(d, started) {
		return nil, ErrCredentialRemoval
	}
	// Convert once to a monotonic context deadline so a wall-clock rollback
	// cannot extend collection beyond the signed approval window.
	ctx, cancel := context.WithTimeout(ctx, time.UnixMicro(d.ExpiresAtUnixMicros).Sub(started))
	defer cancel()
	lock, err := lockRuntimeMutationExisting(o.executor.receiptDir)
	if err != nil {
		return nil, err
	}
	handedOff := false
	defer func() {
		if !handedOff {
			_ = lock.Close()
		}
	}()
	e := o.executor
	receipt, err := e.readHeldRemovalReceipt(d.OperationID)
	if err != nil || !receipt.Held || !receipt.Acknowledged || !validHeldRemovalDelivery(receipt.Report) ||
		!validSavedRemovalFailure(receipt.Report) || receipt.Report.JobID != d.JobID ||
		receipt.Report.AccountID != d.AccountID || receipt.Report.ConfigVersionID != d.CandidateVersionID ||
		receipt.Report.ServerID != d.ServerID || receipt.Report.AgentID != d.AgentID {
		return nil, ErrCredentialRemoval
	}
	wire, err := json.Marshal(receipt.Report)
	if err != nil {
		return nil, ErrCredentialRemoval
	}
	_, _, digest, err := canonicalRemovalJSON(wire)
	if err != nil || digest != d.FailureReportDigest {
		return nil, ErrCredentialRemoval
	}
	fence, err := readRemovalFile(filepath.Join(e.receiptDir, "inflight.json"), 16384, true)
	if err != nil || string(fence) != receipt.Fingerprint ||
		!runtimeMarkerAbsent(filepath.Join(e.receiptDir, "mutation-inflight.json")) ||
		!runtimeMarkerAbsent(filepath.Join(e.receiptDir, runtimeResultFile)) || !runtimeRecoveryStateAbsent(e.receiptDir) {
		return nil, ErrCredentialRemoval
	}
	if receipt.RecoveryAttemptID != "" && receipt.RecoveryAttemptID != d.AttemptID {
		previous, err := o.readCheckpoint(receipt.RecoveryAttemptID)
		if err != nil || d.PreviousAttemptID != receipt.RecoveryAttemptID ||
			previous.Decision.OperationID != d.OperationID ||
			now().Before(time.UnixMicro(previous.Decision.ExpiresAtUnixMicros)) {
			return nil, ErrCredentialRemoval
		}
	}
	active, err := readRemovalFile(e.activePath, removalMaxConfigBytes, false)
	_, _, activeHash, hashErr := canonicalRemovalJSON(active)
	if err != nil || hashErr != nil || activeHash != d.CandidateRuntimeHash {
		return nil, ErrCredentialRemoval
	}
	observation := RemovalRecoveryObservation{
		AttemptID: d.AttemptID, OperationID: d.OperationID, FailureReportDigest: digest,
		CandidateRuntimeHash: activeHash, RuntimeState: "unverified",
	}
	// A running process and candidate file are separate observations. This
	// does not establish which config that process originally loaded.
	witness, witnessErr := CaptureRuntimeRecoveryWitness(ctx, e.activePath, e.adapter)
	if witnessErr == nil && witness.RuntimeHash == activeHash {
		observation.RuntimeState = "running"
		observation.Generation, observation.ListenerPort = witness.Generation, witness.ListenerPort
	}
	current, err := readRemovalFile(e.activePath, removalMaxConfigBytes, false)
	_, _, currentHash, hashErr := canonicalRemovalJSON(current)
	if err != nil || hashErr != nil || currentHash != activeHash || ctx.Err() != nil || !recoveryObservationLive(d, now()) {
		return nil, ErrCredentialRemoval
	}
	observation.ObservedAtUnixMicros = now().UnixMicro()
	checkpoint := removalRecoveryCheckpoint{Decision: d, Signature: bytes.Clone(signature), Observation: observation}
	saved, err := o.readCheckpoint(d.AttemptID)
	switch {
	case err == nil:
		compared := observation
		compared.ObservedAtUnixMicros = saved.Observation.ObservedAtUnixMicros
		if saved.Decision != d || !bytes.Equal(saved.Signature, signature) || saved.Observation != compared {
			return nil, ErrCredentialRemoval
		}
		observation = saved.Observation
	case errors.Is(err, os.ErrNotExist):
		body, err := json.Marshal(checkpoint)
		if err != nil || !recoveryObservationLive(d, now()) {
			return nil, ErrCredentialRemoval
		}
		if err := writeRemovalFile(o.checkpointPath(d.AttemptID), body); err != nil {
			return nil, err
		}
	default:
		return nil, err
	}
	if ctx.Err() != nil || !recoveryObservationLive(d, now()) {
		return nil, ErrCredentialRemoval
	}
	if receipt.RecoveryAttemptID != d.AttemptID {
		receipt.RecoveryAttemptID = d.AttemptID
		if err := e.saveHeldRemovalReceipt(d.OperationID, receipt); err != nil {
			return nil, err
		}
	}
	if ctx.Err() != nil || !recoveryObservationLive(d, now()) {
		return nil, ErrCredentialRemoval
	}
	handedOff = true
	return &RemovalRecoveryObservationLease{Observation: observation, lock: lock}, nil
}

func recoveryObservationLive(d RemovalRecoveryDecision, now time.Time) bool {
	issued, expires := time.UnixMicro(d.IssuedAtUnixMicros), time.UnixMicro(d.ExpiresAtUnixMicros)
	return !issued.After(now) && expires.After(now) && expires.Sub(issued) > 0 && expires.Sub(issued) <= 90*time.Second
}

func (o RemovalRecoveryObserver) decodeDecision(data, signature []byte) (RemovalRecoveryDecision, error) {
	var d RemovalRecoveryDecision
	if len(data) > 16384 || len(signature) != ed25519.SignatureSize || len(o.key) != ed25519.PublicKeySize {
		return d, ErrCredentialRemoval
	}
	_, canonical, _, err := canonicalRemovalJSON(data)
	if err != nil {
		return d, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&d) != nil {
		return d, ErrCredentialRemoval
	}
	body, _ := json.Marshal(d)
	_, typed, _, err := canonicalRemovalJSON(body)
	if err != nil || !bytes.Equal(canonical, typed) || d.Audience != o.audience ||
		d.AgentID != o.executor.agentID || d.ServerID != o.executor.serverID || d.AgentGeneration != o.generation ||
		d.AttemptID == d.OperationID || d.AttemptID == d.JobID || !d.SharedRestartConsent ||
		!validRemovalHash(d.Nonce) || !validRemovalHash(d.FailureReportDigest) || !validRemovalHash(d.CandidateRuntimeHash) ||
		(d.PreviousAttemptID != "" && !canonicalTaskIDPattern.MatchString(d.PreviousAttemptID)) {
		return d, ErrCredentialRemoval
	}
	for _, id := range []string{d.Audience, d.AttemptID, d.OperationID, d.JobID, d.AccountID, d.ServerID, d.AgentID, d.CandidateVersionID, d.ActorID, d.SessionID} {
		if !canonicalTaskIDPattern.MatchString(id) {
			return d, ErrCredentialRemoval
		}
	}
	message, err := d.SigningBytes()
	if err != nil || !ed25519.Verify(o.key, message, signature) {
		return d, ErrCredentialRemoval
	}
	return d, nil
}

func (o RemovalRecoveryObserver) checkpointPath(attempt string) string {
	return filepath.Join(o.executor.receiptDir, attempt+".recovery-observation.json")
}

func (o RemovalRecoveryObserver) readCheckpoint(attempt string) (removalRecoveryCheckpoint, error) {
	var checkpoint removalRecoveryCheckpoint
	if !canonicalTaskIDPattern.MatchString(attempt) {
		return checkpoint, ErrCredentialRemoval
	}
	data, err := readRemovalFile(o.checkpointPath(attempt), 16384, true)
	if err != nil {
		return checkpoint, err
	}
	if _, _, _, err := canonicalRemovalJSON(data); err != nil {
		return checkpoint, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&checkpoint) != nil {
		return checkpoint, ErrCredentialRemoval
	}
	decision, _ := json.Marshal(checkpoint.Decision)
	_, err = o.decodeDecision(decision, checkpoint.Signature)
	if err != nil || checkpoint.Decision.AttemptID != attempt || checkpoint.Observation.AttemptID != attempt ||
		checkpoint.Observation.OperationID != checkpoint.Decision.OperationID ||
		checkpoint.Observation.FailureReportDigest != checkpoint.Decision.FailureReportDigest ||
		checkpoint.Observation.CandidateRuntimeHash != checkpoint.Decision.CandidateRuntimeHash ||
		checkpoint.Observation.ObservedAtUnixMicros < checkpoint.Decision.IssuedAtUnixMicros ||
		checkpoint.Observation.ObservedAtUnixMicros >= checkpoint.Decision.ExpiresAtUnixMicros {
		return checkpoint, ErrCredentialRemoval
	}
	switch checkpoint.Observation.RuntimeState {
	case "running":
		if !validRemovalGeneration(checkpoint.Observation.Generation) ||
			checkpoint.Observation.ListenerPort < 1 || checkpoint.Observation.ListenerPort > 65535 {
			return checkpoint, ErrCredentialRemoval
		}
	case "unverified":
		if checkpoint.Observation.Generation != (RuntimeGeneration{}) || checkpoint.Observation.ListenerPort != 0 {
			return checkpoint, ErrCredentialRemoval
		}
	default:
		return checkpoint, ErrCredentialRemoval
	}
	return checkpoint, nil
}
