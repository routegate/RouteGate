package heartbeat

import (
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

	"github.com/ikaevus/routegate/agent/internal/client"
	"github.com/ikaevus/routegate/agent/internal/config"
	"github.com/ikaevus/routegate/agent/internal/platform"
	"github.com/ikaevus/routegate/agent/internal/systeminfo"
	"github.com/ikaevus/routegate/agent/internal/tasks"
)

const fencedTaskID = "11111111-1111-4111-8111-111111111111"
const fencedServerID = "22222222-2222-4222-8222-222222222222"
const fencedAgentID = "33333333-3333-4333-8333-333333333333"

type fencedServiceAdapter struct {
	tasks.VPNCoreAdapter
	calls int
	run   func() error
}

func (a *fencedServiceAdapter) ExecuteServiceTask(context.Context, tasks.ConfigTask) (tasks.ServiceTaskReport, error) {
	a.calls++
	var err error
	if a.run != nil {
		err = a.run()
	}
	return tasks.ServiceTaskReport{Kind: tasks.TaskKindVPNCoreService, Operation: "restart"}, err
}

// Exercises the actual HTTP task poll/dispatch/result loop without running host
// commands. The Manager is an isolated protocol fixture, not a production API.
func fencedRunnerFixture(t *testing.T, kind string, resultCodes ...int) (*Runner, *fencedServiceAdapter, *atomic.Int32) {
	t.Helper()
	task := tasks.ConfigTask{ID: fencedTaskID, AgentID: fencedAgentID, ServerID: fencedServerID, Kind: kind, Status: "in_progress", Operation: "restart"}
	results := &atomic.Int32{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get("Authorization") != "Bearer isolated-token" {
			t.Error("missing authentication")
		}
		switch req.URL.Path {
		case "/api/v1/agent/tasks/next":
			_ = json.NewEncoder(w).Encode(map[string]any{"task": task})
		case "/api/v1/agent/tasks/" + fencedTaskID + "/result":
			attempt := int(results.Add(1)) - 1
			w.WriteHeader(resultCodes[min(attempt, len(resultCodes)-1)])
		default:
			t.Errorf("unexpected path %s", req.URL.Path)
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	a := &fencedServiceAdapter{}
	r := NewRunner(config.Config{ManagerURL: srv.URL, AgentID: fencedAgentID, ServerID: fencedServerID, AgentToken: "isolated-token", ServiceControlEnabled: true, ExperimentalRuntimeMutationFencing: true}, "", slog.New(slog.NewTextHandler(io.Discard, nil)))
	r.client = client.New(srv.URL)
	r.vpnCoreAdapter = a
	r.runtimeMutationDir = filepath.Join(t.TempDir(), "mutations")
	r.runtimeMutationManagerReady = true // Negotiated by the protocol fixture.
	return r, a, results
}

func TestFencedDispatchServiceSuccess(t *testing.T) {
	r, a, results := fencedRunnerFixture(t, tasks.TaskKindVPNCoreService, 204)
	a.run = func() error {
		if _, err := os.Stat(filepath.Join(r.runtimeMutationDir, "mutation-inflight.json")); err != nil {
			t.Error("mutation ran without durable intent", err)
		}
		return nil
	}
	if err := r.processNextTask(context.Background()); err != nil {
		t.Fatal(err)
	}
	if a.calls != 1 || results.Load() != 1 {
		t.Fatal(a.calls, results.Load())
	}
	if _, err := os.Stat(filepath.Join(r.runtimeMutationDir, "mutation-inflight.json")); !os.IsNotExist(err) {
		t.Fatal("successful acknowledged task retained intent", err)
	}
}

func TestFencedDispatchPreservesUncertaintyAcrossRestart(t *testing.T) {
	for _, mode := range []string{"handler failure", "unacknowledged result"} {
		t.Run(mode, func(t *testing.T) {
			code := 204
			if mode == "unacknowledged result" {
				code = 400
			}
			r, a, results := fencedRunnerFixture(t, tasks.TaskKindVPNCoreService, code)
			if mode == "handler failure" {
				a.run = func() error { return errors.New("sensitive-runtime-output") }
			}
			if err := r.processNextTask(context.Background()); !errors.Is(err, errRuntimeMutationRecovery) {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(r.runtimeMutationDir, "mutation-inflight.json")); err != nil {
				t.Fatal(err)
			}
			// A fresh Runner simulates Agent restart; disabling the flag cannot
			// bypass existing state. Neither retry nor a new mutation may run.
			fresh := NewRunner(r.cfg, "", r.logger)
			fresh.cfg.ExperimentalRuntimeMutationFencing = false
			fresh.runtimeMutationDir = r.runtimeMutationDir
			fresh.runtimeMutationManagerReady = true
			fresh.vpnCoreAdapter = a
			if err := fresh.processNextTask(context.Background()); !errors.Is(err, tasks.ErrRuntimeMutationBlocked) {
				t.Fatal(err)
			}
			wantResults := int32(1)
			if mode == "unacknowledged result" {
				wantResults = 2 // Historical result replay; runtime stays fenced.
			}
			if mode == "handler failure" {
				wantResults = 0
			}
			if a.calls != 1 || results.Load() != wantResults {
				t.Fatal("retried runtime or invented terminal result", a.calls, results.Load())
			}
		})
	}
}

func TestFencedDispatchEveryMutationKind(t *testing.T) {
	for _, kind := range []string{tasks.TaskKindConfigApply, "", tasks.TaskKindVPNCoreService, tasks.TaskKindVPNCoreInstall, tasks.TaskKindMaintenance, tasks.TaskKindPlatformUpdate, tasks.TaskKindCredentialRemoval, "future_kind"} {
		t.Run("kind_"+kind, func(t *testing.T) {
			r, a, results := fencedRunnerFixture(t, kind, 204)
			if err := os.Mkdir(r.runtimeMutationDir, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(r.runtimeMutationDir, "inflight.json"), []byte("unresolved-removal"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := r.processNextTask(context.Background()); !errors.Is(err, tasks.ErrRuntimeMutationBlocked) {
				t.Fatal("not blocked", err)
			}
			if a.calls != 0 || results.Load() != 0 {
				t.Fatal("dispatch or terminal report escaped gate")
			}
		})
	}
}

func TestFencedDispatchClassificationAndIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, kind, operation string
		allowed               bool
	}{
		{"diagnostic", tasks.TaskKindDiagnostic, "host_overview", true},
		{"update reconciliation", tasks.TaskKindPlatformUpdate, tasks.PlatformUpdateOperationReconcile, true},
		{"detached dispatch", tasks.TaskKindPlatformUpdate, tasks.PlatformUpdateOperationDispatch, false},
		{"future update", tasks.TaskKindPlatformUpdate, "future", false},
		{"removal stays disabled", tasks.TaskKindCredentialRemoval, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, _, _ := fencedRunnerFixture(t, tc.kind, 204)
			ran := false
			err := r.withRuntimeMutation(context.Background(), tasks.ConfigTask{Kind: tc.kind, Operation: tc.operation}, func(context.Context) error { ran = true; return nil })
			if ran != tc.allowed || (err == nil) != tc.allowed {
				t.Fatal(ran, err)
			}
		})
	}
	for _, mode := range []string{"wrong agent", "wrong server", "wrong status", "wrong task"} {
		t.Run(mode, func(t *testing.T) {
			r, _, _ := fencedRunnerFixture(t, tasks.TaskKindVPNCoreService, 204)
			task := tasks.ConfigTask{ID: fencedTaskID, AgentID: fencedAgentID, ServerID: fencedServerID, Status: "in_progress", Kind: tasks.TaskKindVPNCoreService}
			switch mode {
			case "wrong agent":
				task.AgentID = fencedTaskID
			case "wrong server":
				task.ServerID = fencedTaskID
			case "wrong status":
				task.Status = "pending"
			case "wrong task":
				task.ID = "../invalid"
			}
			err := r.withRuntimeMutation(context.Background(), task, func(context.Context) error { t.Error("handler ran"); return nil })
			if !errors.Is(err, tasks.ErrRuntimeMutationBlocked) {
				t.Fatal(err)
			}
		})
	}
}

func TestFencedDispatchDefaultAndCancellation(t *testing.T) {
	r, a, _ := fencedRunnerFixture(t, tasks.TaskKindVPNCoreService, 204)
	r.cfg.ExperimentalRuntimeMutationFencing = false
	r.runtimeMutationManagerReady = false
	if err := r.processNextTask(context.Background()); err != nil {
		t.Fatal(err)
	}
	if a.calls != 1 {
		t.Fatal(a.calls)
	}
	if _, err := os.Stat(r.runtimeMutationDir); !os.IsNotExist(err) {
		t.Fatal("default path created state", err)
	}
	r.cfg.ExperimentalRuntimeMutationFencing = true
	r.runtimeMutationManagerReady = true
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	task := tasks.ConfigTask{ID: fencedTaskID, AgentID: fencedAgentID, ServerID: fencedServerID, Status: "in_progress", Kind: tasks.TaskKindVPNCoreService}
	err := r.withRuntimeMutation(ctx, task, func(context.Context) error { t.Error("cancelled handler ran"); return nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(r.runtimeMutationDir, "mutation-inflight.json")); !os.IsNotExist(err) {
		t.Fatal("provable pre-execution cancellation retained intent", err)
	}
}

type fencedConfigAdapter struct {
	rollbackTestAdapter
	stage       string
	check       func()
	failRestart bool
}

func (a *fencedConfigAdapter) Descriptor() platform.VPNCoreAdapterDescriptor {
	return platform.VPNCoreAdapterDescriptor{Core: "sing-box", Protocol: "vless"}
}
func (a *fencedConfigAdapter) Stage(task tasks.ConfigTask) (tasks.StageResult, error) {
	a.check()
	if err := os.WriteFile(a.stage, []byte("candidate config"), 0600); err != nil {
		return tasks.StageResult{}, err
	}
	return tasks.StageResult{StagedPath: a.stage, ConfigVersionID: task.ConfigVersionID}, nil
}
func (a *fencedConfigAdapter) Restart(context.Context) (tasks.ServiceResult, error) {
	a.check()
	a.restartCalls++
	if a.failRestart && a.restartCalls == 1 {
		return tasks.ServiceResult{}, errors.New("restart failed")
	}
	return tasks.ServiceResult{}, nil
}

func TestFencedDispatchConfigApplyAndRollback(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "apply", true: "rollback"}[fail], func(t *testing.T) {
			r, _, results := fencedRunnerFixture(t, tasks.TaskKindConfigApply, 204)
			dir := t.TempDir()
			r.cfg.ActiveConfigPath = filepath.Join(dir, "active.json")
			r.cfg.ConfigBackupDir = filepath.Join(dir, "backups")
			if err := os.WriteFile(r.cfg.ActiveConfigPath, []byte("baseline config"), 0600); err != nil {
				t.Fatal(err)
			}
			a := &fencedConfigAdapter{stage: filepath.Join(dir, "candidate.json"), failRestart: fail}
			a.check = func() {
				if _, err := os.Stat(filepath.Join(r.runtimeMutationDir, "mutation-inflight.json")); err != nil {
					t.Error("no durable intent", err)
				}
				if lease, err := tasks.BeginRuntimeMutation(r.runtimeMutationDir, fencedTaskID, tasks.TaskKindVPNCoreService); err == nil {
					lease.Close()
					t.Error("lock released during apply/rollback")
				}
			}
			r.vpnCoreAdapter = a
			task := tasks.ConfigTask{ID: fencedTaskID, Kind: tasks.TaskKindConfigApply, AgentID: fencedAgentID, ServerID: fencedServerID, Status: "in_progress", ConfigVersionID: fencedTaskID, RenderedConfig: json.RawMessage(`{}`)}
			err := r.dispatchTask(context.Background(), task)
			if fail && !errors.Is(err, errRuntimeMutationRecovery) || !fail && err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(r.cfg.ActiveConfigPath)
			if err != nil {
				t.Fatal(err)
			}
			want := "candidate config"
			if fail {
				want = "baseline config"
			}
			if string(data) != want {
				t.Fatal("unexpected runtime file")
			}
			_, err = os.Stat(filepath.Join(r.runtimeMutationDir, "mutation-inflight.json"))
			if fail && err != nil || !fail && !os.IsNotExist(err) {
				t.Fatal("wrong intent retention", err)
			}
			wantResults := int32(1)
			if fail {
				wantResults = 0
			}
			if results.Load() != wantResults {
				t.Fatal("missing terminal report")
			}
		})
	}
}

