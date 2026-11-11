package heartbeat

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/ikaevus/routegate/agent/internal/config"
)

// Regression: the newer traffic-accounting heartbeat must coexist with the
// opt-in mutation policy. Old #551 had no traffic outbox or this capability.
func TestFencedHeartbeatPreservesModernTrafficAccounting(t *testing.T) {
	for _, tc := range []struct {
		name, acknowledgedAgent string
		accepted               bool
		wantReady              bool
	}{
		{"matching_ack", fencedAgentID, true, true},
		{"missing_ack", fencedAgentID, false, false},
		{"wrong_identity", fencedServerID, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := make(chan map[string]any, 1)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if req.Method != http.MethodPost || req.URL.Path != "/api/v1/agent/heartbeat" ||
					req.Header.Get("Authorization") != "Bearer isolated-token" {
					http.Error(w, "unexpected heartbeat", http.StatusBadRequest)
					return
				}
				var body struct {
					Capabilities map[string]any `json:"capabilities"`
				}
				if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
					http.Error(w, "invalid JSON", http.StatusBadRequest)
					return
				}
				got <- body.Capabilities
				w.Header().Set("Content-Type", "application/json")
				if err := json.NewEncoder(w).Encode(map[string]any{
					"ok": true, "agentId": tc.acknowledgedAgent, "serverId": fencedServerID,
					"runtimeMutationFencingAccepted": tc.accepted,
				}); err != nil {
					t.Error(err)
				}
			}))
			defer srv.Close()

			r := NewRunner(config.Config{
				ManagerURL: srv.URL, AgentID: fencedAgentID, ServerID: fencedServerID,
				AgentToken: "isolated-token", TrafficCollectionEnabled: true,
				ExperimentalRuntimeMutationFencing: true,
			}, "", slog.New(slog.NewTextHandler(io.Discard, nil)))
			r.runtimeMutationDir = filepath.Join(t.TempDir(), "runtime-state")
			if err := r.sendHeartbeat(context.Background()); err != nil {
				t.Fatal(err)
			}
			if r.runtimeMutationManagerReady != tc.wantReady {
				t.Fatalf("manager fencing acceptance = %v; want %v", r.runtimeMutationManagerReady, tc.wantReady)
			}
			var capabilities map[string]any
			select {
			case capabilities = <-got:
			default:
				t.Fatal("heartbeat did not report capabilities")
			}
			if capabilities["runtimeMutationFencingV1"] != true {
				t.Fatal("opt-in fencing capability missing")
			}
			traffic, ok := capabilities["trafficAccounting"].(map[string]any)
			if !ok || traffic["enabled"] != true || traffic["source"] != "sing-box-v2ray-api" {
				t.Fatalf("modern traffic accounting capability lost: %#v", traffic)
			}
		})
	}
}
