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
	"time"

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
	const removalPath = "/api/v1/agent/credential-removals/"
	deadline := time.Now().Add(30 * time.Second).UTC()
	task.DeliveryExpiresAt = &deadline
	resultPath := removalPath + task.ID + "/result"
	received := make(chan tasks.CredentialRemovalReport, 1)
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == removalPath+"claim":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(client.NextWorkResponse{Task: &task})
		case r.Method == http.MethodPost && r.URL.Path == resultPath:
			body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 8192))
			var report tasks.CredentialRemovalReport
			decoder := json.NewDecoder(bytes.NewReader(body))
			decoder.DisallowUnknownFields()
			if err != nil || decoder.Decode(&report) != nil || decoder.Decode(new(any)) != io.EOF ||
				report.State != "succeeded" || bytes.Contains(body, []byte(task.CredentialRemoval.VLESSUUID)) {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			select {
			case received <- report:
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
	report, err := agent.ProcessHeldRemoval(ctx, token, executor)
	if err != nil {
		return zero, err
	}
	if report == nil {
		return zero, errors.New("Agent did not receive the isolated removal task")
	}
	select {
	case got := <-received:
		if got != *report {
			return zero, errors.New("Agent result changed during HTTP delivery")
		}
	default:
		return zero, errors.New("isolated HTTP peer did not receive the result")
	}
	t.Log("Agent fetched scoped removal via authenticated HTTP and returned exact credential-free evidence")
	return *report, nil
}

func TestCredentialRemovalHTTPPreservesRestartFailureAcrossLostAcknowledgement(t *testing.T) {
	var task tasks.ConfigTask
	var original []byte
	results := 0
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer isolated-failure-agent" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/api/v1/agent/credential-removals/claim" {
			_ = json.NewEncoder(w).Encode(client.NextWorkResponse{Task: &task})
			return
		}
		if r.URL.Path != "/api/v1/agent/credential-removals/"+task.ID+"/result" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16384))
		var report tasks.CredentialRemovalReport
		if err != nil || json.Unmarshal(body, &report) != nil || report.State != "recovery_required" ||
			report.Code != "restart_failed" || bytes.Contains(body, []byte("private-command-output")) ||
			bytes.Contains(body, []byte(task.CredentialRemoval.VLESSUUID)) {
			t.Error("invalid or secret-bearing failure report")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		results++
		if results == 1 {
			original = bytes.Clone(body)
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if !bytes.Equal(body, original) {
			t.Error("lost acknowledgement changed the failure report")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer peer.Close()
	agent := client.New(peer.URL)
	tasks.RunCredentialRemovalFailureHTTPFixture(t, func(work tasks.ConfigTask, executor tasks.CredentialRemovalExecutor) (*tasks.CredentialRemovalReport, error) {
		task = work
		return agent.ProcessHeldRemoval(context.Background(), "isolated-failure-agent", executor)
	})
	if results != 2 {
		t.Fatal("failure report was not delivered and replayed", results)
	}
}
