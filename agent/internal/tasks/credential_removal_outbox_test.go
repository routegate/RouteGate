package tasks

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCredentialRemovalOutboxRecoversInterruptedMutationWithoutExecution(t *testing.T) {
	executor, task, adapter, _ := removalExecutorFixture(t)
	deadline := time.Now().Add(time.Minute)
	task.DeliveryExpiresAt = &deadline
	adapter.restartHook = func() error { panic("simulated worker exit") }
	func() {
		defer func() {
			if recover() == nil {
				t.Error("crash injection did not execute")
			}
		}()
		_, _ = executor.ExecuteHeld(context.Background(), task)
	}()
	fencePath := filepath.Join(executor.receiptDir, "inflight.json")
	fence, err := os.ReadFile(fencePath)
	if err != nil {
		t.Fatal(err)
	}
	active, err := os.ReadFile(executor.activePath)
	if err != nil {
		t.Fatal(err)
	}
	restarted := NewCredentialRemovalExecutor(executor.activePath, executor.receiptDir, executor.serverID, executor.agentID, adapter, true)
	report, err := restarted.PendingHeldRemovalReport(context.Background())
	if err != nil || report == nil || report.Code != "interrupted_or_failed_mutation" || report.State != "recovery_required" {
		t.Fatal("interrupted receipt did not become a durable recovery report", report, err)
	}
	replayed, err := restarted.PendingHeldRemovalReport(context.Background())
	if err != nil || replayed == nil || *replayed != *report {
		t.Fatal("interruption report changed on replay", replayed, err)
	}
	afterFence, _ := os.ReadFile(fencePath)
	afterActive, _ := os.ReadFile(executor.activePath)
	if !bytes.Equal(fence, afterFence) || !bytes.Equal(active, afterActive) || adapter.restarts != 1 {
		t.Fatal("outbox changed runtime or cleared its fence")
	}
	if err := restarted.AcknowledgeHeldRemovalReport(context.Background(), *report); err != nil {
		t.Fatal(err)
	}
	if pending, err := restarted.PendingHeldRemovalReport(context.Background()); err != nil || pending != nil {
		t.Fatal("acknowledged report remained pending", pending, err)
	}
	if _, err := os.Stat(fencePath); err != nil {
		t.Fatal("acknowledgement removed mutation fence", err)
	}
}

func TestCredentialRemovalOutboxWaitsForWorkerAndPreservesLegacyReceipts(t *testing.T) {
	executor, task, _, _ := removalExecutorFixture(t)
	if _, err := executor.Execute(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	if report, err := executor.PendingHeldRemovalReport(context.Background()); err != nil || report != nil {
		t.Fatal("legacy direct execution entered the held outbox", report, err)
	}
	lock, err := executor.lock()
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if _, err := executor.PendingHeldRemovalReport(context.Background()); err == nil {
		t.Fatal("outbox raced an admitted mutation worker")
	}
}

func TestCredentialRemovalOutboxAcknowledgesExactSuccessOnly(t *testing.T) {
	executor, task, adapter, _ := removalExecutorFixture(t)
	deadline := time.Now().Add(time.Minute)
	task.DeliveryExpiresAt = &deadline
	report, err := executor.ExecuteHeld(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := executor.PendingHeldRemovalReport(context.Background())
	if err != nil || pending == nil || *pending != report {
		t.Fatal(pending, err)
	}
	changed := report
	changed.After.PID++
	if err := executor.AcknowledgeHeldRemovalReport(context.Background(), changed); err == nil {
		t.Fatal("different report was acknowledged")
	}
	if err := executor.AcknowledgeHeldRemovalReport(context.Background(), report); err != nil {
		t.Fatal(err)
	}
	if err := executor.AcknowledgeHeldRemovalReport(context.Background(), report); err != nil {
		t.Fatal("idempotent acknowledgement failed", err)
	}
	if pending, err := executor.PendingHeldRemovalReport(context.Background()); err != nil || pending != nil || adapter.restarts != 1 {
		t.Fatal("acknowledgement caused execution or duplicate delivery", pending, err)
	}
}
