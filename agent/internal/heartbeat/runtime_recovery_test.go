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
