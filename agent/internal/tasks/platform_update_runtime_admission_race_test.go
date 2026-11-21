package tasks

import (
	"os"
	"path/filepath"
	"testing"
)

// Demonstrates the cross-protocol TOCTOU gap without running systemd, an
// updater, or touching the production runtime-mutation directory. A detached
// updater's state-presence check is not a reservation of the shared lease.
func TestPlatformUpdateStateProbeDoesNotReserveRuntimeMutationLease(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "runtime-mutations")
	if RuntimeMutationStatePresent(dir) {
		t.Fatal("fresh runtime mutation directory unexpectedly exists")
	}

	// The worker has now observed no state, but holds no lock. Interleave
	// another Agent mutation before the worker crosses mutation_started.
	lease, err := BeginRuntimeMutation(dir,
		"11111111-1111-4111-8111-111111111111", TaskKindVPNCoreService)
	if err != nil {
		t.Fatalf("concurrent Agent mutation could not acquire shared lease: %v", err)
	}
	defer lease.Close()

	if !RuntimeMutationStatePresent(dir) {
		t.Fatal("concurrent mutation did not publish durable state")
	}
	if _, err := os.Stat(filepath.Join(dir, "mutation-inflight.json")); err != nil {
		t.Fatalf("concurrent mutation has no durable intent: %v", err)
	}
	if _, err := BeginRuntimeMutation(dir,
		"22222222-2222-4222-8222-222222222222", TaskKindConfigApply); err == nil {
		t.Fatal("a second cooperating Agent mutation acquired the occupied lease")
	}

	// The earlier negative predicate remains just an observation. The
	// detached worker must perform atomic admission at the actual mutation
	// boundary; rechecking a predicate alone cannot close this race.
}

func TestDetachedUpdateLeaseExcludesAgentAndSurvivesWorkerExit(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "runtime-mutations")
	updateID := "11111111-1111-4111-8111-111111111111"
	agentID := "22222222-2222-4222-8222-222222222222"

	workerLease, err := BeginRuntimeMutation(dir, updateID, TaskKindPlatformUpdate)
	if err != nil {
		t.Fatalf("detached worker failed to reserve shared admission: %v", err)
	}
	if lease, err := BeginRuntimeMutation(dir, agentID, TaskKindConfigApply); err == nil {
		lease.Close()
		t.Fatal("Agent mutation admitted while detached worker owns lease")
	}
	// A worker crash releases the kernel flock but not the durable intent.
	if err := workerLease.Close(); err != nil {
		t.Fatal(err)
	}
	if lease, err := BeginRuntimeMutation(dir, agentID, TaskKindVPNCoreService); err == nil {
		lease.Close()
		t.Fatal("Agent mutation admitted after detached worker lost its lease")
	}
	if _, err := os.Stat(filepath.Join(dir, "mutation-inflight.json")); err != nil {
		t.Fatalf("worker crash did not retain update admission evidence: %v", err)
	}
}

func TestCompletedDetachedUpdateLeaseAllowsNewAdmission(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "runtime-mutations")
	workerLease, err := BeginRuntimeMutation(dir,
		"11111111-1111-4111-8111-111111111111", TaskKindPlatformUpdate)
	if err != nil {
		t.Fatal(err)
	}
	if err := workerLease.Complete(); err != nil {
		t.Fatal(err)
	}
	if err := workerLease.Close(); err != nil {
		t.Fatal(err)
	}
	agentLease, err := BeginRuntimeMutation(dir,
		"22222222-2222-4222-8222-222222222222", TaskKindConfigApply)
	if err != nil {
		t.Fatalf("completed update blocked next Agent admission: %v", err)
	}
	defer agentLease.Close()
}

func TestDetachedUpdateFailedLeaseCompletionRetainsRecoveryFence(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "runtime-mutations")
	updateID := "11111111-1111-4111-8111-111111111111"
	agentID := "22222222-2222-4222-8222-222222222222"

	workerLease, err := BeginRuntimeMutation(dir, updateID, TaskKindPlatformUpdate)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a damaged durable intent after the updater has started.
	// A successful updater alone is not proof that recovery can be cleared.
	if err := os.WriteFile(filepath.Join(dir, "mutation-inflight.json"),
		[]byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := workerLease.Complete(); err == nil {
		t.Fatal("changed detached-worker intent was cleared")
	}
	if err := workerLease.Close(); err != nil {
		t.Fatal(err)
	}
	if lease, err := BeginRuntimeMutation(dir, agentID, TaskKindConfigApply); err == nil {
		lease.Close()
		t.Fatal("Agent admitted mutation despite damaged update recovery evidence")
	}
}

