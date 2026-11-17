package tasks

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestReadPlatformUpdateReconciliationLifecycle(t *testing.T) {
	store := testPlatformUpdateReceiptStore(t)
	taskID := "550e8400-e29b-41d4-a716-446655440000"
	if _, err := store.CreatePrepared(taskID, "v1.2.3"); err != nil {
		t.Fatal(err)
	}

	pending, err := readPlatformUpdateReconciliation(store, taskID, "v1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	if pending.State != PlatformUpdateReconciliationPending || pending.Code != "" || pending.MutationStarted {
		t.Fatalf("unexpected pending reconciliation: %+v", pending)
	}

	if _, err := store.MarkMutationStarted(taskID); err != nil {
		t.Fatal(err)
	}
	pending, err = readPlatformUpdateReconciliation(store, taskID, "v1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	if pending.State != PlatformUpdateReconciliationPending || !pending.MutationStarted {
		t.Fatalf("mutation_started projection is invalid: %+v", pending)
	}

	if _, err := store.MarkSucceeded(taskID); err != nil {
		t.Fatal(err)
	}
	succeeded, err := readPlatformUpdateReconciliation(store, taskID, "v1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	if succeeded.State != PlatformUpdateReconciliationSucceeded || !succeeded.MutationStarted || succeeded.TaskID != taskID || succeeded.TargetVersion != "v1.2.3" {
		t.Fatalf("unexpected success reconciliation: %+v", succeeded)
	}
}

