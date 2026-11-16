package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/ikaevus/routegate/agent/internal/platform"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

const removalAccount = "11111111-1111-4111-8111-111111111111"
const removalUUID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"

type removalAdapter struct {
	VPNCoreAdapter
	generation                              RuntimeGeneration
	restarts, validations                   int
	validationHook, restartHook, healthHook func() error
}

func (a *removalAdapter) Descriptor() platform.VPNCoreAdapterDescriptor {
	return platform.VPNCoreAdapterDescriptor{Core: "sing-box", Protocol: "vless"}
}
func (a *removalAdapter) ObserveGeneration(context.Context, string) (RuntimeGeneration, error) {
	return a.generation, nil
}
func (a *removalAdapter) IsEnabled(context.Context) (ServiceResult, error) {
	return ServiceResult{}, nil
}
func (a *removalAdapter) Validate(context.Context, string) (ValidationResult, error) {
	a.validations++
	if a.validationHook != nil {
		return ValidationResult{}, a.validationHook()
	}
	return ValidationResult{}, nil
}
func (a *removalAdapter) Restart(context.Context) (ServiceResult, error) {
	a.restarts++
	if a.restartHook != nil {
		return ServiceResult{}, a.restartHook()
	}
	a.generation = RuntimeGeneration{InvocationID: strings.Repeat("b", 32), PID: 200, StartedMonotonic: "2000"}
	return ServiceResult{}, nil
}
func (a *removalAdapter) CheckHealth(context.Context, string) (ListenerHealthResult, error) {
	if a.healthHook != nil {
		return ListenerHealthResult{Port: 443}, a.healthHook()
	}
	return ListenerHealthResult{Port: 443}, nil
}
func removalExecutorFixture(t *testing.T) (CredentialRemovalExecutor, ConfigTask, *removalAdapter, []byte) {
	t.Helper()
	baseline := []byte(`{"log":{"level":"error"},"inbounds":[{"type":"vless","tag":"vless-in","listen":"127.0.0.1","listen_port":443,"users":[{"name":"` + removalAccount + `","uuid":"` + removalUUID + `"},{"name":"22222222-2222-4222-8222-222222222222","uuid":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"}],"tls":{"enabled":true,"reality":{"enabled":true,"private_key":"DO-NOT-LOG"}}},{"type":"shadowsocks","tag":"ss-in","password":"OTHER-SECRET"}],"outbounds":[{"type":"direct"}]}`)
	object, _, before, err := canonicalRemovalJSON(baseline)
	if err != nil {
		t.Fatal(err)
	}
	inbound := object["inbounds"].([]any)[0].(map[string]any)
	inbound["users"] = inbound["users"].([]any)[1:]
	candidate, _ := json.Marshal(object)
	_, _, after, _ := canonicalRemovalJSON(candidate)
	node := "33333333-3333-4333-8333-333333333333"
	agent := "44444444-4444-4444-8444-444444444444"
	envelope, _ := json.Marshal(map[string]any{"schemaVersion": "routegate.config.v1", "server": map[string]string{"id": node}, "agent": map[string]string{"id": agent}, "singBox": object})
	task := ConfigTask{ID: "55555555-5555-4555-8555-555555555555", Kind: TaskKindCredentialRemoval, ServerID: node, AgentID: agent, ConfigVersionID: "66666666-6666-4666-8666-666666666666", Action: "apply", Status: "in_progress", ConfigHash: strings.Repeat("c", 64), RenderedConfig: envelope, CredentialRemoval: &CredentialRemovalRequest{SchemaVersion: 1, OperationID: "77777777-7777-4777-8777-777777777777", AccountID: removalAccount, BaselineVersionID: "88888888-8888-4888-8888-888888888888", BaselineRuntimeHash: before, CandidateRuntimeHash: after, VLESSUUID: removalUUID, SharedProcessRestartAcknowledged: true}}
	dir := t.TempDir()
	active := filepath.Join(dir, "config.json")
	if err := os.WriteFile(active, baseline, 0600); err != nil {
		t.Fatal(err)
	}
	adapter := &removalAdapter{generation: RuntimeGeneration{InvocationID: strings.Repeat("a", 32), PID: 100, StartedMonotonic: "1000"}}
	return NewCredentialRemovalExecutor(active, filepath.Join(dir, "receipts"), node, agent, adapter, true), task, adapter, baseline
}
func TestCredentialRemovalSuccessAndReplay(t *testing.T) {
	e, task, a, _ := removalExecutorFixture(t)
	report, err := e.Execute(context.Background(), task)
	if err != nil {
		t.Fatal(err, report)
	}
	if report.State != "succeeded" || report.ActiveRuntimeHash != task.CredentialRemoval.CandidateRuntimeHash || a.restarts != 1 || report.Before == report.After {
		t.Fatal(report, a)
	}
	replay, err := e.Execute(context.Background(), task)
	if err != nil || replay != report || a.restarts != 1 || a.validations != 1 {
		t.Fatal(replay, err, a)
	}
	data, _ := json.Marshal(report)
	receipt, _ := os.ReadFile(filepath.Join(e.receiptDir, task.CredentialRemoval.OperationID+".json"))
	for _, secret := range []string{removalUUID, "DO-NOT-LOG", "OTHER-SECRET", e.activePath} {
		if strings.Contains(string(data)+string(receipt), secret) {
			t.Fatal("secret in evidence")
		}
	}
	if _, err := os.Stat(filepath.Join(e.receiptDir, "inflight.json")); !os.IsNotExist(err) {
		t.Fatal("completed fence retained", err)
	}
	a.generation.InvocationID = strings.Repeat("d", 32)
	replay, err = e.Execute(context.Background(), task)
	if err == nil || replay.State != "recovery_required" || a.restarts != 1 {
		t.Fatal(replay, err)
	}
}
func TestCredentialRemovalPreflightNoMutation(t *testing.T) {
	cases := map[string]func(*CredentialRemovalExecutor, *ConfigTask, *removalAdapter){
		"wrong agent": func(e *CredentialRemovalExecutor, c *ConfigTask, a *removalAdapter) { c.AgentID = removalAccount },
		"no acknowledgement": func(e *CredentialRemovalExecutor, c *ConfigTask, a *removalAdapter) {
			c.CredentialRemoval.SharedProcessRestartAcknowledged = false
		},
		"service disabled": func(e *CredentialRemovalExecutor, c *ConfigTask, a *removalAdapter) { e.serviceControl = false },
		"hash mismatch": func(e *CredentialRemovalExecutor, c *ConfigTask, a *removalAdapter) {
			c.CredentialRemoval.BaselineRuntimeHash = strings.Repeat("f", 64)
		},
		"wrong account": func(e *CredentialRemovalExecutor, c *ConfigTask, a *removalAdapter) {
			c.CredentialRemoval.AccountID = c.AgentID
		},
		"validation": func(e *CredentialRemovalExecutor, c *ConfigTask, a *removalAdapter) {
			a.validationHook = func() error { return errors.New("DO-NOT-LOG") }
		},
		"generation race": func(e *CredentialRemovalExecutor, c *ConfigTask, a *removalAdapter) {
			a.validationHook = func() error { a.generation.PID++; return nil }
		},
		"unsafe mode": func(e *CredentialRemovalExecutor, c *ConfigTask, a *removalAdapter) { _ = os.Chmod(e.activePath, 0666) },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			e, task, a, before := removalExecutorFixture(t)
			change(&e, &task, a)
			report, err := e.Execute(context.Background(), task)
			after, _ := os.ReadFile(e.activePath)
			if err == nil || report.State == "succeeded" || a.restarts != 0 || string(before) != string(after) || strings.Contains(err.Error(), "DO-NOT-LOG") {
				t.Fatal(report, err, a.restarts)
			}
		})
	}
}
func TestCredentialRemovalFailureFenceNoRollback(t *testing.T) {
	for _, phase := range []string{"restart", "generation", "health", "file changed"} {
		t.Run(phase, func(t *testing.T) {
			e, task, a, before := removalExecutorFixture(t)
			switch phase {
			case "restart":
				a.restartHook = func() error { return errors.New("secret-output") }
			case "generation":
				a.restartHook = func() error { return nil }
			case "health":
				a.healthHook = func() error { return errors.New("secret-output") }
			case "file changed":
				a.healthHook = func() error { return os.WriteFile(e.activePath, []byte(`{"modified":true}`), 0600) }
			}
			report, err := e.Execute(context.Background(), task)
			if err == nil || report.State != "recovery_required" || a.restarts != 1 {
				t.Fatal(report, err)
			}
			after, _ := os.ReadFile(e.activePath)
			if string(after) == string(before) {
				t.Fatal("rollback regranted UUID")
			}
			if _, err := os.Stat(filepath.Join(e.receiptDir, "inflight.json")); err != nil {
				t.Fatal(err)
			}
			report, err = e.Execute(context.Background(), task)
			if err == nil || report.State != "recovery_required" || a.restarts != 1 {
				t.Fatal(report, err)
			}
			task.ID = "99999999-9999-4999-8999-999999999999"
			task.CredentialRemoval.OperationID = task.ID
			report, err = e.Execute(context.Background(), task)
			if err == nil || report.Code != "runtime_recovery_required" || a.restarts != 1 {
				t.Fatal(report, err)
			}
		})
	}
}
func TestCredentialRemovalInterruptedAndLock(t *testing.T) {
	e, task, a, _ := removalExecutorFixture(t)
	lock, err := e.lock()
	if err != nil {
		t.Fatal(err)
	}
	report, err := e.Execute(context.Background(), task)
	if err == nil || report.Code != "local_state_unavailable" || a.restarts != 0 {
		t.Fatal(report, err)
	}
	lock.Close()
	a.validationHook = func() error { return errors.New("failure") }
	_, _ = e.Execute(context.Background(), task)
	path := filepath.Join(e.receiptDir, task.CredentialRemoval.OperationID+".json")
	data, _ := os.ReadFile(path)
	var receipt removalReceipt
	_ = json.Unmarshal(data, &receipt)
	for _, state := range []string{"prepared", "applying", "recovery_required"} {
		receipt.Report.State = state
		data, _ = json.Marshal(receipt)
		if err := writeRemovalFile(path, data); err != nil {
			t.Fatal(err)
		}
		report, err = e.Execute(context.Background(), task)
		if err == nil || report.State == "succeeded" || a.restarts != 0 {
			t.Fatal(report, err)
		}
	}
	receipt.Report.JobID = task.AgentID
	data, _ = json.Marshal(receipt)
	_ = writeRemovalFile(path, data)
	report, err = e.Execute(context.Background(), task)
	if err == nil || report.Code != "operation_identity_changed" {
		t.Fatal(report, err)
	}
}
func TestCredentialRemovalDeltaAndStrictJSON(t *testing.T) {
	_, task, _, baseline := removalExecutorFixture(t)
	var envelope map[string]json.RawMessage
	_ = json.Unmarshal(task.RenderedConfig, &envelope)
	candidate := envelope["singBox"]
	if _, err := verifyRemovalRuntimeDelta(baseline, candidate, *task.CredentialRemoval); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{`{"x":1,"x":2}`, `{"a":{"x":1,"x":2}}`, `{} {}`, `[]`, `null`} {
		if _, _, _, err := canonicalRemovalJSON([]byte(bad)); err == nil {
			t.Fatal("accepted", bad)
		}
	}
	mutated := []byte(strings.Replace(string(candidate), "OTHER-SECRET", "changed", 1))
	_, _, hash, _ := canonicalRemovalJSON(mutated)
	request := *task.CredentialRemoval
	request.CandidateRuntimeHash = hash
	if _, err := verifyRemovalRuntimeDelta(baseline, mutated, request); err == nil {
		t.Fatal("unrelated credential mutation accepted")
	}
	duplicate := []byte(strings.Replace(string(baseline), "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", removalUUID, 1))
	_, _, hash, _ = canonicalRemovalJSON(duplicate)
	request = *task.CredentialRemoval
	request.BaselineRuntimeHash = hash
	if _, err := verifyRemovalRuntimeDelta(duplicate, candidate, request); err == nil {
		t.Fatal("shared UUID accepted")
	}
}
func TestCredentialRemovalConfigSource(t *testing.T) {
	dir := t.TempDir()
	active := filepath.Join(dir, "config.json")
	_ = os.WriteFile(active, []byte(`{}`), 0600)
	for _, args := range [][]string{{"sing-box", "run", "-c", active}, {"sing-box", "-C", dir, "run"}, {"sing-box", "run", "--config=" + active}} {
		if err := verifyRemovalConfigArgs(args, active); err != nil {
			t.Fatal(args, err)
		}
	}
	for _, args := range [][]string{{"sing-box", "run"}, {"sing-box", "run", "-c", "stdin"}, {"sing-box", "run", "-c", active, "-C", dir}, {"sing-box", "check", "-c", active}, {"sing-box", "run", "--unknown", "-c", active}} {
		if err := verifyRemovalConfigArgs(args, active); err == nil {
			t.Fatal(args)
		}
	}
	_ = os.WriteFile(filepath.Join(dir, "extra.json"), []byte(`{}`), 0600)
	if err := verifyRemovalConfigArgs([]string{"sing-box", "run", "-C", dir}, active); err == nil {
		t.Fatal("merged files accepted")
	}
	link := filepath.Join(dir, "link.json")
	_ = os.Symlink(active, link)
	if err := verifyRemovalConfigArgs([]string{"sing-box", "run", "-c", link}, active); err == nil {
		t.Fatal("symlink accepted")
	}
	if _, err := readRemovalFile(link, 1024, false); err == nil {
		t.Fatal("followed symlink")
	}
}
func TestCredentialRemovalSystemdGeneration(t *testing.T) {
	output := "InvocationID=" + strings.Repeat("a", 32) + "\nMainPID=123\nExecMainStartTimestampMonotonic=456\nActiveState=active\nSubState=running\nKillMode=control-group\n"
	s := NewServiceController("sing-box.service")
	s.run = func(ctx context.Context, binary string, args ...string) ([]byte, error) {
		if binary != "systemctl" || args[len(args)-2] != "--" || args[len(args)-1] != "sing-box.service" {
			t.Fatal(binary, args)
		}
		return []byte(output), nil
	}
	if _, err := s.ObserveGeneration(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{strings.Replace(output, "control-group", "process", 1), strings.Replace(output, "MainPID=123", "MainPID=0", 1), output + "MainPID=999\n", strings.Replace(output, "active\n", "inactive\n", 1)} {
		saved := output
		output = bad
		if _, err := s.ObserveGeneration(context.Background()); err == nil {
			t.Fatal("invalid generation accepted")
		}
		output = saved
	}
}

func TestCredentialRemovalReplayRechecksHealthRace(t *testing.T) {
	e, task, a, _ := removalExecutorFixture(t)
	if _, err := e.Execute(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	a.healthHook = func() error { a.generation.PID++; return nil }
	report, err := e.Execute(context.Background(), task)
	if err == nil || report.Code != "completed_evidence_changed" || a.restarts != 1 {
		t.Fatal(report, err)
	}
}
func TestCredentialRemovalLastUserAndLegacyRejection(t *testing.T) {
	e, task, _, baseline := removalExecutorFixture(t)
	object, _, _, _ := canonicalRemovalJSON(baseline)
	inbound := object["inbounds"].([]any)[0].(map[string]any)
	inbound["users"] = inbound["users"].([]any)[:1]
	baseline = removalMarshal(t, object)
	_, _, task.CredentialRemoval.BaselineRuntimeHash, _ = canonicalRemovalJSON(baseline)
	if err := os.WriteFile(e.activePath, baseline, 0600); err != nil {
		t.Fatal(err)
	}
	inbound["users"] = []any{}
	bindRemovalCandidate(t, &task, object)
	if _, err := NewStager(t.TempDir()).Stage(task); err == nil {
		t.Fatal("legacy stager accepted removal authority")
	}
	if report, err := e.Execute(context.Background(), task); err != nil || report.State != "succeeded" {
		t.Fatal(report, err)
	}
}

func TestCredentialRemovalRejectsNonRegularFilesWithoutBlocking(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readRemovalFile(path, 1024, false); err == nil {
		t.Fatal("accepted FIFO")
	}
	if _, err := readRemovalFile(filepath.Dir(path), 1024, false); err == nil {
		t.Fatal("accepted directory")
	}
}
