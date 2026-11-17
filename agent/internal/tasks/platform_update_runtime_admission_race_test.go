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
