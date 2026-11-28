package heartbeat

import (
	"context"
	"errors"
	"strings"

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
	if !r.cfg.ExperimentalRuntimeMutationFencing && !r.runtimeMutationManagerReady {
		// A detached platform-update worker owns its own durable lease.
		// Dispatching or observing its receipt must not hold a dispatcher
		// lease which ends before the worker starts.
		switch task.EffectiveKind() {
		case tasks.TaskKindDiagnostic:
			return execute(ctx)
		case tasks.TaskKindPlatformUpdate:
			if task.Operation != tasks.PlatformUpdateOperationReconcile && !tasks.RuntimeMutationIdleForPlatformUpdate(dir) {
				return tasks.ErrRuntimeMutationBlocked
			}
			// The detached worker independently reserves the shared lease.
			// A completed legacy mutation leaves the lock directory present.
			return execute(ctx)
		case tasks.TaskKindConfigApply, tasks.TaskKindVPNCoreService, tasks.TaskKindVPNCoreInstall, tasks.TaskKindMaintenance:
			// Legacy mutations must participate in the same atomic admission
			// as detached updates, even when experimental fencing is off.
			// On ambiguous failure, retain the durable intent for recovery.
			lease, err := tasks.BeginRuntimeMutation(dir, task.ID, task.EffectiveKind())
			if err != nil {
				return tasks.ErrRuntimeMutationBlocked
			}
			defer lease.Close()
			// Cancellation before the handler runs cannot have mutated the
			// runtime. Release only this still-owned, unchanged intent.
			if err := ctx.Err(); err != nil {
				if lease.Complete() != nil {
					return errRuntimeMutationRecovery
				}
				return err
			}
			if err := execute(ctx); err != nil {
				return err
			}
			if err := lease.Complete(); err != nil {
				return errRuntimeMutationRecovery
			}
			return nil
		default:
			return execute(ctx) // Existing unsupported-kind rejection.
		}
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
	var before *tasks.RuntimeRecoveryWitness
	recoveryAdapter := r.runtimeRecoveryAdapter(task)
	expectedHash := ""
	if recoveryAdapter != nil {
		r.logger.Info("runtime mutation preflight", "phase", "witness_before")
		observed, err := tasks.CaptureRuntimeRecoveryWitness(ctx, r.cfg.ActiveConfigPath, recoveryAdapter)
		if err != nil {
			r.logger.Warn("runtime mutation preflight", "phase", "witness_unavailable")
			return errRuntimeMutationRecovery
		}
		r.logger.Info("runtime mutation preflight", "phase", "witness_ready")
		before = &observed
		expectedHash = observed.RuntimeHash
		if task.EffectiveKind() == tasks.TaskKindConfigApply {
			expectedHash, err = tasks.RuntimeRecoveryTargetHash(task)
			if err != nil {
				return errRuntimeMutationRecovery
			}
		}
	}
	if execute(client.WithDurableTaskResult(ctx, func(taskID string, data []byte) error {
		var witness *tasks.RuntimeRecoveryWitness
		if before != nil {
			observed, err := tasks.CaptureRuntimeRecoveryWitness(ctx, r.cfg.ActiveConfigPath, recoveryAdapter)
			if err != nil || observed.Generation.InvocationID == before.Generation.InvocationID || observed.RuntimeHash != expectedHash {
				return tasks.ErrRuntimeMutationBlocked
			}
			observed.PreviousGeneration = before.Generation
			witness = &observed
		}
		return lease.SaveResultWithRuntimeWitness(r.runtimeResultBinding(), taskID, data, witness)
	})) != nil {
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

func (r *Runner) runtimeRecoveryAdapter(task tasks.ConfigTask) tasks.VPNCoreAdapter {
	if !r.cfg.ServiceControlEnabled || !tasks.SupportsRuntimeRecovery(r.vpnCoreAdapter) {
		return nil
	}
	switch task.EffectiveKind() {
	case tasks.TaskKindVPNCoreService:
		if task.Operation != "restart" {
			return nil
		}
	case tasks.TaskKindConfigApply:
		adapters, err := tasks.SelectVPNCoreAdapters(task, r.vpnCoreAdapter, r.wireGuardAdapter, r.hysteria2Adapter, r.shadowsocksAdapter, r.mtprotoAdapter)
		if err != nil || len(adapters) != 1 || !tasks.SupportsRuntimeRecovery(adapters[0]) {
			return nil
		}
	default:
		return nil
	}
	return r.vpnCoreAdapter
}

func (r *Runner) runtimeResultBinding() tasks.RuntimeResultBinding {
	return tasks.RuntimeResultBinding{ManagerURL: strings.TrimRight(r.cfg.ManagerURL, "/"), AgentID: r.cfg.AgentID, ServerID: r.cfg.ServerID}
}

func (r *Runner) replayRuntimeResult(ctx context.Context) {
	if !r.runtimeMutationManagerReady {
		return
	}
	dir := r.runtimeMutationDir
	if dir == "" {
		dir = tasks.DefaultRuntimeMutationDir
	}
	if err := tasks.ReplayRuntimeResult(dir, r.runtimeResultBinding(), func(taskID string, data []byte) error {
		return r.client.ReplayTaskResult(ctx, r.cfg.AgentToken, taskID, data, r.runtimeResultBinding())
	}); err != nil {
		// Keep polling for read-only diagnostics; admission remains fenced.
		r.logger.Warn("durable task result requires reconciliation")
	}
}

func (r *Runner) prepareRuntimeMutationHeartbeat(info *systeminfo.Info) {
	dir := r.runtimeMutationDir
	if dir == "" {
		dir = tasks.DefaultRuntimeMutationDir
	}
	// Legacy handlers and the detached worker also use this directory. A
	// completed lease leaves runtime.lock behind; that is not an opt-in to
	// experimental witness/recovery semantics. Reuse the lock-held idle check
	// so unresolved, contended or unreadable state still requests preservation.
	// This observation never grants admission: dispatch acquires its own lease.
	if r.cfg.ExperimentalRuntimeMutationFencing || r.runtimeMutationManagerReady || !tasks.RuntimeMutationIdleForPlatformUpdate(dir) {
		if info.Capabilities == nil {
			info.Capabilities = map[string]any{}
		}
		// This requests timeout preservation only, NOT credential-removal support.
		info.Capabilities["runtimeMutationFencingV1"] = true
		if r.credentialRemovalEnabled() {
			info.Capabilities["credentialRemovalV1"] = true
		}
	}
}

func (r *Runner) credentialRemovalEnabled() bool {
	return r.cfg.ExperimentalCredentialRemoval && r.cfg.ExperimentalRuntimeMutationFencing &&
		r.cfg.ServiceControlEnabled && tasks.SupportsRuntimeRecovery(r.vpnCoreAdapter)
}
