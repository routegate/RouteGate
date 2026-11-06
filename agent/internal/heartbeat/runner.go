package heartbeat

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/ikaevus/routegate/agent/internal/client"
	"github.com/ikaevus/routegate/agent/internal/config"
	"github.com/ikaevus/routegate/agent/internal/presence"
	"github.com/ikaevus/routegate/agent/internal/runtimecleanup"
	"github.com/ikaevus/routegate/agent/internal/systeminfo"
	"github.com/ikaevus/routegate/agent/internal/tasks"
	"github.com/ikaevus/routegate/agent/internal/traffic"
)

type Runner struct {
	cfg                         config.Config
	configPath                  string
	client                      *client.Client
	logger                      *slog.Logger
	trafficCollector            traffic.Collector
	trafficTracker              *traffic.DeltaTracker
	lastTrafficReport           time.Time
	presenceCollector           presence.Collector
	lastPresenceReport          time.Time
	vpnCoreAdapter              tasks.VPNCoreAdapter
	wireGuardAdapter            tasks.VPNCoreAdapter
	hysteria2Adapter            tasks.VPNCoreAdapter
	shadowsocksAdapter          tasks.VPNCoreAdapter
	mtprotoAdapter              tasks.VPNCoreAdapter
	runtimeCleaner              runtimecleanup.Cleaner
	runtimeMutationDir          string
	runtimeMutationManagerReady bool
	removalManagerReady         bool
	recoveryResponse            *tasks.RuntimeRecoveryChallengeResponse
}

func NewRunner(cfg config.Config, configPath string, logger *slog.Logger) *Runner {
	runner := &Runner{
		cfg:               cfg,
		configPath:        configPath,
		client:            client.New(cfg.ManagerURL),
		logger:            logger,
		trafficCollector:  traffic.NoopCollector{},
		trafficTracker:    traffic.NewDeltaTracker(),
		presenceCollector: presence.NoopCollector{},
		vpnCoreAdapter: tasks.NewSingBoxVLESSAdapter(
			cfg.ConfigStagingDir,
			cfg.SingBoxPath,
			cfg.SingBoxServiceName,
		),
		wireGuardAdapter: tasks.NewWireGuardAdapter(
			cfg.WireGuardStagingDir,
			cfg.WGQuickPath,
			cfg.WGPath,
			cfg.WireGuardServiceName,
			cfg.WireGuardInterface,
		),
		hysteria2Adapter: tasks.NewHysteria2Adapter(
			cfg.Hysteria2StagingDir,
			cfg.Hysteria2Path,
			cfg.SSPath,
			cfg.Hysteria2ServiceName,
		),
		shadowsocksAdapter: tasks.NewSingBoxShadowsocksAdapter(
			cfg.ConfigStagingDir,
			cfg.SingBoxPath,
			cfg.SingBoxServiceName,
		),
		mtprotoAdapter: tasks.NewMTProtoAdapter(
			cfg.MTProtoStagingDir,
			cfg.MTGPath,
			cfg.MTProtoServiceName,
		),
		runtimeCleaner:     runtimecleanup.New(cfg),
		runtimeMutationDir: tasks.DefaultRuntimeMutationDir,
	}
	if cfg.TrafficCollectionEnabled {
		runner.trafficCollector = traffic.NewFileCollector(cfg.TrafficUsageFilePath)
	}
	if cfg.ClientPresenceEnabled {
		runner.presenceCollector = presence.NewRuntimeCollectorWithWireGuard(
			cfg.ActiveConfigPath,
			cfg.SingBoxServiceName,
			cfg.ClientPresenceFilePath,
			cfg.WireGuardActiveConfigPath,
			cfg.WireGuardInterface,
			cfg.WGPath,
		)
	}
	return runner
}

