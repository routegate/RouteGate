package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ikaevus/routegate/agent/internal/tasks"
)

func TestRuntimeMutationResultVerification(t *testing.T) {
	for _, mode := range []string{"verified", "unverified", "digest", "agent", "server", "task", "schema", "unsupported", "unauthorized", "malformed", "manager"} {
		t.Run(mode, func(t *testing.T) {
			data := []byte(`{"status":"succeeded","resultPayload":{"count":9007199254740993}}`)
			digest := sha256.Sum256(data)
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Header.Get("Authorization") != "Bearer isolated-token" {
					t.Error("missing authentication")
				}
				body, _ := io.ReadAll(r.Body)
				if string(body) != string(data) {
					t.Error("result changed in transit")
				}
				if !strings.HasSuffix(r.URL.Path, "/verify") {
					w.WriteHeader(404)
					return
				}
				result := map[string]any{"schemaVersion": 1, "verified": true, "taskId": "job", "agentId": "agent", "serverId": "node", "envelopeSha256": hex.EncodeToString(digest[:])}
				switch mode {
				case "unverified":
					result["verified"] = false
				case "digest":
					result["envelopeSha256"] = "wrong"
				case "agent":
					result["agentId"] = "other"
				case "server":
					result["serverId"] = "other"
				case "task":
					result["taskId"] = "other"
				case "schema":
					result["schemaVersion"] = 2
				case "unsupported":
					w.WriteHeader(404)
					return
				case "unauthorized":
					w.WriteHeader(401)
					return
				case "malformed":
					w.Write([]byte("{"))
					return
				}
				json.NewEncoder(w).Encode(result)
			}))
			defer srv.Close()
			binding := tasks.RuntimeResultBinding{ManagerURL: srv.URL, AgentID: "agent", ServerID: "node"}
			if mode == "manager" {
				binding.ManagerURL += "/other"
			}
			err := New(srv.URL).ReplayTaskResult(context.Background(), "isolated-token", "job", data, binding)
			if (err == nil) != (mode == "verified") {
				t.Fatal("incorrect evidence decision", err)
			}
			want := 2
			if mode == "manager" {
				want = 0
			}
			if calls != want {
				t.Fatal("unexpected requests", calls)
			}
		})
	}
}
