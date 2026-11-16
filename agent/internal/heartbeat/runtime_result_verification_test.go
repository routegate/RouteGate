package heartbeat

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ikaevus/routegate/agent/internal/config"
	"github.com/ikaevus/routegate/agent/internal/tasks"
)

func TestRuntimeMutationVerifiedResultRetainsFence(t *testing.T) {
	var verifications atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			json.NewEncoder(w).Encode(map[string]any{"task": nil})
			return
		}
		if !strings.HasSuffix(r.URL.Path, "/verify") {
			w.WriteHeader(404)
			return
		}
		verifications.Add(1)
		body, _ := io.ReadAll(r.Body)
		digest := sha256.Sum256(body)
		json.NewEncoder(w).Encode(map[string]any{"schemaVersion": 1, "verified": true, "taskId": fencedTaskID, "agentId": fencedAgentID, "serverId": fencedServerID, "envelopeSha256": hex.EncodeToString(digest[:])})
	}))
	defer srv.Close()
	dir := filepath.Join(t.TempDir(), "state")
	lease, err := tasks.BeginRuntimeMutation(dir, fencedTaskID, tasks.TaskKindVPNCoreService)
	if err != nil {
		t.Fatal(err)
	}
	binding := tasks.RuntimeResultBinding{ManagerURL: srv.URL, AgentID: fencedAgentID, ServerID: fencedServerID}
	if err := lease.SaveResult(binding, fencedTaskID, []byte(`{"status":"succeeded","resultPayload":{"phase":"complete"}}`)); err != nil {
		t.Fatal(err)
	}
	lease.Close()
	r := NewRunner(config.Config{ManagerURL: srv.URL, AgentID: fencedAgentID, ServerID: fencedServerID, AgentToken: "isolated-token"}, "", slog.New(slog.NewTextHandler(io.Discard, nil)))
	r.runtimeMutationManagerReady = true
	r.runtimeMutationDir = dir
	for range 2 {
		if err := r.processNextTask(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if verifications.Load() != 1 {
		t.Fatal("verified receipt redelivered")
	}
	if _, err := os.Stat(filepath.Join(dir, "mutation-inflight.json")); err != nil {
		t.Fatal("result verification unlocked runtime", err)
	}
	if other, err := tasks.BeginRuntimeMutation(dir, fencedTaskID, tasks.TaskKindVPNCoreService); err == nil {
		other.Close()
		t.Fatal("historical evidence admitted a new runtime mutation")
	}
}
