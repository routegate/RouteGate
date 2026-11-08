package tasks

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func recoveryObservationFixture(t *testing.T) (RemovalRecoveryObserver, RemovalRecoveryDecision, ed25519.PrivateKey, *removalAdapter) {
	t.Helper()
	executor, task, adapter, _ := removalExecutorFixture(t)
	deadline := time.Now().Add(time.Minute)
	task.DeliveryExpiresAt = &deadline
	adapter.restartHook = func() error { return errors.New("private command failure") }
	report, err := executor.ExecuteHeld(context.Background(), task)
	if err == nil || report.Code != "restart_failed" {
		t.Fatal(report, err)
	}
	if err := executor.AcknowledgeHeldRemovalReport(context.Background(), report); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(report)
	_, _, digest, _ := canonicalRemovalJSON(body)
	public, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	const audience = "00112233-4455-4677-8899-aabbccddeeff"
	observer, err := NewRemovalRecoveryObserver(executor, audience, public, 7)
	if err != nil {
		t.Fatal(err)
	}
	d := RemovalRecoveryDecision{
		Audience: audience, AttemptID: "ddeeff00-1122-4334-8556-778899aabbcc",
		OperationID: report.OperationID, JobID: report.JobID, AccountID: report.AccountID,
		ServerID: report.ServerID, AgentID: report.AgentID, AgentGeneration: 7,
		CandidateVersionID: report.ConfigVersionID, CandidateRuntimeHash: task.CredentialRemoval.CandidateRuntimeHash,
		FailureReportDigest: digest, ActorID: "11223344-5566-4788-899a-bbccddeeff00",
		SessionID: "99887766-5544-4322-8100-ffeeddccbbaa", Nonce: strings.Repeat("c", 64),
		SharedRestartConsent: true, IssuedAtUnixMicros: time.Now().Add(-time.Second).UnixMicro(),
		ExpiresAtUnixMicros: time.Now().Add(time.Minute).UnixMicro(),
	}
	return observer, d, private, adapter
}

func signedRecoveryObservationDecision(t *testing.T, d RemovalRecoveryDecision, private ed25519.PrivateKey) ([]byte, []byte) {
	t.Helper()
	body, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	message, err := d.SigningBytes()
	if err != nil {
		t.Fatal(err)
	}
	return body, ed25519.Sign(private, message)
}

