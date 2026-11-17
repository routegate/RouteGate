package heartbeat

import (
	"context"
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
