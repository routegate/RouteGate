package tasks

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func challengedRuntimeFixture(t *testing.T) (CredentialRemovalExecutor, ConfigTask, *removalAdapter, RuntimeResultBinding, RuntimeRecoveryObservationChallenge) {
	t.Helper()
	e, task, adapter, binding := recoveryFixture(t)
	observation, err := ObserveRuntimeRecovery(context.Background(), e.receiptDir, binding, task.ID, e.activePath, adapter)
	if err != nil {
		t.Fatal(err)
	}
	witness, err := json.Marshal(observation.Witness)
	if err != nil {
		t.Fatal(err)
	}
	issued := time.Now().UTC().Add(-time.Second)
	challenge := RuntimeRecoveryObservationChallenge{
		SchemaVersion: 1,
		ObservationID: "11111111-1111-4111-8111-111111111111",
		ReservationID: "22222222-2222-4222-8222-222222222222",
		AttemptID: "33333333-3333-4333-8333-333333333333",
		AttemptEpoch: 17,
		Nonce: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0xa4}, 32)),
		IssuedAt: issued,
		ExpiresAt: issued.Add(30 * time.Second),
		Binding: binding,
		TaskID: task.ID,
		AgentCredentialGeneration: 4,
		MarkerHash: observation.MarkerHash,
		ReceiptHash: observation.ReceiptHash,
		RuntimeWitnessHash: markerDigest(witness),
	}
	return e, task, adapter, binding, challenge
}

func TestRuntimeRecoveryChallengedObservationBindsFreshEvidenceAndRetainsFence(t *testing.T) {
	e, task, adapter, binding, challenge := challengedRuntimeFixture(t)
	paths := []string{
		e.activePath,
		filepath.Join(e.receiptDir, "mutation-inflight.json"),
		filepath.Join(e.receiptDir, "mutation-result.json"),
	}
	before := make([][]byte, len(paths))
	for i, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil { t.Fatal(err) }
		before[i] = data
	}

	proof, err := ObserveChallengedRuntimeRecovery(context.Background(), e.receiptDir, binding, challenge, e.activePath, adapter)
	if err != nil { t.Fatal(err) }
	if proof.SchemaVersion != 1 || proof.ObservationID != challenge.ObservationID ||
		proof.AttemptID != challenge.AttemptID || proof.ReservationID != challenge.ReservationID ||
		proof.AttemptEpoch != challenge.AttemptEpoch || proof.Nonce != challenge.Nonce ||
		proof.AgentCredentialGeneration != challenge.AgentCredentialGeneration ||
		proof.Observation.TaskID != task.ID || proof.Observation.MarkerHash != challenge.MarkerHash ||
		proof.Observation.ReceiptHash != challenge.ReceiptHash ||
		proof.Observation.Witness.Generation != adapter.generation ||
		proof.ObservedAt.Before(challenge.IssuedAt) || !proof.ObservedAt.Before(challenge.ExpiresAt) {
		t.Fatalf("unbound read-only observation: %+v", proof)
	}

	for i, path := range paths {
		current, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(before[i], current) {
			t.Fatalf("observation modified durable runtime material: %s %v", path, err)
		}
	}
	if lease, err := BeginRuntimeMutation(e.receiptDir, task.ID, TaskKindVPNCoreService); err == nil {
		lease.Close()
		t.Fatal("challenge observation unlocked runtime mutation admission")
	}
	if adapter.restarts != 1 || adapter.validations != 0 {
		t.Fatal("observation executed mutating adapter methods")
	}
	data, err := json.Marshal(proof)
	if err != nil { t.Fatal(err) }
	for _, sensitive := range []string{removalUUID, "DO-NOT-LOG", e.activePath, binding.ManagerURL} {
		if bytes.Contains(data, []byte(sensitive)) {
			t.Fatal("confidential identifier, path, or Manager URL leaked to proof")
		}
	}
}

