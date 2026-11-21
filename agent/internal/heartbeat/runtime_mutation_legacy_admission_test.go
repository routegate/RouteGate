package heartbeat

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/ikaevus/routegate/agent/internal/tasks"
)

// The opt-in runtime fence intentionally leaves legacy service tasks working
// when no durable state exists. Unsupported task kinds must stay rejected even
// through that legacy path. Detached platform-update dispatch has a separate
// historical path and is not covered by this test.
func TestLegacyUnfencedDispatchCannotEnableDormantMutations(t *testing.T) {
	for _, tc := range []struct {
		name string
		kind string
	}{
		{name: "credential_removal", kind: tasks.TaskKindCredentialRemoval},
		{name: "unknown_kind", kind: "rg140_future_mutation"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, adapter, results := fencedRunnerFixture(t, tc.kind, 204)
			r.cfg.ExperimentalRuntimeMutationFencing = false
			r.runtimeMutationManagerReady = false

			if err := r.processNextTask(context.Background()); err == nil {
				t.Fatal("dormant task unexpectedly admitted by legacy dispatch")
			}
			if adapter.calls != 0 {
				t.Fatal("dormant task invoked VPN service adapter")
			}
			if results.Load() != 1 {
				t.Fatalf("unsupported task must be rejected once, got %d results", results.Load())
			}
			if _, err := os.Stat(r.runtimeMutationDir); !os.IsNotExist(err) {
				t.Fatalf("unsupported task created runtime mutation state: %v", err)
			}
		})
	}
}

func TestLegacyServiceDispatchSharesDetachedWorkerAdmission(t *testing.T) {
	r, adapter, results := fencedRunnerFixture(t, tasks.TaskKindVPNCoreService, 204)
	r.cfg.ExperimentalRuntimeMutationFencing = false
	r.runtimeMutationManagerReady = false
	adapter.run = func() error {
		if _, err := os.Stat(r.runtimeMutationDir + "/mutation-inflight.json"); err != nil {
			t.Errorf("legacy handler ran without durable admission: %v", err)
		}
		other, err := tasks.BeginRuntimeMutation(r.runtimeMutationDir,
			"44444444-4444-4444-8444-444444444444", tasks.TaskKindPlatformUpdate)
		if err == nil {
			other.Close()
			t.Error("detached worker admitted during legacy mutation")
		}
		return nil
	}
	if err := r.processNextTask(context.Background()); err != nil {
		t.Fatal(err)
	}
	if adapter.calls != 1 || results.Load() != 1 {
		t.Fatalf("legacy service dispatch calls=%d results=%d", adapter.calls, results.Load())
	}
	worker, err := tasks.BeginRuntimeMutation(r.runtimeMutationDir,
		"44444444-4444-4444-8444-444444444444", tasks.TaskKindPlatformUpdate)
	if err != nil {
		t.Fatalf("completed legacy mutation blocked detached worker: %v", err)
	}
	defer worker.Close()
}

func TestLegacyServiceDispatchBlockedByDetachedWorker(t *testing.T) {
	r, adapter, results := fencedRunnerFixture(t, tasks.TaskKindVPNCoreService, 204)
	r.cfg.ExperimentalRuntimeMutationFencing = false
	r.runtimeMutationManagerReady = false
	worker, err := tasks.BeginRuntimeMutation(r.runtimeMutationDir,
		"44444444-4444-4444-8444-444444444444", tasks.TaskKindPlatformUpdate)
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()
	if err := r.processNextTask(context.Background()); err == nil {
		t.Fatal("legacy dispatch entered occupied update admission")
	}
	if adapter.calls != 0 || results.Load() != 0 {
		t.Fatalf("blocked legacy dispatch calls=%d results=%d", adapter.calls, results.Load())
	}
}

