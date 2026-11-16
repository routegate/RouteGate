package tasks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRuntimeMutationRecoveryTargetHash(t *testing.T) {
	_, task, _, _ := removalExecutorFixture(t)
	hash, err := RuntimeRecoveryTargetHash(task)
	if err != nil || hash != task.CredentialRemoval.CandidateRuntimeHash {
		t.Fatal("candidate digest changed", err)
	}
	task.RenderedConfig = json.RawMessage(`{"singBox":null}`)
	if _, err := RuntimeRecoveryTargetHash(task); err == nil {
		t.Fatal("invalid candidate accepted")
	}
}

func recoveryFixture(t *testing.T) (CredentialRemovalExecutor, ConfigTask, *removalAdapter, RuntimeResultBinding) {
	t.Helper()
	e, task, adapter, _ := removalExecutorFixture(t)
	lease, err := BeginRuntimeMutation(e.receiptDir, task.ID, TaskKindVPNCoreService)
	if err != nil {
		t.Fatal(err)
	}
	previous := adapter.generation
	if _, err := adapter.Restart(context.Background()); err != nil {
		t.Fatal(err)
	}
	witness, err := CaptureRuntimeRecoveryWitness(context.Background(), e.activePath, adapter)
	if err != nil {
		t.Fatal(err)
	}
	witness.PreviousGeneration = previous
	binding := RuntimeResultBinding{ManagerURL: "https://isolated.invalid", AgentID: task.AgentID, ServerID: task.ServerID}
	if err := lease.SaveResultWithRuntimeWitness(binding, task.ID, []byte(`{"status":"succeeded","resultPayload":{"kind":"vpn_core_service","operation":"restart"}}`), &witness); err != nil {
		t.Fatal(err)
	}
	lease.Close()
	if err := ReplayRuntimeResult(e.receiptDir, binding, func(string, []byte) error { return nil }); err != nil {
		t.Fatal(err)
	}
	return e, task, adapter, binding
}

func TestRuntimeMutationRecoveryObservation(t *testing.T) {
	e, task, a, binding := recoveryFixture(t)
	paths := []string{e.activePath, filepath.Join(e.receiptDir, "mutation-inflight.json"), filepath.Join(e.receiptDir, runtimeResultFile)}
	before := make([][]byte, len(paths))
	for i, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		before[i] = data
	}
	proof, err := ObserveRuntimeRecovery(context.Background(), e.receiptDir, binding, task.ID, e.activePath, a)
	if err != nil || proof.TaskID != task.ID || proof.AgentID != task.AgentID || proof.ServerID != task.ServerID || proof.Witness.Generation != a.generation || !validRemovalHash(proof.ReceiptHash) {
		t.Fatal(proof, err)
	}
	for i, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(before[i], data) {
			t.Fatal("observation mutated durable state", err)
		}
	}
	if lease, err := BeginRuntimeMutation(e.receiptDir, task.ID, TaskKindVPNCoreService); err == nil {
		lease.Close()
		t.Fatal("observation unlocked mutation")
	}
	if a.restarts != 1 || a.validations != 0 {
		t.Fatal("observation ran mutating adapter methods")
	}
	encoded, _ := json.Marshal(proof)
	for _, secret := range []string{removalUUID, "DO-NOT-LOG", e.activePath} {
		if bytes.Contains(encoded, []byte(secret)) {
			t.Fatal("secret/path in observation")
		}
	}
}

func TestRuntimeMutationRecoveryRejectsChangedEvidence(t *testing.T) {
	for _, mode := range []string{"generation", "config", "listener", "during_health_config", "during_health_generation", "during_health_receipt", "unacknowledged", "legacy_receipt", "binding", "task", "removal", "cancelled", "concurrent"} {
		t.Run(mode, func(t *testing.T) {
			e, task, a, binding := recoveryFixture(t)
			ctx := context.Background()
			path := filepath.Join(e.receiptDir, runtimeResultFile)
			switch mode {
			case "generation":
				a.generation.InvocationID = strings.Repeat("c", 32)
			case "config":
				if err := os.WriteFile(e.activePath, []byte(`{"inbounds":[]}`), 0600); err != nil {
					t.Fatal(err)
				}
			case "listener":
				a.healthHook = func() error { return errors.New("not listening") }
			case "during_health_config":
				a.healthHook = func() error { return os.WriteFile(e.activePath, []byte(`{"inbounds":[]}`), 0600) }
			case "during_health_generation":
				a.healthHook = func() error { a.generation.PID++; return nil }
			case "during_health_receipt":
				a.healthHook = func() error { return os.WriteFile(path, []byte(`{}`), 0600) }
			case "unacknowledged", "legacy_receipt":
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				var receipt runtimeResultReceipt
				if err := json.Unmarshal(data, &receipt); err != nil {
					t.Fatal(err)
				}
				if mode == "unacknowledged" {
					receipt.Acknowledged = false
				} else {
					receipt.RuntimeWitness = nil
				}
				data, err = json.Marshal(receipt)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, data, 0600); err != nil {
					t.Fatal(err)
				}
			case "binding":
				binding.AgentID = resultTaskID
			case "task":
				task.ID = resultTaskID
			case "removal":
				if err := os.WriteFile(filepath.Join(e.receiptDir, "inflight.json"), []byte("unknown"), 0600); err != nil {
					t.Fatal(err)
				}
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "concurrent":
				lock, err := lockRuntimeMutation(e.receiptDir)
				if err != nil {
					t.Fatal(err)
				}
				defer lock.Close()
			}
			proof, err := ObserveRuntimeRecovery(ctx, e.receiptDir, binding, task.ID, e.activePath, a)
			if !errors.Is(err, ErrRuntimeMutationBlocked) || proof.SchemaVersion != 0 {
				t.Fatal("unsafe proof issued", proof, err)
			}
			if runtimeMarkerAbsent(filepath.Join(e.receiptDir, "mutation-inflight.json")) {
				t.Fatal("failure removed fence")
			}
		})
	}
}
