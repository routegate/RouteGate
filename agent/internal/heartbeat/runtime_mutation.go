package heartbeat

import (
	"context"
	"errors"

	"github.com/ikaevus/routegate/agent/internal/client"
	"github.com/ikaevus/routegate/agent/internal/systeminfo"
	"github.com/ikaevus/routegate/agent/internal/tasks"
)

var errRuntimeMutationRecovery = errors.New("runtime mutation recovery required; no automatic retry or unlock")

// withRuntimeMutation is deliberately opt-in until authenticated recovery and
// detached-worker handoff are implemented. Existing state makes the gate sticky:
// removing the opt-in flag on restart must not forget an unresolved mutation.
func (r *Runner) withRuntimeMutation(ctx context.Context, task tasks.ConfigTask, execute func(context.Context) error) error {
	dir := r.runtimeMutationDir
	if dir == "" {
		dir = tasks.DefaultRuntimeMutationDir
	}
	if !r.cfg.ExperimentalRuntimeMutationFencing && !r.runtimeMutationManagerReady && !tasks.RuntimeMutationStatePresent(dir) {
		return execute(ctx)
	}
	switch task.EffectiveKind() {
	case tasks.TaskKindDiagnostic:
		return execute(ctx) // Read-only diagnostics remain usable during recovery.
	case tasks.TaskKindPlatformUpdate:
		if task.Operation == tasks.PlatformUpdateOperationReconcile {
			return execute(ctx) // Receipt observation never dispatches a mutation.
		}
		// Never hold a dispatcher-only lease for a detached mutation. Do not
		// terminalize an update whose prior dispatch outcome might be unknown.
		return tasks.ErrRuntimeMutationBlocked
	case tasks.TaskKindConfigApply, tasks.TaskKindVPNCoreService, tasks.TaskKindVPNCoreInstall, tasks.TaskKindMaintenance:
	default:
		// Fail closed for unknown/future kinds, including credential removal.
		return tasks.ErrRuntimeMutationBlocked
	}
	if task.Status != "in_progress" || r.cfg.AgentID == "" || r.cfg.ServerID == "" || task.AgentID != r.cfg.AgentID || task.ServerID != r.cfg.ServerID {
		return tasks.ErrRuntimeMutationBlocked
	}
	if !r.runtimeMutationManagerReady {
		return tasks.ErrRuntimeMutationBlocked
	}
	lease, err := tasks.BeginRuntimeMutation(dir, task.ID, task.EffectiveKind())
	if err != nil {
		// Do not send a terminal result: this may be a redelivery of a task
		// whose real result was lost. Manager must retain uncertainty.
		return tasks.ErrRuntimeMutationBlocked
	}
	defer lease.Close()
	if ctx.Err() != nil {
		// No handler has run, so this live owner can prove no mutation began.
		if lease.Complete() != nil {
			return errRuntimeMutationRecovery
		}
		return ctx.Err()
	}
	if execute(client.WithStrictTaskAcknowledgement(ctx)) != nil {
		// Includes post-mutation errors and unacknowledged results. Even an
		// apparent validation failure stays fenced until its phase is proven.
		// Do not log a raw handler error which may include config/command data.
		return errRuntimeMutationRecovery
	}
	if lease.Complete() != nil {
		return errRuntimeMutationRecovery
	}
	return nil
}

func (r *Runner) prepareRuntimeMutationHeartbeat(info *systeminfo.Info) {
	dir := r.runtimeMutationDir
	if dir == "" {
		dir = tasks.DefaultRuntimeMutationDir
	}
	if r.cfg.ExperimentalRuntimeMutationFencing || tasks.RuntimeMutationStatePresent(dir) {
		if info.Capabilities == nil {
			info.Capabilities = map[string]any{}
		}
		// This requests timeout preservation only, NOT credential-removal support.
		info.Capabilities["runtimeMutationFencingV1"] = true
	}
}
