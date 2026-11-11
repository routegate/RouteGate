package heartbeat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ikaevus/routegate/agent/internal/config"
	"github.com/ikaevus/routegate/agent/internal/tasks"
)

func TestFencedDispatchDurableResultProcessDeath(t *testing.T) {
	newRunner := func(url, dir string) (*Runner, *fencedServiceAdapter) {
		r := NewRunner(config.Config{ManagerURL: url, AgentID: fencedAgentID, ServerID: fencedServerID, AgentToken: "isolated-token", ServiceControlEnabled: true, ExperimentalRuntimeMutationFencing: true}, "", slog.New(slog.NewTextHandler(io.Discard, nil)))
		r.runtimeMutationDir = dir
		r.runtimeMutationManagerReady = true
		a := &fencedServiceAdapter{}
		r.vpnCoreAdapter = a
		return r, a
	}
	if dir := os.Getenv("RG_TEST_RESULT_CHILD_DIR"); dir != "" {
		r, _ := newRunner(os.Getenv("RG_TEST_RESULT_CHILD_URL"), dir)
		_ = r.processNextTask(context.Background())
		os.Exit(3) // Parent must kill us before result acknowledgement.
	}
	dir := filepath.Join(t.TempDir(), "state")
	received := make(chan []byte, 1)
	killed := make(chan struct{})
	var posts atomic.Int32
	replayed := make(chan []byte, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get("Authorization") != "Bearer isolated-token" {
			t.Error("missing authentication")
		}
		if req.Method == http.MethodGet {
			if posts.Load() > 0 {
				json.NewEncoder(w).Encode(map[string]any{"task": nil})
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"task": tasks.ConfigTask{ID: fencedTaskID, AgentID: fencedAgentID, ServerID: fencedServerID, Kind: tasks.TaskKindVPNCoreService, Status: "in_progress", Operation: "restart"}})
			return
		}
		data, _ := io.ReadAll(req.Body)
		if posts.Add(1) == 1 {
			if _, err := os.Stat(filepath.Join(dir, "mutation-result.json")); err != nil {
				t.Error("HTTP preceded durable result", err)
			}
			received <- data
			<-killed
		} else {
			replayed <- data
		}
		w.WriteHeader(204)
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestFencedDispatchDurableResultProcessDeath$")
	cmd.Env = append(os.Environ(), "RG_TEST_RESULT_CHILD_DIR="+dir, "RG_TEST_RESULT_CHILD_URL="+srv.URL)
	if err := cmd.Start(); err != nil {
		close(killed)
		t.Fatal(err)
	}
	var original []byte
	select {
	case original = <-received:
	case <-ctx.Done():
		close(killed)
		cmd.Wait()
		t.Fatal("child never delivered saved result")
	}
	if err := cmd.Process.Kill(); err != nil {
		close(killed)
		t.Fatal(err)
	}
	_ = cmd.Wait()
	close(killed)
	r, adapter := newRunner(srv.URL, dir)
	if err := r.processNextTask(context.Background()); err != nil {
		t.Fatal(err)
	}
	if adapter.calls != 0 || posts.Load() != 2 || !bytes.Equal(original, <-replayed) {
		t.Fatal("restart did not replay the exact report without runtime execution")
	}
	if _, err := os.Stat(filepath.Join(dir, "mutation-inflight.json")); err != nil {
		t.Fatal("replay without a polled task cleared intent", err)
	}
}

func TestFencedDispatchDurableResultRestart(t *testing.T) {
	r, adapter, results := fencedRunnerFixture(t, tasks.TaskKindVPNCoreService, 400, 204)
	if err := r.processNextTask(context.Background()); !errors.Is(err, errRuntimeMutationRecovery) {
		t.Fatal(err)
	}
	path := filepath.Join(r.runtimeMutationDir, "mutation-result.json")
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("missing private durable result", err)
	}
	fresh := NewRunner(r.cfg, "", r.logger)
	fresh.cfg.ExperimentalRuntimeMutationFencing = false
	fresh.runtimeMutationDir = r.runtimeMutationDir
	fresh.vpnCoreAdapter = adapter
	// No replay before Manager's identity-bound acknowledgement.
	if err := fresh.processNextTask(context.Background()); !errors.Is(err, tasks.ErrRuntimeMutationBlocked) {
		t.Fatal(err)
	}
	if results.Load() != 1 {
		t.Fatal("replayed without Manager negotiation")
	}
	fresh.runtimeMutationManagerReady = true
	for range 2 {
		if err := fresh.processNextTask(context.Background()); !errors.Is(err, tasks.ErrRuntimeMutationBlocked) {
			t.Fatal(err)
		}
	}
	if adapter.calls != 1 || results.Load() != 2 {
		t.Fatal("runtime repeated or acknowledged result resent", adapter.calls, results.Load())
	}
	if _, err := os.Stat(filepath.Join(r.runtimeMutationDir, "mutation-inflight.json")); err != nil {
		t.Fatal("historical delivery unlocked runtime", err)
	}
}

func TestFencedDispatchDurableResultWriteFailure(t *testing.T) {
	r, adapter, results := fencedRunnerFixture(t, tasks.TaskKindVPNCoreService, 204)
	adapter.run = func() error {
		// Simulate an unavailable receipt destination after runtime execution.
		return os.Mkdir(filepath.Join(r.runtimeMutationDir, "mutation-result.json"), 0700)
	}
	if err := r.processNextTask(context.Background()); !errors.Is(err, errRuntimeMutationRecovery) {
		t.Fatal(err)
	}
	if adapter.calls != 1 || results.Load() != 0 {
		t.Fatal("reported without durable result")
	}
}
