package tasks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// These tests simulate local crash cut-points using disposable fixtures. No
// production Agent or Manager is involved. An incomplete state must never
// become an authorization to resume runtime mutation.
func TestRuntimeRecoveryCrashBoundariesRemainFenced(t *testing.T) {
	for _, mode := range []string{
		"intent_only",
		"receipt_only",
		"unacknowledged_receipt",
		"damaged_intent",
		"damaged_receipt",
		"concurrent_removal_intent",
		"changed_active_config",
	} {
		t.Run(mode, func(t *testing.T) {
			e, task, adapter, binding := recoveryFixture(t)
			markerPath := filepath.Join(e.receiptDir, "mutation-inflight.json")
			receiptPath := filepath.Join(e.receiptDir, runtimeResultFile)
			switch mode {
			case "intent_only":
				if err := os.Remove(receiptPath); err != nil { t.Fatal(err) }
			case "receipt_only":
				if err := os.Remove(markerPath); err != nil { t.Fatal(err) }
			case "unacknowledged_receipt":
				raw, err := os.ReadFile(receiptPath)
				if err != nil { t.Fatal(err) }
				var receipt runtimeResultReceipt
				if err := json.Unmarshal(raw, &receipt); err != nil { t.Fatal(err) }
				receipt.Acknowledged = false
				changed, err := json.Marshal(receipt)
				if err != nil { t.Fatal(err) }
				if err := os.WriteFile(receiptPath, changed, 0600); err != nil { t.Fatal(err) }
			case "damaged_intent":
				if err := os.WriteFile(markerPath, []byte("{"), 0600); err != nil { t.Fatal(err) }
			case "damaged_receipt":
				if err := os.WriteFile(receiptPath, []byte("{"), 0600); err != nil { t.Fatal(err) }
			case "concurrent_removal_intent":
				if err := os.WriteFile(filepath.Join(e.receiptDir, "inflight.json"), []byte("{}"), 0600); err != nil { t.Fatal(err) }
			case "changed_active_config":
				if err := os.WriteFile(e.activePath, []byte(`{"inbounds":[]}`), 0600); err != nil { t.Fatal(err) }
			}

			// Snapshot the deliberately damaged state. The review itself must
			// not "repair" or silently clear any of its durable files.
			files := []string{markerPath, receiptPath, e.activePath}
			before := make([][]byte, len(files))
			existed := make([]bool, len(files))
			for i, path := range files {
				data, err := os.ReadFile(path)
				if err != nil && !os.IsNotExist(err) { t.Fatal(err) }
				existed[i] = err == nil
				before[i] = data
			}

			hold, err := beginRuntimeRecoveryReadOnlyHoldV1(
				context.Background(), e.receiptDir, binding, task.ID, e.activePath, adapter,
			)
			if hold != nil {
				_ = hold.Close()
				t.Fatal("crash boundary admitted a local hold")
			}
			if !errors.Is(err, ErrRuntimeMutationBlocked) {
				t.Fatalf("crash boundary did not fail closed: %v", err)
			}
			if other, err := BeginRuntimeMutation(e.receiptDir, task.ID, TaskKindVPNCoreService); err == nil {
				_ = other.Close()
				t.Fatal("crash boundary reopened ordinary mutation")
			}
			if adapter.restarts != 1 || adapter.validations != 0 {
				t.Fatal("read-only review altered VPN core runtime")
			}
			for i, path := range files {
				data, err := os.ReadFile(path)
				if err != nil && !os.IsNotExist(err) { t.Fatal(err) }
				if (err == nil) != existed[i] || !bytes.Equal(data, before[i]) {
					t.Fatalf("review modified crash state at %s: %v", path, err)
				}
			}
		})
	}
}

func TestRuntimeRecoveryCancelledDuringHealthNeverLeaksEvidence(t *testing.T) {
	e, task, adapter, binding := recoveryFixture(t)
	markerPath := filepath.Join(e.receiptDir, "mutation-inflight.json")
	receiptPath := filepath.Join(e.receiptDir, runtimeResultFile)
	markerBefore, err := os.ReadFile(markerPath)
	if err != nil { t.Fatal(err) }
	receiptBefore, err := os.ReadFile(receiptPath)
	if err != nil { t.Fatal(err) }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Cancellation happens only after the local observer has entered its
	// health step, not before lock acquisition.
	adapter.healthHook = func() error { cancel(); return nil }
	hold, err := beginRuntimeRecoveryReadOnlyHoldV1(
		ctx, e.receiptDir, binding, task.ID, e.activePath, adapter,
	)
	if hold != nil {
		_ = hold.Close()
		t.Fatal("cancelled in-flight observation returned retained evidence")
	}
	if !errors.Is(err, ErrRuntimeMutationBlocked) {
		t.Fatalf("mid-observation cancellation accepted: %v", err)
	}
	if fresh, err := lockRuntimeMutation(e.receiptDir); err != nil {
		t.Fatal("cancelled observation leaked its exclusive flock", err)
	} else {
		_ = fresh.Close()
	}
	markerAfter, err := os.ReadFile(markerPath)
	if err != nil { t.Fatal(err) }
	receiptAfter, err := os.ReadFile(receiptPath)
	if err != nil { t.Fatal(err) }
	if !bytes.Equal(markerBefore, markerAfter) || !bytes.Equal(receiptBefore, receiptAfter) {
		t.Fatal("cancelled observation modified durable intent or receipt")
	}
	if other, err := BeginRuntimeMutation(e.receiptDir, task.ID, TaskKindConfigApply); err == nil {
		_ = other.Close()
		t.Fatal("cancelled observation lifted the persistent runtime fence")
	}
}
