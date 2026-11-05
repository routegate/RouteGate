package tasks_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ikaevus/routegate/agent/internal/client"
	"github.com/ikaevus/routegate/agent/internal/tasks"
)

// Opt-in only: uses a disposable CI VM, a temporary systemd unit and loopback.
// The HTTP peer is a fixture. Production Manager dispatch remains disabled.
func TestCredentialRemovalSystemdRealityTraffic(t *testing.T) {
	tasks.RunCredentialRemovalSystemdRealityFixture(t, executeRemovalViaHTTP)
}

func executeRemovalViaHTTP(t *testing.T, ctx context.Context, task tasks.ConfigTask, executor tasks.CredentialRemovalExecutor) (tasks.CredentialRemovalReport, error) {
	t.Helper()
	const token = "isolated-removal-agent"
	var zero tasks.CredentialRemovalReport
	resultPath := "/api/v1/agent/tasks/" + task.ID + "/result"
	received := make(chan tasks.CredentialRemovalReport, 1)
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/agent/tasks/next":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(client.NextWorkResponse{Task: &task})
		case r.Method == http.MethodPost && r.URL.Path == resultPath:
			body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 8192))
			var envelope struct {
				Status        string                        `json:"status"`
				ResultPayload tasks.CredentialRemovalReport `json:"resultPayload"`
			}
			decoder := json.NewDecoder(bytes.NewReader(body))
			decoder.DisallowUnknownFields()
			if err != nil || decoder.Decode(&envelope) != nil || decoder.Decode(new(any)) != io.EOF ||
				envelope.Status != "succeeded" || bytes.Contains(body, []byte(task.CredentialRemoval.VLESSUUID)) {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			select {
			case received <- envelope.ResultPayload:
				w.WriteHeader(http.StatusNoContent)
			default:
				w.WriteHeader(http.StatusConflict)
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer peer.Close()

	agent := client.New(peer.URL)
	work, err := agent.NextWork(ctx, token)
	if err != nil || work.Task == nil || work.RecoveryChallenge != nil {
		return zero, errors.New("Agent did not receive the isolated removal task")
	}
	report, err := executor.Execute(ctx, *work.Task)
	if err != nil {
		return report, err
	}
	data, err := json.Marshal(report)
	if err != nil {
		return zero, err
	}
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		return zero, err
	}
	if err := agent.CompleteTaskSucceeded(client.WithStrictTaskAcknowledgement(ctx), token, work.Task.ID, payload); err != nil {
		return zero, err
	}
	select {
	case got := <-received:
		if got != report {
			return zero, errors.New("Agent result changed during HTTP delivery")
		}
	default:
		return zero, errors.New("isolated HTTP peer did not receive the result")
	}
	t.Log("Agent fetched scoped removal via authenticated HTTP and returned exact credential-free evidence")
	return report, nil
}
