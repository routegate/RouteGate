package heartbeat

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ikaevus/routegate/agent/internal/platform"
	"github.com/ikaevus/routegate/agent/internal/tasks"
)

type recoveryServiceAdapter struct {
	*fencedServiceAdapter
	observationError bool
	restarted        bool
	noRestart        bool
}

func (a *recoveryServiceAdapter) Descriptor() platform.VPNCoreAdapterDescriptor {
	return platform.VPNCoreAdapterDescriptor{Core: "sing-box", Protocol: "vless"}
}
func (a *recoveryServiceAdapter) ObserveGeneration(context.Context, string) (tasks.RuntimeGeneration, error) {
	if a.observationError {
		return tasks.RuntimeGeneration{}, errors.New("untrusted source")
	}
	if a.restarted {
		return tasks.RuntimeGeneration{InvocationID: strings.Repeat("b", 32), PID: 124, StartedMonotonic: "600"}, nil
	}
	return tasks.RuntimeGeneration{InvocationID: strings.Repeat("a", 32), PID: 123, StartedMonotonic: "500"}, nil
}

func (a *recoveryServiceAdapter) ExecuteServiceTask(ctx context.Context, task tasks.ConfigTask) (tasks.ServiceTaskReport, error) {
	result, err := a.fencedServiceAdapter.ExecuteServiceTask(ctx, task)
	if err == nil && !a.noRestart {
		a.restarted = true
	}
	return result, err
}
func (a *recoveryServiceAdapter) IsEnabled(context.Context) (tasks.ServiceResult, error) {
	return tasks.ServiceResult{}, nil
}
func (a *recoveryServiceAdapter) CheckHealth(context.Context, string) (tasks.ListenerHealthResult, error) {
	return tasks.ListenerHealthResult{Port: 443}, nil
}

func TestFencedDispatchSavesRuntimeRecoveryWitness(t *testing.T) {
	for _, mode := range []string{"observed", "unproven", "same_process", "wrong_config"} {
		t.Run(mode, func(t *testing.T) {
			fail := mode == "unproven"
			r, base, results := fencedRunnerFixture(t, tasks.TaskKindVPNCoreService, 400)
			r.vpnCoreAdapter = &recoveryServiceAdapter{fencedServiceAdapter: base, observationError: fail, noRestart: mode == "same_process"}
			r.cfg.ActiveConfigPath = filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(r.cfg.ActiveConfigPath, []byte(`{"inbounds":[]}`), 0600); err != nil {
				t.Fatal(err)
			}
			if mode == "wrong_config" {
				base.run = func() error {
					return os.WriteFile(r.cfg.ActiveConfigPath, []byte(`{"inbounds":[],"log":{"level":"error"}}`), 0600)
				}
			}
			if err := r.processNextTask(context.Background()); !errors.Is(err, errRuntimeMutationRecovery) {
				t.Fatal(err)
			}
			wantCalls := 1
			if fail {
				wantCalls = 0
			}
			if base.calls != wantCalls {
				t.Fatal("runtime operation repeated")
			}
			data, err := os.ReadFile(filepath.Join(r.runtimeMutationDir, "mutation-result.json"))
			if mode != "observed" {
				if !os.IsNotExist(err) || results.Load() != 0 {
					t.Fatal("unproven runtime produced receipt or HTTP result")
				}
				return
			}
			if err != nil || results.Load() != 1 {
				t.Fatal("result not saved/delivered", err)
			}
			var receipt struct {
				Witness *tasks.RuntimeRecoveryWitness `json:"runtimeWitness"`
			}
			if json.Unmarshal(data, &receipt) != nil || receipt.Witness == nil || receipt.Witness.Generation.PID != 124 || receipt.Witness.PreviousGeneration.PID != 123 {
				t.Fatal("runtime checkpoint missing")
			}
		})
	}
}


 // A missing active config is not proof of a pristine runtime. In particular,
 // the first post-install apply currently stops at the witness preflight.
 // Preserve this fail-closed property until a separately verified bootstrap
 // admission protocol exists; never "fix" E2E by bypassing all witness errors.