func (r *Runner) Run(ctx context.Context, once bool) error {
	if err := r.ensureRegistered(ctx); err != nil {
		return err
	}
	if once {
		if err := r.sendHeartbeat(ctx); err != nil {
			return err
		}
		if err := r.processNextTask(ctx); err != nil {
			return err
		}
		if err := r.reportTrafficUsage(ctx); err != nil {
			r.logger.Warn("report traffic usage failed", "error", err)
		}
		if err := r.reportClientPresence(ctx); err != nil {
			r.logger.Warn("report client presence failed", "error", err)
		}
		return nil
	}
	if err := r.sendHeartbeat(ctx); err != nil {
		r.logger.Warn("heartbeat failed", "error", err)
	}
	if err := r.processNextTask(ctx); err != nil {
		r.logger.Warn("process agent task failed", "error", err)
	}
	if err := r.reportTrafficUsage(ctx); err != nil {
		r.logger.Warn("report traffic usage failed", "error", err)
	}
	if err := r.reportClientPresence(ctx); err != nil {
		r.logger.Warn("report client presence failed", "error", err)
	}
	ticker := time.NewTicker(r.workPollInterval())
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			ticker.Reset(r.workPollInterval())
			if err := r.sendHeartbeat(ctx); err != nil {
				r.logger.Warn("heartbeat failed", "error", err)
			}
			if err := r.processNextTask(ctx); err != nil {
				r.logger.Warn("process agent task failed", "error", err)
			}
			if err := r.reportTrafficUsage(ctx); err != nil {
				r.logger.Warn("report traffic usage failed", "error", err)
			}
			if err := r.reportClientPresence(ctx); err != nil {
				r.logger.Warn("report client presence failed", "error", err)
			}
		}
	}
}

func (r *Runner) reportClientPresence(ctx context.Context) error {
	if !r.cfg.ClientPresenceEnabled {
		return nil
	}
	now := time.Now().UTC()
	if !r.lastPresenceReport.IsZero() && now.Sub(r.lastPresenceReport) < r.cfg.ClientPresenceInterval() {
		return nil
	}
	snapshot, err := r.presenceCollector.Collect(ctx)
	if err != nil {
		return fmt.Errorf("collect client presence: %w", err)
	}
	res, err := r.client.ReportClientPresence(ctx, r.cfg.AgentToken, snapshot)
	if err != nil {
		return fmt.Errorf("submit client presence: %w", err)
	}
	r.lastPresenceReport = now
	r.logger.Info("client presence report accepted", "agent_id", res.AgentID, "server_id", res.ServerID, "accepted", res.Accepted)
	return nil
}

func (r *Runner) ensureRegistered(ctx context.Context) error {
	if r.cfg.HasAgentCredentials() {
		return nil
	}
	if !r.cfg.HasRegistrationToken() {
		return errors.New("agent_token or registration_token is required")
	}
	info := systeminfo.Collect()
	res, err := r.client.Register(ctx, r.cfg, info)
	if err != nil {
		return err
	}
	r.cfg.AgentID = res.AgentID
	r.cfg.ServerID = res.ServerID
	r.cfg.AgentToken = res.AgentToken
	r.cfg.RegistrationToken = ""
	if err := r.cfg.Save(r.configPath); err != nil {
		return err
	}
	r.logger.Info("agent registered", "agent_id", r.cfg.AgentID, "server_id", r.cfg.ServerID)
	return nil
}

func (r *Runner) sendHeartbeat(ctx context.Context) error {
	info := systeminfo.Collect()
	r.prepareRuntimeMutationHeartbeat(&info)
	r.runtimeMutationManagerReady = false
	r.removalManagerReady = false
	res, err := r.client.Heartbeat(ctx, r.cfg.AgentToken, info)
	if err != nil {
		return err
	}
	r.runtimeMutationManagerReady = res.OK && res.AgentID == r.cfg.AgentID && res.ServerID == r.cfg.ServerID && res.RuntimeMutationFencingAccepted
	r.removalManagerReady = r.runtimeMutationManagerReady && res.CredentialRemovalAccepted
	r.logger.Info("heartbeat accepted", "agent_id", res.AgentID, "server_id", res.ServerID, "server_status", res.ServerStatus)
	return nil
}