func TestFencedDispatchRequiresManagerNegotiation(t *testing.T) {
	r, a, results := fencedRunnerFixture(t, tasks.TaskKindVPNCoreService, 204)
	r.runtimeMutationManagerReady = false
	if err := r.processNextTask(context.Background()); !errors.Is(err, tasks.ErrRuntimeMutationBlocked) {
		t.Fatal(err)
	}
	if a.calls != 0 || results.Load() != 0 {
		t.Fatal("mutation ran without timeout preservation")
	}
	if _, err := os.Stat(r.runtimeMutationDir); !os.IsNotExist(err) {
		t.Fatal("unnegotiated job acquired intent")
	}
}

func TestFencedHeartbeatRequestsTimeoutPreservation(t *testing.T) {
	r, _, _ := fencedRunnerFixture(t, tasks.TaskKindVPNCoreService, 204)
	for _, enabled := range []bool{false, true} {
		r.cfg.ExperimentalRuntimeMutationFencing = enabled
		info := systeminfo.Info{}
		r.prepareRuntimeMutationHeartbeat(&info)
		got, _ := info.Capabilities["runtimeMutationFencingV1"].(bool)
		if got != enabled {
			t.Fatal("wrong policy request")
		}
	}
	r.cfg.ExperimentalRuntimeMutationFencing = false
	if err := os.Mkdir(r.runtimeMutationDir, 0700); err != nil {
		t.Fatal(err)
	}
	info := systeminfo.Info{}
	r.prepareRuntimeMutationHeartbeat(&info)
	if info.Capabilities["runtimeMutationFencingV1"] != true {
		t.Fatal("existing state not negotiated")
	}
}

