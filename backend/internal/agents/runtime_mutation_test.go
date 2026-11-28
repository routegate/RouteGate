package agents

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRuntimeMutationHeartbeatAcknowledgesPersistedPolicy(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		repo := &fakeAgentAPIRepository{heartbeatAgent: Agent{ID: "agent", ServerID: "node", Capabilities: Capabilities{"runtimeMutationFencingV1": enabled}}}
		req := httptest.NewRequest(http.MethodPost, "/api/v1/agent/heartbeat", strings.NewReader(`{"capabilities":{"runtimeMutationFencingV1":true}}`))
		req.Header.Set("Authorization", "Bearer rg_agent_isolated-token")
		res := httptest.NewRecorder()
		testAgentHandler(repo).Heartbeat(res, req)
		if res.Code != 200 {
			t.Fatal(res.Code, res.Body.String())
		}
		var body AgentHeartbeatResponse
		if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.RuntimeMutationFencingAccepted != enabled {
			t.Fatal("acknowledged request rather than persisted policy")
		}
	}
	for _, value := range []any{"true", 1, nil, map[string]any{}} {
		if (Capabilities{"runtimeMutationFencingV1": value}).RuntimeMutationFencingEnabled() {
			t.Fatal("invalid policy accepted")
		}
	}
}