func TestCredentialRemovalRecoveryObservationRetainsLockAndEvidence(t *testing.T) {
	observer, d, key, adapter := recoveryObservationFixture(t)
	e := observer.executor
	active, _ := os.ReadFile(e.activePath)
	fencePath := filepath.Join(e.receiptDir, "inflight.json")
	fence, _ := os.ReadFile(fencePath)
	original, err := e.readHeldRemovalReceipt(d.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	body, signature := signedRecoveryObservationDecision(t, d, key)
	lease, err := observer.Begin(context.Background(), body, signature)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	if lease.Observation.RuntimeState != "running" || lease.Observation.CandidateRuntimeHash != d.CandidateRuntimeHash ||
		lease.Observation.FailureReportDigest != d.FailureReportDigest {
		t.Fatal("observation did not bind the failed operation", lease.Observation)
	}
	if lock, err := e.lock(); err == nil {
		lock.Close()
		t.Fatal("observer released the shared mutation lock")
	}
	checkpoint, _ := os.ReadFile(observer.checkpointPath(d.AttemptID))
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewRemovalRecoveryObserver(e, observer.audience, observer.key, observer.generation)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := restarted.Begin(context.Background(), body, signature)
	if err != nil {
		t.Fatal(err)
	}
	defer replay.Close()
	if replay.Observation != lease.Observation {
		t.Fatal("replay renewed the original observation")
	}
	saved, _ := e.readHeldRemovalReceipt(d.OperationID)
	afterCheckpoint, _ := os.ReadFile(observer.checkpointPath(d.AttemptID))
	afterActive, _ := os.ReadFile(e.activePath)
	afterFence, _ := os.ReadFile(fencePath)
	if saved.Report != original.Report || saved.Fingerprint != original.Fingerprint || saved.RecoveryAttemptID != d.AttemptID ||
		!bytes.Equal(checkpoint, afterCheckpoint) || !bytes.Equal(active, afterActive) || !bytes.Equal(fence, afterFence) || adapter.restarts != 1 {
		t.Fatal("observation changed runtime, original evidence, fence or checkpoint")
	}
	for _, secret := range []string{removalUUID, "private command failure", e.activePath} {
		if bytes.Contains(checkpoint, []byte(secret)) {
			t.Fatal("checkpoint exposed private runtime data")
		}
	}
}

func TestCredentialRemovalRecoveryObservationRejectsUntrustedOrStaleDecision(t *testing.T) {
	observer, d, key, _ := recoveryObservationFixture(t)
	for name, edit := range map[string]func(*RemovalRecoveryDecision){
		"wrong agent":        func(d *RemovalRecoveryDecision) { d.AgentID = d.ActorID },
		"changed generation": func(d *RemovalRecoveryDecision) { d.AgentGeneration++ },
		"wrong failure":      func(d *RemovalRecoveryDecision) { d.FailureReportDigest = strings.Repeat("0", 64) },
		"wrong candidate":    func(d *RemovalRecoveryDecision) { d.CandidateRuntimeHash = strings.Repeat("0", 64) },
		"no consent":         func(d *RemovalRecoveryDecision) { d.SharedRestartConsent = false },
		"expired":            func(d *RemovalRecoveryDecision) { d.ExpiresAtUnixMicros = time.Now().Add(-time.Second).UnixMicro() },
		"overlong":           func(d *RemovalRecoveryDecision) { d.ExpiresAtUnixMicros = time.Now().Add(3 * time.Minute).UnixMicro() },
	} {
		t.Run(name, func(t *testing.T) {
			bad := d
			edit(&bad)
			body, signature := signedRecoveryObservationDecision(t, bad, key)
			if lease, err := observer.Begin(context.Background(), body, signature); err == nil {
				lease.Close()
				t.Fatal("unsafe recovery decision accepted")
			}
		})
	}
	body, signature := signedRecoveryObservationDecision(t, d, key)
	signature[0] ^= 1
	if lease, err := observer.Begin(context.Background(), body, signature); err == nil {
		lease.Close()
		t.Fatal("forged signature accepted")
	}
	lock, err := observer.executor.lock()
	if err != nil {
		t.Fatal(err)
	}
	_, signature = signedRecoveryObservationDecision(t, d, key)
	if lease, err := observer.Begin(context.Background(), body, signature); err == nil {
		lease.Close()
		t.Fatal("observer admitted while previous worker held lock")
	}
	lock.Close()
	if _, err := os.Stat(observer.checkpointPath(d.AttemptID)); !os.IsNotExist(err) {
		t.Fatal("rejected decisions left a checkpoint")
	}
}

func TestCredentialRemovalRecoveryObservationDeadlineAndUnknownRuntime(t *testing.T) {
	observer, d, key, adapter := recoveryObservationFixture(t)
	body, signature := signedRecoveryObservationDecision(t, d, key)
	clock := time.UnixMicro(d.IssuedAtUnixMicros).Add(time.Second)
	adapter.healthHook = func() error { clock = time.UnixMicro(d.ExpiresAtUnixMicros); return nil }
	if lease, err := observer.begin(context.Background(), body, signature, func() time.Time { return clock }); err == nil {
		lease.Close()
		t.Fatal("observation exceeded approval deadline")
	}
	if _, err := os.Stat(observer.checkpointPath(d.AttemptID)); !os.IsNotExist(err) {
		t.Fatal("expired observation left a checkpoint")
	}
	adapter.healthHook = nil
	adapter.observeHook = func() (RuntimeGeneration, error) { return RuntimeGeneration{}, errors.New("unavailable") }
	lease, err := observer.Begin(context.Background(), body, signature)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	if lease.Observation.RuntimeState != "unverified" || lease.Observation.Generation != (RuntimeGeneration{}) ||
		lease.Observation.ListenerPort != 0 || adapter.restarts != 1 {
		t.Fatal("unknown process became verified or restarted", lease.Observation)
	}
}

func TestCredentialRemovalRecoveryObservationSupersedesExpiredAttempt(t *testing.T) {
	observer, d, key, adapter := recoveryObservationFixture(t)
	body, signature := signedRecoveryObservationDecision(t, d, key)
	first, err := observer.Begin(context.Background(), body, signature)
	if err != nil {
		t.Fatal(err)
	}
	first.Close()
	later := time.UnixMicro(d.ExpiresAtUnixMicros).Add(time.Second)
	next := d
	next.AttemptID = "aabbccdd-eeff-4011-8233-445566778899"
	next.PreviousAttemptID = d.AttemptID
	next.IssuedAtUnixMicros = later.UnixMicro()
	next.ExpiresAtUnixMicros = later.Add(time.Minute).UnixMicro()
	body, signature = signedRecoveryObservationDecision(t, next, key)
	lease, err := observer.begin(context.Background(), body, signature, func() time.Time { return later })
	if err != nil {
		t.Fatal(err)
	}
	lease.Close()
	body, signature = signedRecoveryObservationDecision(t, d, key)
	if lease, err := observer.begin(context.Background(), body, signature, func() time.Time { return later }); err == nil {
		lease.Close()
		t.Fatal("superseded attempt resumed")
	}
	if _, err := observer.readCheckpoint(d.AttemptID); err != nil || adapter.restarts != 1 {
		t.Fatal("old checkpoint was lost or another restart occurred", err)
	}
}

func TestCredentialRemovalRecoveryObservationRequiresAcknowledgedFailure(t *testing.T) {
	observer, d, key, _ := recoveryObservationFixture(t)
	receipt, err := observer.executor.readHeldRemovalReceipt(d.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	receipt.Acknowledged = false
	if err := observer.executor.saveHeldRemovalReceipt(d.OperationID, receipt); err != nil {
		t.Fatal(err)
	}
	body, signature := signedRecoveryObservationDecision(t, d, key)
	if lease, err := observer.Begin(context.Background(), body, signature); err == nil {
		lease.Close()
		t.Fatal("recovery observed before Manager accepted the original failure")
	}
}

func TestCredentialRemovalRecoveryObservationMonotonicDeadline(t *testing.T) {
	observer, d, key, adapter := recoveryObservationFixture(t)
	clock := time.Now()
	d.IssuedAtUnixMicros = clock.Add(-time.Second).UnixMicro()
	d.ExpiresAtUnixMicros = clock.Add(10 * time.Millisecond).UnixMicro()
	body, signature := signedRecoveryObservationDecision(t, d, key)
	adapter.healthHook = func() error { time.Sleep(30 * time.Millisecond); return nil }
	if lease, err := observer.begin(context.Background(), body, signature, func() time.Time { return clock }); err == nil {
		lease.Close()
		t.Fatal("fixed wall clock extended signed observation deadline")
	}
	if _, err := os.Stat(observer.checkpointPath(d.AttemptID)); !os.IsNotExist(err) {
		t.Fatal("timed-out observation left a checkpoint")
	}
}

func TestCredentialRemovalRecoveryObservationRejectsCorruptedCheckpoint(t *testing.T) {
	observer, d, key, _ := recoveryObservationFixture(t)
	body, signature := signedRecoveryObservationDecision(t, d, key)
	lease, err := observer.Begin(context.Background(), body, signature)
	if err != nil {
		t.Fatal(err)
	}
	lease.Close()
	checkpoint, err := observer.readCheckpoint(d.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint.Observation.ObservedAtUnixMicros = d.ExpiresAtUnixMicros
	data, _ := json.Marshal(checkpoint)
	if err := writeRemovalFile(observer.checkpointPath(d.AttemptID), data); err != nil {
		t.Fatal(err)
	}
	if lease, err := observer.Begin(context.Background(), body, signature); err == nil {
		lease.Close()
		t.Fatal("out-of-window checkpoint replay accepted")
	}
}