func TestFencedDispatchChangedIntentCannotClear(t *testing.T) {
	r, a, _ := fencedRunnerFixture(t, tasks.TaskKindVPNCoreService, 204)
	a.run = func() error {
		return os.WriteFile(filepath.Join(r.runtimeMutationDir, "mutation-inflight.json"), []byte("changed"), 0600)
	}
	if err := r.processNextTask(context.Background()); !errors.Is(err, errRuntimeMutationRecovery) {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(r.runtimeMutationDir, "mutation-inflight.json"))
	if err != nil || string(data) != "changed" {
		t.Fatal("cleared changed state", err)
	}
}

func TestFencedDispatchRequiresPositiveResultAcknowledgement(t *testing.T) {
	for _, finalCode := range []int{204, 404} {
		t.Run(http.StatusText(finalCode), func(t *testing.T) {
			r, a, results := fencedRunnerFixture(t, tasks.TaskKindVPNCoreService, 500, finalCode)
			err := r.processNextTask(context.Background())
			if finalCode == 204 && err != nil || finalCode == 404 && !errors.Is(err, errRuntimeMutationRecovery) {
				t.Fatal(err)
			}
			if a.calls != 1 || results.Load() != 2 {
				t.Fatal("runtime replay or missing result retry", a.calls, results.Load())
			}
			_, err = os.Stat(filepath.Join(r.runtimeMutationDir, "mutation-inflight.json"))
			if finalCode == 204 && !os.IsNotExist(err) || finalCode == 404 && err != nil {
				t.Fatal("incorrect release after uncertain result", err)
			}
		})
	}
}

