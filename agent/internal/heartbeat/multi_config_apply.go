package heartbeat

import (
	"context"
	"fmt"

	"github.com/ikaevus/routegate/agent/internal/tasks"
)

type preparedConfigRuntime struct {
	adapter      tasks.VPNCoreAdapter
	activePath   string
	backupDir    string
	stage        tasks.StageResult
	validation   tasks.ValidationResult
	apply        tasks.ApplyResult
	restart      tasks.ServiceResult
	health       tasks.ServiceResult
	persistence  tasks.ServiceResult
	listener     tasks.ListenerHealthResult
}

func (r *Runner) processConfigApplyTask(ctx context.Context, task tasks.ConfigTask) error {
	adapters, err := tasks.SelectVPNCoreAdapters(
		task,
		r.vpnCoreAdapter,
		r.wireGuardAdapter,
		r.hysteria2Adapter,
		r.shadowsocksAdapter,
		r.mtprotoAdapter,
	)
	if err != nil {
		return r.completeConfigApplyFailure(ctx, task, err, nil, "select")
	}

	r.logger.Info("config apply phase", "phase", "selected")
	prepared := make([]preparedConfigRuntime, 0, len(adapters))
	for _, adapter := range adapters {
		activePath, backupDir := r.adapterStorage(adapter)
		stage, stageErr := adapter.Stage(task)
		if stageErr != nil {
			return r.completeConfigApplyFailure(ctx, task, stageErr, prepared, "stage")
		}
		validation, validationErr := adapter.Validate(ctx, stage.StagedPath)
		current := preparedConfigRuntime{
			adapter: adapter, activePath: activePath, backupDir: backupDir,
			stage: stage, validation: validation,
		}
		prepared = append(prepared, current)
		if validationErr != nil {
			return r.completeConfigApplyFailure(ctx, task, validationErr, prepared, "validate")
		}
	}

	r.logger.Info("config apply phase", "phase", "validated")
	for index := range prepared {
		applier := tasks.NewApplier(prepared[index].activePath, prepared[index].backupDir)
		apply, applyErr := applier.Apply(prepared[index].stage.StagedPath, prepared[index].stage.ConfigVersionID)
		if applyErr != nil {
			rollback := r.rollbackPreparedRuntimes(ctx, prepared[:index])
			return r.completeConfigApplyFailure(ctx, task, fmt.Errorf("apply VPN runtime config: %w; rollback=%s", applyErr, rollback), prepared, "apply")
		}
		prepared[index].apply = apply
	}

	r.logger.Info("config apply phase", "phase", "applied")
	if !r.cfg.ServiceControlEnabled {
		report := multiConfigApplyReport(prepared, "skipped_service_control_disabled")
		if err := r.client.CompleteTaskSucceeded(ctx, r.cfg.AgentToken, task.ID, report); err != nil {
			return err
		}
		r.logger.Info("multi-runtime config staged, validated and applied; service control skipped", "job_id", task.ID, "config_version_id", task.ConfigVersionID, "runtime_count", len(prepared))
		return nil
	}

	for index := range prepared {
		r.logger.Info("config apply phase", "phase", "restart")
		restart, restartErr := prepared[index].adapter.Restart(ctx)
		prepared[index].restart = restart
		if restartErr != nil {
			rollback := r.rollbackPreparedRuntimes(ctx, prepared)
			return r.completeConfigApplyFailure(ctx, task, fmt.Errorf("restart VPN runtime: %w; rollback=%s", restartErr, rollback), prepared, "restart")
		}

		r.logger.Info("config apply phase", "phase", "healthcheck")
		health, healthErr := prepared[index].adapter.IsActive(ctx)
		prepared[index].health = health
		if healthErr != nil {
			rollback := r.rollbackPreparedRuntimes(ctx, prepared)
			return r.completeConfigApplyFailure(ctx, task, fmt.Errorf("VPN runtime active check: %w; rollback=%s", healthErr, rollback), prepared, "healthcheck")
		}

		r.logger.Info("config apply phase", "phase", "persistence")
		persistence, persistenceErr := prepared[index].adapter.IsEnabled(ctx)
		prepared[index].persistence = persistence
		if persistenceErr != nil {
			rollback := r.rollbackPreparedRuntimes(ctx, prepared)
			return r.completeConfigApplyFailure(ctx, task, fmt.Errorf("VPN runtime persistence check: %w; rollback=%s", persistenceErr, rollback), prepared, "persistence")
		}

		r.logger.Info("config apply phase", "phase", "listener")
		listener, listenerErr := prepared[index].adapter.CheckHealth(ctx, prepared[index].apply.ActivePath)
		prepared[index].listener = listener
		if listenerErr != nil {
			rollback := r.rollbackPreparedRuntimes(ctx, prepared)
			return r.completeConfigApplyFailure(ctx, task, fmt.Errorf("VPN runtime listener healthcheck: %w; rollback=%s", listenerErr, rollback), prepared, "listener")
		}
	}

	r.logger.Info("config apply phase", "phase", "report")
	report := multiConfigApplyReport(prepared, "succeeded")
	if err := r.client.CompleteTaskSucceeded(ctx, r.cfg.AgentToken, task.ID, report); err != nil {
		return err
	}
	r.logger.Info("multi-runtime config staged, validated and applied", "job_id", task.ID, "config_version_id", task.ConfigVersionID, "runtime_count", len(prepared))
	return nil
}