func TestRuntimeRecoveryChallengedObservationRejectsIdentityNonceAndExpiryDrift(t *testing.T) {
	for name, mutate := range map[string]func(*RuntimeRecoveryObservationChallenge) {
		"missing_nonce": func(c *RuntimeRecoveryObservationChallenge) { c.Nonce = "" },
		"short_nonce": func(c *RuntimeRecoveryObservationChallenge) { c.Nonce = base64.RawURLEncoding.EncodeToString([]byte{1}) },
		"padded_nonce": func(c *RuntimeRecoveryObservationChallenge) { c.Nonce += "=" },
		"wrong_schema": func(c *RuntimeRecoveryObservationChallenge) { c.SchemaVersion++ },
		"wrong_attempt": func(c *RuntimeRecoveryObservationChallenge) { c.AttemptID = "" },
		"wrong_observation_id": func(c *RuntimeRecoveryObservationChallenge) { c.ObservationID = "" },
		"wrong_reservation": func(c *RuntimeRecoveryObservationChallenge) { c.ReservationID = "INVALID" },
		"zero_epoch": func(c *RuntimeRecoveryObservationChallenge) { c.AttemptEpoch = 0 },
		"zero_credential_generation": func(c *RuntimeRecoveryObservationChallenge) { c.AgentCredentialGeneration = 0 },
		"changed_task": func(c *RuntimeRecoveryObservationChallenge) { c.TaskID = c.ObservationID },
		"changed_agent": func(c *RuntimeRecoveryObservationChallenge) { c.Binding.AgentID = c.ObservationID },
		"changed_server": func(c *RuntimeRecoveryObservationChallenge) { c.Binding.ServerID = c.ObservationID },
		"changed_manager": func(c *RuntimeRecoveryObservationChallenge) { c.Binding.ManagerURL += "/other" },
		"bad_marker_hash": func(c *RuntimeRecoveryObservationChallenge) { c.MarkerHash = strings.Repeat("a", 64) },
		"bad_receipt_hash": func(c *RuntimeRecoveryObservationChallenge) { c.ReceiptHash = strings.Repeat("b", 64) },
		"bad_witness_hash": func(c *RuntimeRecoveryObservationChallenge) { c.RuntimeWitnessHash = strings.Repeat("c", 64) },
		"uppercase_witness_hash": func(c *RuntimeRecoveryObservationChallenge) { c.RuntimeWitnessHash = strings.Repeat("F", 64) },
		"already_expired": func(c *RuntimeRecoveryObservationChallenge) { c.ExpiresAt = time.Now().Add(-10*time.Second) },
		"too_long": func(c *RuntimeRecoveryObservationChallenge) { c.ExpiresAt = c.IssuedAt.Add(2*time.Minute) },
		"issued_in_future": func(c *RuntimeRecoveryObservationChallenge) { c.IssuedAt = time.Now().Add(10*time.Second); c.ExpiresAt = c.IssuedAt.Add(10*time.Second) },
	} {
		t.Run(name, func(t *testing.T) {
			e, _, adapter, binding, challenge := challengedRuntimeFixture(t)
			mutate(&challenge)
			proof, err := ObserveChallengedRuntimeRecovery(context.Background(), e.receiptDir, binding, challenge, e.activePath, adapter)
			if !errors.Is(err, ErrRuntimeMutationBlocked) || proof.SchemaVersion != 0 {
				t.Fatalf("untrusted request accepted: %+v %v", proof, err)
			}
			if runtimeMarkerAbsent(filepath.Join(e.receiptDir, "mutation-inflight.json")) {
				t.Fatal("failed observation removed the runtime fence")
			}
		})
	}
}

func TestRuntimeRecoveryChallengedObservationRejectsChangedRuntimeAndConcurrentMutation(t *testing.T) {
	for _, mode := range []string{"generation", "config", "health", "receipt", "lock", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			e, _, adapter, binding, challenge := challengedRuntimeFixture(t)
			ctx := context.Background()
			switch mode {
			case "generation":
				adapter.generation.InvocationID = strings.Repeat("d", 32)
			case "config":
				if err := os.WriteFile(e.activePath, []byte(`{"inbounds":[]}`), 0600); err != nil { t.Fatal(err) }
			case "health":
				adapter.healthHook = func() error { return errors.New("runtime health unknown") }
			case "receipt":
				if err := os.WriteFile(filepath.Join(e.receiptDir, "mutation-result.json"), []byte("{}"), 0600); err != nil { t.Fatal(err) }
			case "lock":
				lock, err := lockRuntimeMutation(e.receiptDir)
				if err != nil { t.Fatal(err) }
				defer lock.Close()
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			proof, err := ObserveChallengedRuntimeRecovery(ctx, e.receiptDir, binding, challenge, e.activePath, adapter)
			if !errors.Is(err, ErrRuntimeMutationBlocked) || proof.SchemaVersion != 0 {
				t.Fatalf("changed runtime was admitted: %+v %v", proof, err)
			}
			if runtimeMarkerAbsent(filepath.Join(e.receiptDir, "mutation-inflight.json")) {
				t.Fatal("runtime intent marker was removed")
			}
		})
	}
}

func TestRuntimeRecoveryChallengedObservationRefusesExpiryDuringObservation(t *testing.T) {
	e, _, adapter, binding, challenge := challengedRuntimeFixture(t)
	challenge.IssuedAt = time.Now().UTC()
	challenge.ExpiresAt = challenge.IssuedAt.Add(25*time.Millisecond)
	adapter.healthHook = func() error {
		time.Sleep(70*time.Millisecond)
		return nil
	}
	proof, err := ObserveChallengedRuntimeRecovery(context.Background(), e.receiptDir, binding, challenge, e.activePath, adapter)
	if !errors.Is(err, ErrRuntimeMutationBlocked) || proof.SchemaVersion != 0 {
		t.Fatalf("expired during observation but accepted: %+v %v", proof, err)
	}
	if runtimeMarkerAbsent(filepath.Join(e.receiptDir, "mutation-inflight.json")) {
		t.Fatal("runtime fence removed after deadline")
	}
}
