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