func TestReconcilePreparedWithoutWorkerFailsBeforeMutation(t *testing.T) {
	store := testPlatformUpdateReceiptStore(t)
	taskID := "550e8400-e29b-41d4-a716-446655440000"
	if _, err := store.CreatePrepared(taskID, "v1.2.3"); err != nil {
		t.Fatal(err)
	}
	result, err := reconcilePlatformUpdateForManager(context.Background(), store, taskID, "v1.2.3", func(context.Context, string) (bool, error) {
		return false, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != PlatformUpdateReconciliationFailed || result.MutationStarted || result.Code != "detached_worker_missing" {
		t.Fatalf("unexpected prepared recovery: %+v", result)
	}
	receipt, err := store.Read(taskID)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Phase != PlatformUpdateReceiptFailed || receipt.MutationStarted {
		t.Fatalf("prepared receipt did not fail closed before mutation: %+v", receipt)
	}
}

func TestReconcileStartedWithoutWorkerBecomesOutcomeUnknown(t *testing.T) {
	store := testPlatformUpdateReceiptStore(t)
	taskID := "550e8400-e29b-41d4-a716-446655440000"
	if _, err := store.CreatePrepared(taskID, "v1.2.3"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkMutationStarted(taskID); err != nil {
		t.Fatal(err)
	}
	result, err := reconcilePlatformUpdateForManager(context.Background(), store, taskID, "v1.2.3", func(context.Context, string) (bool, error) {
		return false, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != PlatformUpdateReconciliationOutcomeUnknown || !result.MutationStarted || result.Code != "detached_worker_missing" {
		t.Fatalf("unexpected orphan recovery: %+v", result)
	}
}

func TestReconcileLiveWorkerKeepsPendingReceipt(t *testing.T) {
	store := testPlatformUpdateReceiptStore(t)
	taskID := "550e8400-e29b-41d4-a716-446655440000"
	if _, err := store.CreatePrepared(taskID, "v1.2.3"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkMutationStarted(taskID); err != nil {
		t.Fatal(err)
	}
	result, err := reconcilePlatformUpdateForManager(context.Background(), store, taskID, "v1.2.3", func(context.Context, string) (bool, error) {
		return true, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != PlatformUpdateReconciliationPending || !result.MutationStarted {
		t.Fatalf("live worker was terminalized: %+v", result)
	}
	receipt, err := store.Read(taskID)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Phase != PlatformUpdateReceiptMutationStarted {
		t.Fatalf("live worker receipt changed: %+v", receipt)
	}
}

func TestReconcileProbeFailureDoesNotChangeReceipt(t *testing.T) {
	store := testPlatformUpdateReceiptStore(t)
	taskID := "550e8400-e29b-41d4-a716-446655440000"
	if _, err := store.CreatePrepared(taskID, "v1.2.3"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkMutationStarted(taskID); err != nil {
		t.Fatal(err)
	}
	if _, err := reconcilePlatformUpdateForManager(context.Background(), store, taskID, "v1.2.3", func(context.Context, string) (bool, error) {
		return false, errors.New("systemd unavailable")
	}); err == nil {
		t.Fatal("worker probe failure was accepted")
	}
	receipt, err := store.Read(taskID)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Phase != PlatformUpdateReceiptMutationStarted {
		t.Fatalf("probe failure changed receipt: %+v", receipt)
	}
}

func TestParsePlatformUpdateWorkerState(t *testing.T) {
	for _, tc := range []struct {
		output string
		active bool
	}{
		{"LoadState=loaded\nActiveState=active\n", true},
		{"ActiveState=activating\nLoadState=loaded\n", true},
		{"LoadState=loaded\nActiveState=inactive\n", false},
		{"LoadState=loaded\nActiveState=failed\n", false},
		{"LoadState=not-found\nActiveState=inactive\n", false},
	} {
		active, err := parsePlatformUpdateWorkerState(tc.output)
		if err != nil {
			t.Fatalf("parsePlatformUpdateWorkerState(%q): %v", tc.output, err)
		}
		if active != tc.active {
			t.Fatalf("active=%v, want %v for %q", active, tc.active, tc.output)
		}
	}
	for _, output := range []string{"", "LoadState=loaded\n", "LoadState=masked\nActiveState=inactive\n", "LoadState=loaded\nActiveState=unknown\n"} {
		if _, err := parsePlatformUpdateWorkerState(output); err == nil {
			t.Fatalf("invalid worker state accepted: %q", output)
		}
	}
}

func TestReadPlatformUpdateReconciliationOutcomeUnknown(t *testing.T) {
	store := testPlatformUpdateReceiptStore(t)
	taskID := "550e8400-e29b-41d4-a716-446655440000"
	if _, err := store.CreatePrepared(taskID, "v1.2.3"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkMutationStarted(taskID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkOutcomeUnknown(taskID, "verified_updater_signaled"); err != nil {
		t.Fatal(err)
	}
	result, err := readPlatformUpdateReconciliation(store, taskID, "v1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	if result.State != PlatformUpdateReconciliationOutcomeUnknown || !result.MutationStarted || result.Code != "verified_updater_signaled" {
		t.Fatalf("unexpected unknown reconciliation: %+v", result)
	}
}

func TestReadPlatformUpdateReconciliationRejectsVersionMismatch(t *testing.T) {
	store := testPlatformUpdateReceiptStore(t)
	taskID := "550e8400-e29b-41d4-a716-446655440000"
	if _, err := store.CreatePrepared(taskID, "v1.2.3"); err != nil {
		t.Fatal(err)
	}
	if _, err := readPlatformUpdateReconciliation(store, taskID, "v1.2.4"); err == nil {
		t.Fatal("receipt was accepted for a different target version")
	}
}

func TestReadPlatformUpdateReconciliationRejectsNonCanonicalInputs(t *testing.T) {
	store := testPlatformUpdateReceiptStore(t)
	for _, tc := range []struct {
		taskID  string
		version string
	}{
		{"../escape", "v1.2.3"},
		{"550e8400-e29b-41d4-a716-446655440000", " v1.2.3"},
	} {
		if _, err := readPlatformUpdateReconciliation(store, tc.taskID, tc.version); err == nil {
			t.Fatalf("non-canonical input accepted: %+v", tc)
		}
	}
}

func TestReconcileSucceededReceiptNeverClearsOrphanedRuntimeAdmission(t *testing.T) {
	store := testPlatformUpdateReceiptStore(t)
	dir := filepath.Join(t.TempDir(), "runtime-mutations")
	taskID := "550e8400-e29b-41d4-a716-446655440000"
	if _, err := store.CreatePrepared(taskID, "v1.2.3"); err != nil {
		t.Fatal(err)
	}
	lease, err := BeginRuntimeMutation(dir, taskID, TaskKindPlatformUpdate)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkMutationStarted(taskID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkSucceeded(taskID); err != nil {
		t.Fatal(err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	probed := false
	result, err := reconcilePlatformUpdateForManager(context.Background(), store, taskID, "v1.2.3",
		func(context.Context, string) (bool, error) {
			probed = true
			return false, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if probed {
		t.Fatal("terminal receipt unexpectedly triggered worker probe")
	}
	if result.State != PlatformUpdateReconciliationSucceeded {
		t.Fatalf("durable terminal success was lost: %+v", result)
	}
	if RuntimeMutationIdleForPlatformUpdate(dir) {
		t.Fatal("Manager reconciliation silently cleared orphaned admission")
	}
	if next, err := BeginRuntimeMutation(dir,
		"11111111-1111-4111-8111-111111111111", TaskKindConfigApply); err == nil {
		next.Close()
		t.Fatal("reconciliation reopened admission without independent recovery")
	}
}
