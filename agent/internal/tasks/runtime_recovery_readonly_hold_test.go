package tasks

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRuntimeRecoveryReadOnlyHoldV1KeepsLockAndDurableFence(t *testing.T) {
	e, task, adapter, binding := recoveryFixture(t)
	markerPath := filepath.Join(e.receiptDir, "mutation-inflight.json")
	receiptPath := filepath.Join(e.receiptDir, runtimeResultFile)
	paths := []string{markerPath, receiptPath, e.activePath}
	before := make([][]byte, len(paths))
	for i, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		before[i] = data
	}

	hold, err := beginRuntimeRecoveryReadOnlyHoldV1(
		context.Background(), e.receiptDir, binding, task.ID, e.activePath, adapter,
	)
	if err != nil || hold == nil || hold.lock == nil {
		t.Fatalf("read-only exclusive review unavailable: %v", err)
	}
	defer hold.Close()
	if hold.observation.TaskID != task.ID ||
		hold.observation.MarkerHash != markerDigest(before[0]) ||
		hold.observation.ReceiptHash != markerDigest(before[1]) ||
		hold.observation.Witness.Generation != adapter.generation {
		t.Fatal("held observation was not freshly derived from persisted state")
	}
	// An already held Agent mutation flock is not re-entrant. Neither
	// another reviewer nor the public observation may bypass this lock.
	if other, err := lockRuntimeMutation(e.receiptDir); err == nil {
		_ = other.Close()
		t.Fatal("parallel local review entered held mutation lock")
	}
	if other, err := beginRuntimeRecoveryReadOnlyHoldV1(
		context.Background(), e.receiptDir, binding, task.ID, e.activePath, adapter,
	); err == nil {
		_ = other.Close()
		t.Fatal("parallel read-only recovery hold admitted")
	}
	if _, err := ObserveRuntimeRecovery(
		context.Background(), e.receiptDir, binding, task.ID, e.activePath, adapter,
	); !errors.Is(err, ErrRuntimeMutationBlocked) {
		t.Fatal("ordinary recovery observation bypassed held flock", err)
	}
	if adapter.restarts != 1 || adapter.validations != 0 {
		t.Fatal("lock-held observation restarted or validated the runtime")
	}
	for i, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(data, before[i]) {
			t.Fatalf("read-only hold changed durable file %s: %v", path, err)
		}
	}

	if err := hold.Close(); err != nil {
		t.Fatal(err)
	}
	// Close ends the short-lived lock only; it must not remove the durable
	// intent marker or authorize a new task after the observed window.
	for i, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(data, before[i]) {
			t.Fatalf("Close altered durable file %s: %v", path, err)
		}
	}
	if freshLock, err := lockRuntimeMutation(e.receiptDir); err != nil {
		t.Fatal("read-only holder leaked local lock", err)
	} else {
		_ = freshLock.Close()
	}
	if mutation, err := BeginRuntimeMutation(e.receiptDir, task.ID, TaskKindVPNCoreService); err == nil {
		_ = mutation.Close()
		t.Fatal("closing the review removed durable runtime fence")
	}
	if err := hold.Close(); err != nil {
		t.Fatal("closing the review twice should not mutate the runtime", err)
	}
}

func TestRuntimeRecoveryReadOnlyHoldV1FailClosedAndDoesNotLeakLock(t *testing.T) {
	for _, mode := range []string{"cancelled", "bad_receipt", "other_process", "changed_config", "busy_lock"} {
		t.Run(mode, func(t *testing.T) {
			e, task, adapter, binding := recoveryFixture(t)
			ctx := context.Background()
			switch mode {
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "bad_receipt":
				if err := os.WriteFile(
					filepath.Join(e.receiptDir, runtimeResultFile), []byte("{}"), 0600,
				); err != nil {
					t.Fatal(err)
				}
			case "other_process":
				adapter.generation.InvocationID = strings.Repeat("c", 32)
			case "changed_config":
				if err := os.WriteFile(e.activePath, []byte(`{"inbounds":[]}`), 0600); err != nil {
					t.Fatal(err)
				}
			case "busy_lock":
				lock, err := lockRuntimeMutation(e.receiptDir)
				if err != nil {
					t.Fatal(err)
				}
				defer lock.Close()
			}
			hold, err := beginRuntimeRecoveryReadOnlyHoldV1(
				ctx, e.receiptDir, binding, task.ID, e.activePath, adapter,
			)
			if !errors.Is(err, ErrRuntimeMutationBlocked) || hold != nil {
				if hold != nil {
					_ = hold.Close()
				}
				t.Fatalf("unsafe review admitted: %v", err)
			}
			if runtimeMarkerAbsent(filepath.Join(e.receiptDir, "mutation-inflight.json")) {
				t.Fatal("failed review erased protective marker")
			}
			if mode != "busy_lock" {
				lock, err := lockRuntimeMutation(e.receiptDir)
				if err != nil {
					t.Fatal("failed review leaked exclusive flock", err)
				}
				_ = lock.Close()
			}
		})
	}
}

func TestRuntimeRecoveryReadOnlyHoldV1NoImplicitStateCreation(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "missing")
	hold, err := beginRuntimeRecoveryReadOnlyHoldV1(
		context.Background(), dir, RuntimeResultBinding{}, "11111111-1111-4111-8111-111111111111",
		filepath.Join(dir, "active.json"), nil,
	)
	if !errors.Is(err, ErrRuntimeMutationBlocked) || hold != nil {
		t.Fatal("missing durable state was accepted", err)
	}
	if _, err := os.Lstat(dir); !os.IsNotExist(err) {
		t.Fatal("read-only review unexpectedly created runtime state", err)
	}
}