// Missing, corrupt or unobservable runtime state cannot authorize a mutation.
// This guards against broad witness bypasses when initial bootstrap is fixed.
func TestFencedInitialApplyUnprovenRuntimeDoesNotExecute(t *testing.T) {
	for _, scenario := range []struct {
		name string
		config string
		observerFails bool
	}{
		{name: "missing_config"},
		{name: "corrupt_config", config: "{invalid"},
		{name: "untrusted_generation", config: `{"inbounds":[]}`, observerFails: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			r, base, results := fencedRunnerFixture(t, tasks.TaskKindConfigApply, 204)
			r.vpnCoreAdapter = &recoveryServiceAdapter{
				fencedServiceAdapter: base, observationError: scenario.observerFails,
			}
			r.cfg.ActiveConfigPath = filepath.Join(t.TempDir(), "active.json")
			if scenario.config != "" {
				if err := os.WriteFile(r.cfg.ActiveConfigPath, []byte(scenario.config), 0600); err != nil {
					t.Fatal(err)
				}
			}
			task := tasks.ConfigTask{
				ID: fencedTaskID, AgentID: fencedAgentID, ServerID: fencedServerID,
				Kind: tasks.TaskKindConfigApply, Status: "in_progress",
				RenderedConfig: []byte(`{}`), // Legacy default selects the VLESS adapter.
			}
			invoked := false
			execute := func(context.Context) error {
				invoked = true
				return nil
			}
			if err := r.withRuntimeMutation(context.Background(), task, execute); !errors.Is(err, errRuntimeMutationRecovery) {
				t.Fatalf("unproven runtime preflight returned %v", err)
			}
			if invoked || base.calls != 0 || results.Load() != 0 {
				t.Fatal("unproven runtime bypassed preflight or emitted terminal result")
			}
			if _, err := os.Stat(filepath.Join(r.runtimeMutationDir, "mutation-inflight.json")); err != nil {
				t.Fatalf("unproven runtime lost durable intent: %v", err)
			}
			if err := r.withRuntimeMutation(context.Background(), task, execute); !errors.Is(err, tasks.ErrRuntimeMutationBlocked) || invoked {
				t.Fatalf("unproven runtime retried: %v", err)
			}
		})
	}
}


 // Even with a trustworthy running service, an invalid target envelope
 // cannot enter config apply. The target hash must be verified under the
 // existing mutation lease, before any handler invocation.
func TestFencedConfigApplyRejectsUnboundTarget(t *testing.T) {
 r, base, results := fencedRunnerFixture(t, tasks.TaskKindConfigApply, 204)
 r.vpnCoreAdapter = &recoveryServiceAdapter{fencedServiceAdapter: base}
 r.cfg.ActiveConfigPath = filepath.Join(t.TempDir(), "active.json")
 if err := os.WriteFile(r.cfg.ActiveConfigPath, []byte(`{"inbounds":[]}`), 0600); err != nil {
  t.Fatal(err)
 }
 task := tasks.ConfigTask{
  ID: fencedTaskID, AgentID: fencedAgentID, ServerID: fencedServerID,
  Kind: tasks.TaskKindConfigApply, Status: "in_progress",
  RenderedConfig: []byte(`{}`), // Adapter selection succeeds, target hash must fail.
 }
 invoked := false
 err := r.withRuntimeMutation(context.Background(), task, func(context.Context) error {
  invoked = true
  return nil
 })
 if !errors.Is(err, errRuntimeMutationRecovery) || invoked || base.calls != 0 || results.Load() != 0 {
  t.Fatalf("unbound target admitted: err=%v invoked=%v calls=%d results=%d", err, invoked, base.calls, results.Load())
 }
 if _, err := os.Stat(filepath.Join(r.runtimeMutationDir, "mutation-inflight.json")); err != nil {
  t.Fatalf("unbound target lost durable intent: %v", err)
 }
}