func TestFencedDispatchProcessExitDuringHandler(t *testing.T) {
	if dir := os.Getenv("RG_TEST_FENCED_DISPATCH_CHILD"); dir != "" {
		r, a, _ := fencedRunnerFixture(t, tasks.TaskKindVPNCoreService, 204)
		r.runtimeMutationDir = dir
		a.run = func() error { os.Exit(0); return nil }
		_ = r.processNextTask(context.Background())
		os.Exit(2) // Handler must have been entered before process termination.
	}
	dir := filepath.Join(t.TempDir(), "mutation-state")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestFencedDispatchProcessExitDuringHandler$")
	cmd.Env = append(os.Environ(), "RG_TEST_FENCED_DISPATCH_CHILD="+dir)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("child %v: %s", err, output)
	}
	r, a, results := fencedRunnerFixture(t, tasks.TaskKindVPNCoreService, 204)
	r.cfg.ExperimentalRuntimeMutationFencing = false
	r.runtimeMutationDir = dir
	if err := r.processNextTask(context.Background()); !errors.Is(err, tasks.ErrRuntimeMutationBlocked) {
		t.Fatal("crash state forgotten", err)
	}
	if a.calls != 0 || results.Load() != 0 {
		t.Fatal("crashed mutation replayed or terminalized")
	}
}