func TestLegacyPlatformUpdateDispatchAfterCompletedMutation(t *testing.T) {
	r, _, _ := fencedRunnerFixture(t, tasks.TaskKindPlatformUpdate, 204)
	r.cfg.ExperimentalRuntimeMutationFencing = false
	r.runtimeMutationManagerReady = false
	lease, err := tasks.BeginRuntimeMutation(r.runtimeMutationDir,
		"44444444-4444-4444-8444-444444444444", tasks.TaskKindVPNCoreService)
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.Complete(); err != nil {
		t.Fatal(err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	called := false
	task := tasks.ConfigTask{ID: fencedTaskID, Kind: tasks.TaskKindPlatformUpdate, Operation: tasks.PlatformUpdateOperationDispatch}
	if err := r.withRuntimeMutation(context.Background(), task, func(context.Context) error {
		called = true
		return nil
	}); err != nil {
		t.Fatalf("completed legacy mutation blocked later update dispatch: %v", err)
	}
	if !called {
		t.Fatal("legacy update dispatcher was not invoked")
	}
}

func TestLegacyPlatformUpdateDispatchBlockedByUnresolvedMutation(t *testing.T) {
	r, _, _ := fencedRunnerFixture(t, tasks.TaskKindPlatformUpdate, 204)
	r.cfg.ExperimentalRuntimeMutationFencing = false
	r.runtimeMutationManagerReady = false
	lease, err := tasks.BeginRuntimeMutation(r.runtimeMutationDir,
		"44444444-4444-4444-8444-444444444444", tasks.TaskKindVPNCoreService)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	called := false
	task := tasks.ConfigTask{ID: fencedTaskID, Kind: tasks.TaskKindPlatformUpdate, Operation: tasks.PlatformUpdateOperationDispatch}
	if err := r.withRuntimeMutation(context.Background(), task, func(context.Context) error {
		called = true
		return nil
	}); err == nil || called {
		t.Fatal("legacy update dispatch bypassed unresolved mutation")
	}
}

func TestLegacyServiceErrorRetainsUncertainRuntimeFence(t *testing.T) {
	r, adapter, results := fencedRunnerFixture(t, tasks.TaskKindVPNCoreService, 204)
	r.cfg.ExperimentalRuntimeMutationFencing = false
	r.runtimeMutationManagerReady = false
	adapter.run = func() error {
		return errors.New("simulated service failure after runtime invocation")
	}
	if err := r.processNextTask(context.Background()); err == nil {
		t.Fatal("legacy handler failure was ignored")
	}
	if adapter.calls != 1 {
		t.Fatalf("legacy handler calls=%d, want 1", adapter.calls)
	}
	if _, err := os.Stat(r.runtimeMutationDir + "/mutation-inflight.json"); err != nil {
		t.Fatalf("ambiguous legacy error lost durable recovery fence: %v", err)
	}
	if err := r.processNextTask(context.Background()); !errors.Is(err, tasks.ErrRuntimeMutationBlocked) {
		t.Fatalf("legacy task retried after ambiguous error: %v", err)
	}
	if adapter.calls != 1 {
		t.Fatal("legacy handler was invoked twice after uncertain outcome")
	}
	if results.Load() > 1 {
		t.Fatalf("unexpected duplicate terminal reports: %d", results.Load())
	}
}

func TestLegacyCancelledBeforeHandlerReleasesAdmission(t *testing.T) {
	r, _, _ := fencedRunnerFixture(t, tasks.TaskKindVPNCoreService, 204)
	r.cfg.ExperimentalRuntimeMutationFencing = false
	r.runtimeMutationManagerReady = false
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	task := tasks.ConfigTask{ID: fencedTaskID, Kind: tasks.TaskKindVPNCoreService}
	if err := r.withRuntimeMutation(ctx, task, func(context.Context) error {
		called = true
		return nil
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-handler cancellation returned %v", err)
	}
	if called {
		t.Fatal("cancelled legacy handler was invoked")
	}
	if _, err := os.Stat(r.runtimeMutationDir + "/mutation-inflight.json"); !os.IsNotExist(err) {
		t.Fatalf("proven pre-handler cancellation retained false recovery fence: %v", err)
	}
	lease, err := tasks.BeginRuntimeMutation(r.runtimeMutationDir,
		"44444444-4444-4444-8444-444444444444", tasks.TaskKindPlatformUpdate)
	if err != nil {
		t.Fatalf("cancelled legacy admission blocked later update: %v", err)
	}
	defer lease.Close()
}