func (r *Runner) rollbackPreparedRuntimes(ctx context.Context, prepared []preparedConfigRuntime) string {
	if len(prepared) == 0 {
		return "skipped"
	}
	status := "succeeded"
	for index := len(prepared) - 1; index >= 0; index-- {
		item := prepared[index]
		if item.apply.ActivePath == "" {
			continue
		}
		applier := tasks.NewApplier(item.activePath, item.backupDir)
		if err := applier.RollbackApply(item.apply); err != nil {
			status = "failed"
			r.logger.Warn("rollback multi-runtime config failed", "core", item.adapter.Descriptor().Core, "protocol", item.adapter.Descriptor().Protocol, "error", err)
			continue
		}
		if item.apply.BackupPath != "" {
			if _, err := item.adapter.Restart(ctx); err != nil {
				status = "failed"
				r.logger.Warn("restart restored VPN runtime failed", "core", item.adapter.Descriptor().Core, "protocol", item.adapter.Descriptor().Protocol, "error", err)
			}
		} else if _, err := item.adapter.Stop(ctx); err != nil {
			status = "failed"
			r.logger.Warn("stop newly-created VPN runtime after rollback failed", "core", item.adapter.Descriptor().Core, "protocol", item.adapter.Descriptor().Protocol, "error", err)
		}
	}
	return status
}

func (r *Runner) completeConfigApplyFailure(ctx context.Context, task tasks.ConfigTask, cause error, prepared []preparedConfigRuntime, stage string) error {
	report := multiConfigApplyReport(prepared, "failed")
	report["failedStage"] = stage
	report["configVersionId"] = task.ConfigVersionID
	report["configHash"] = task.ConfigHash
	if completeErr := r.client.CompleteTaskFailed(ctx, r.cfg.AgentToken, task.ID, cause.Error(), report); completeErr != nil {
		return fmt.Errorf("%v; report failure: %w", cause, completeErr)
	}
	return cause
}

func multiConfigApplyReport(prepared []preparedConfigRuntime, status string) map[string]any {
	components := make([]map[string]any, 0, len(prepared))
	for _, item := range prepared {
		descriptor := item.adapter.Descriptor()
		component := map[string]any{
			"core": descriptor.Core,
			"protocol": descriptor.Protocol,
			"status": status,
			"stagedPath": item.stage.StagedPath,
			"activePath": item.apply.ActivePath,
			"backupPath": item.apply.BackupPath,
			"validationCommand": item.validation.Command,
			"validationOutput": item.validation.Output,
			"restartCommand": item.restart.Command,
			"healthCommand": item.health.Command,
			"persistenceCommand": item.persistence.Command,
			"listenerAddress": item.listener.Address,
			"listenerPort": item.listener.Port,
		}
		components = append(components, component)
	}
	return map[string]any{
		"stage": status,
		"runtimeCount": len(prepared),
		"components": components,
	}
}
