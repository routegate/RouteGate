package heartbeat

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ikaevus/routegate/agent/internal/tasks"
)

// An HTTP 404 after a lost acknowledgement is not positive evidence of
// accepted completion. Replaying a saved result must never rerun the handler.
func TestFencedDurableReplayRefusesHistorical404AndRetainsFence(t *testing.T) {
	r, adapter, attempts := fencedRunnerFixture(t, tasks.TaskKindVPNCoreService, 400, 404, 204)
	if err := r.processNextTask(context.Background()); !errors.Is(err, errRuntimeMutationRecovery) {
		t.Fatalf("first uncertain completion: %v", err)
	}
	if adapter.calls != 1 || attempts.Load() != 1 {
		t.Fatalf("first mutation/result attempts: %d/%d", adapter.calls, attempts.Load())
	}

	fresh := NewRunner(r.cfg, "", r.logger)
	fresh.runtimeMutationDir = r.runtimeMutationDir
	fresh.runtimeMutationManagerReady = true
	fresh.vpnCoreAdapter = adapter

	if err := fresh.processNextTask(context.Background()); !errors.Is(err, tasks.ErrRuntimeMutationBlocked) {
		t.Fatalf("404 replay unexpectedly unlocked: %v", err)
	}
	if attempts.Load() != 2 || adapter.calls != 1 {
		t.Fatal("404 caused runtime execution or unexpected result retry")
	}

	receiptPath := filepath.Join(r.runtimeMutationDir, "mutation-result.json")
	checkReceipt := func(wantAcknowledged bool) {
		t.Helper()
		data, err := os.ReadFile(receiptPath)
		if err != nil {
			t.Fatal(err)
		}
		var receipt struct {
			Acknowledged bool `json:"acknowledged"`
		}
		if err := json.Unmarshal(data, &receipt); err != nil {
			t.Fatal(err)
		}
		if receipt.Acknowledged != wantAcknowledged {
			t.Fatalf("receipt acknowledged=%v, want %v", receipt.Acknowledged, wantAcknowledged)
		}
		if strings.Contains(string(data), "isolated-token") {
			t.Fatal("Agent bearer token leaked to receipt")
		}
	}
	checkReceipt(false)

	if err := fresh.processNextTask(context.Background()); !errors.Is(err, tasks.ErrRuntimeMutationBlocked) {
		t.Fatalf("positive receipt delivery must still retain recovery fence: %v", err)
	}
	if attempts.Load() != 3 || adapter.calls != 1 {
		t.Fatal("positive acknowledgement repeated runtime mutation")
	}
	checkReceipt(true)
	if _, err := os.Stat(filepath.Join(r.runtimeMutationDir, "mutation-inflight.json")); err != nil {
		t.Fatal("historical result replay cleared runtime intent:", err)
	}

	// No second delivery after durable acknowledgement; no subsequent mutation.
	if err := fresh.processNextTask(context.Background()); !errors.Is(err, tasks.ErrRuntimeMutationBlocked) {
		t.Fatalf("acknowledged history gave mutation admission: %v", err)
	}
	if attempts.Load() != 3 || adapter.calls != 1 {
		t.Fatal("acknowledged receipt was resent or runtime handler was retried")
	}
}