func TestPlatformUpdateIdlePreflightAfterCompletedLease(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "runtime-mutations")
	if !RuntimeMutationIdleForPlatformUpdate(dir) {
		t.Fatal("fresh runtime directory rejected")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("preflight created runtime directory unexpectedly: %v", err)
	}
	lease, err := BeginRuntimeMutation(dir,
		"11111111-1111-4111-8111-111111111111", TaskKindPlatformUpdate)
	if err != nil {
		t.Fatal(err)
	}
	if RuntimeMutationIdleForPlatformUpdate(dir) {
		t.Fatal("preflight admitted live update lease")
	}
	if err := lease.Complete(); err != nil {
		t.Fatal(err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	if !RuntimeMutationIdleForPlatformUpdate(dir) {
		t.Fatal("completed update left a permanent false-positive block")
	}
	if err := os.WriteFile(filepath.Join(dir, "unexpected-state"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if RuntimeMutationIdleForPlatformUpdate(dir) {
		t.Fatal("unknown runtime state must fail closed")
	}
}

func TestPlatformUpdatePreflightRejectsCrashedAndForeignAdmissionState(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "runtime-mutations")
	lease, err := BeginRuntimeMutation(dir,
		"11111111-1111-4111-8111-111111111111", TaskKindVPNCoreService)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate process exit: the kernel lock is gone, but intent persists.
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	if RuntimeMutationIdleForPlatformUpdate(dir) {
		t.Fatal("preflight ignored orphaned legacy mutation marker")
	}
	if lease, err := BeginRuntimeMutation(dir,
		"22222222-2222-4222-8222-222222222222", TaskKindPlatformUpdate); err == nil {
		lease.Close()
		t.Fatal("detached worker admitted over orphaned legacy mutation")
	}
}

func TestPlatformUpdatePreflightRejectsLegacyRemovalMarker(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "runtime-mutations")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "inflight.json"), []byte("unresolved"), 0600); err != nil {
		t.Fatal(err)
	}
	if RuntimeMutationIdleForPlatformUpdate(dir) {
		t.Fatal("preflight ignored legacy credential removal marker")
	}
}

func TestPlatformUpdatePreflightRejectsUnsafeRuntimeDirectory(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "target")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(parent, "runtime-mutations")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if RuntimeMutationIdleForPlatformUpdate(link) {
		t.Fatal("symlinked admission directory passed preflight")
	}
	if _, err := os.Stat(filepath.Join(target, "runtime.lock")); !os.IsNotExist(err) {
		t.Fatalf("unsafe preflight wrote through symlink: %v", err)
	}
}

func TestPlatformUpdatePreflightRejectsUnexpectedLockFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "runtime-mutations")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "runtime.lock"), []byte("lock"), 0644); err != nil {
		t.Fatal(err)
	}
	if RuntimeMutationIdleForPlatformUpdate(dir) {
		t.Fatal("unsafe shared lock permissions passed preflight")
	}
}

func TestSuccessPathCrashDoesNotBypassUnclearedAdmission(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "runtime-mutations")
	updateID := "11111111-1111-4111-8111-111111111111"
	lease, err := BeginRuntimeMutation(dir, updateID, TaskKindPlatformUpdate)
	if err != nil {
		t.Fatal(err)
	}
	// Models the shared admission side of a crash after an updater succeeds
	// but before the live worker calls Complete. Receipt persistence is
	// tested separately; this test proves orphaned admission stays closed.
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	if RuntimeMutationIdleForPlatformUpdate(dir) {
		t.Fatal("uncleared success-path intent was treated as idle")
	}
	if next, err := BeginRuntimeMutation(dir,
		"22222222-2222-4222-8222-222222222222", TaskKindConfigApply); err == nil {
		next.Close()
		t.Fatal("Agent admitted new mutation over uncleared update intent")
	}
	if next, err := BeginRuntimeMutation(dir,
		"33333333-3333-4333-8333-333333333333", TaskKindPlatformUpdate); err == nil {
		next.Close()
		t.Fatal("another update admitted over uncleared update intent")
	}
}

func TestSuccessfulReceiptAndOrphanedAdmissionRequireRecovery(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "runtime-mutations")
	store := testPlatformUpdateReceiptStore(t)
	updateID := "11111111-1111-4111-8111-111111111111"
	if _, err := store.CreatePrepared(updateID, "v1.2.3"); err != nil {
		t.Fatal(err)
	}
	lease, err := BeginRuntimeMutation(dir, updateID, TaskKindPlatformUpdate)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkMutationStarted(updateID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkSucceeded(updateID); err != nil {
		t.Fatal(err)
	}
	// Crash cut between durable success receipt and admission completion.
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	receipt, err := store.Read(updateID)
	if err != nil || receipt.Phase != PlatformUpdateReceiptSucceeded {
		t.Fatalf("durable success receipt lost: receipt=%+v err=%v", receipt, err)
	}
	if RuntimeMutationIdleForPlatformUpdate(dir) {
		t.Fatal("success receipt silently cleared orphaned cross-protocol admission")
	}
	if next, err := BeginRuntimeMutation(dir,
		"22222222-2222-4222-8222-222222222222", TaskKindConfigApply); err == nil {
		next.Close()
		t.Fatal("Agent mutation admitted despite orphaned successful update")
	}
	if _, err := store.CreatePrepared(updateID, "v1.2.3"); err == nil {
		t.Fatal("same-task update receipt allowed replay after success")
	}
}