func (r *Runner) reportTrafficUsage(ctx context.Context) error {
	if !r.cfg.TrafficCollectionEnabled {
		return nil
	}
	now := time.Now().UTC()
	if !r.lastTrafficReport.IsZero() && now.Sub(r.lastTrafficReport) < r.cfg.TrafficCollectionInterval() {
		return nil
	}
	r.lastTrafficReport = now

	snapshots, err := r.trafficCollector.Collect(ctx)
	if err != nil {
		return err
	}
	usageEvents := r.trafficTracker.BuildUsageEvents(snapshots)
	if len(usageEvents) == 0 {
		r.logger.Debug("no traffic usage delta available", "snapshots", len(snapshots))
		return nil
	}

	res, err := r.client.ReportTrafficUsage(ctx, r.cfg.AgentToken, usageEvents)
	if err != nil {
		return err
	}
	r.logger.Info("traffic usage report accepted", "agent_id", res.AgentID, "server_id", res.ServerID, "accepted", res.Accepted)
	return nil
}

func (r *Runner) processNextTask(ctx context.Context) error {
	r.replayRuntimeResult(ctx)
	work, err := r.client.NextWork(ctx, r.cfg.AgentToken)
	if err != nil {
		return err
	}
	if work.RecoveryChallenge != nil {
		return r.processRecoveryObservation(ctx, *work.RecoveryChallenge)
	}
	r.recoveryResponse = nil
	task := work.Task
	if task == nil {
		if r.removalManagerReady && r.credentialRemovalEnabled() {
			dir := r.runtimeMutationDir
			if dir == "" {
				dir = tasks.DefaultRuntimeMutationDir
			}
			executor := tasks.NewCredentialRemovalExecutor(r.cfg.ActiveConfigPath, dir, r.cfg.ServerID, r.cfg.AgentID, r.vpnCoreAdapter, r.cfg.ServiceControlEnabled)
			_, err := r.client.ProcessHeldRemoval(ctx, r.cfg.AgentToken, executor)
			return err
		}
		r.logger.Debug("no agent task available")
		return nil
	}

	return r.dispatchTask(ctx, *task)
}

func (r *Runner) dispatchTask(ctx context.Context, task tasks.ConfigTask) error {
	return r.withRuntimeMutation(ctx, task, func(taskCtx context.Context) error { return r.dispatchTaskUnfenced(taskCtx, task) })
}

func (r *Runner) dispatchTaskUnfenced(ctx context.Context, task tasks.ConfigTask) error {
	switch task.EffectiveKind() {
	case tasks.TaskKindVPNCoreService:
		return r.processVPNCoreServiceTask(ctx, task)
	case tasks.TaskKindVPNCoreInstall:
		return r.processVPNCoreInstallTask(ctx, task)
	case tasks.TaskKindDiagnostic:
		return r.processDiagnosticTask(ctx, task)
	case tasks.TaskKindPlatformUpdate:
		return r.processPlatformUpdateReconciliationTask(ctx, task)
	case tasks.TaskKindMaintenance:
		return r.processMaintenanceTask(ctx, task)
	case tasks.TaskKindConfigApply:
		return r.processConfigApplyTask(ctx, task)
	default:
		err := fmt.Errorf("unsupported agent task kind %q", task.Kind)
		report := map[string]any{"kind": task.Kind, "status": "rejected"}
		if completeErr := r.client.CompleteTaskFailed(ctx, r.cfg.AgentToken, task.ID, err.Error(), report); completeErr != nil {
			return fmt.Errorf("reject unsupported task kind: %v; report failure: %w", err, completeErr)
		}
		return err
	}
}

func (r *Runner) adapterStorage(adapter tasks.VPNCoreAdapter) (string, string) {
	if adapter.Descriptor().Core == "wireguard" {
		return r.cfg.WireGuardActiveConfigPath, r.cfg.WireGuardBackupDir
	}
	if adapter.Descriptor().Core == "hysteria" {
		return r.cfg.Hysteria2ActiveConfigPath, r.cfg.Hysteria2BackupDir
	}
	if adapter.Descriptor().Core == "mtg" {
		return r.cfg.MTProtoActiveConfigPath, r.cfg.MTProtoBackupDir
	}
	return r.cfg.ActiveConfigPath, r.cfg.ConfigBackupDir
}
