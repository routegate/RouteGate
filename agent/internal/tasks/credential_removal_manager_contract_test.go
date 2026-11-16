package tasks

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// This consumes actual PostgreSQL-backed Manager preparation bytes from the
// isolated Backend CI job, not a second handwritten Agent-only fixture.
// A fake local service generation proves parser/executor compatibility without
// exposing live Agent routes or restarting any real VPN process.
func TestCredentialRemovalManagerPreparedContract(t *testing.T) {
	path := os.Getenv("ROUTEGATE_TEST_REMOVAL_CONTRACT_FIXTURE")
	if path == "" {
		t.Skip("cross-module Manager/Agent contract fixture is opt-in")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("private Manager fixture unavailable")
	}
	var fixture struct {
		BaselineRuntime json.RawMessage `json:"baselineRuntime"`
		Task            ConfigTask       `json:"task"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal("invalid Manager candidate fixture")
	}
	task := fixture.Task
	if task.CredentialRemoval == nil || task.Kind != TaskKindCredentialRemoval {
		t.Fatal("Manager did not supply a typed scoped-removal candidate")
	}
	var envelope struct {
		SingBox json.RawMessage `json:"singBox"`
	}
	if err := json.Unmarshal(task.RenderedConfig, &envelope); err != nil {
		t.Fatal("invalid candidate envelope")
	}
	if _, err := verifyRemovalRuntimeDelta(fixture.BaselineRuntime,
		envelope.SingBox, *task.CredentialRemoval); err != nil {
		t.Fatal("real Manager candidate violates independent Agent runtime contract")
	}
	_, _, expectedHash, err := canonicalRemovalJSON(envelope.SingBox)
	if err != nil || expectedHash != task.CredentialRemoval.CandidateRuntimeHash {
		t.Fatal("Manager/Agent runtime digest disagreement")
	}
	dir := t.TempDir()
	active := filepath.Join(dir, "active.json")
	if err := os.WriteFile(active, fixture.BaselineRuntime, 0600); err != nil {
		t.Fatal(err)
	}
	adapter := &removalAdapter{generation: RuntimeGeneration{
		InvocationID: strings.Repeat("a", 32), PID: 100,
		StartedMonotonic: "1000",
	}}
	executor := NewCredentialRemovalExecutor(active, filepath.Join(dir, "receipts"),
		task.ServerID, task.AgentID, adapter, true)

	// Reject an altered signed-intent input without touching the local runtime.
	invalid := task
	request := *task.CredentialRemoval
	request.CandidateRuntimeHash = strings.Repeat("f", 64)
	invalid.CredentialRemoval = &request
	if _, err := executor.Execute(context.Background(), invalid); err == nil {
		t.Fatal("Agent accepted mutated Manager candidate digest")
	}
	if adapter.restarts != 0 {
		t.Fatal("invalid Manager input restarted sing-box")
	}
	unchanged, err := os.ReadFile(active)
	if err != nil || !bytes.Equal(unchanged, fixture.BaselineRuntime) {
		t.Fatal("invalid Manager input modified applied runtime")
	}

	report, err := executor.Execute(context.Background(), task)
	if err != nil || report.State != "succeeded" ||
		report.ActiveRuntimeHash != task.CredentialRemoval.CandidateRuntimeHash ||
		report.OperationID != task.CredentialRemoval.OperationID ||
		report.AccountID != task.CredentialRemoval.AccountID ||
		adapter.restarts != 1 {
		t.Fatal("Agent could not apply actual Manager-prepared candidate in disposable runtime", err)
	}
	current, err := os.ReadFile(active)
	if err != nil {
		t.Fatal(err)
	}
	_, _, hash, err := canonicalRemovalJSON(current)
	if err != nil || hash != task.CredentialRemoval.CandidateRuntimeHash {
		t.Fatal("Agent applied bytes differ from Manager's exact candidate runtime")
	}
	replay, err := executor.Execute(context.Background(), task)
	if err != nil || replay != report || adapter.restarts != 1 {
		t.Fatal("Manager candidate replay was not read-only", err)
	}
}
