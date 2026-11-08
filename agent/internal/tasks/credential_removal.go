package tasks

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type removalGenerationObserver interface {
	ObserveGeneration(context.Context, string) (RuntimeGeneration, error)
}

// CredentialRemovalExecutor uses trusted local paths and one locally selected
// sing-box adapter. It never selects or restarts other protocol adapters.
type CredentialRemovalExecutor struct {
	activePath, receiptDir, serverID, agentID string
	adapter                                   VPNCoreAdapter
	serviceControl                            bool
}

func NewCredentialRemovalExecutor(activePath, receiptDir, serverID, agentID string, adapter VPNCoreAdapter, serviceControl bool) CredentialRemovalExecutor {
	return CredentialRemovalExecutor{activePath: activePath, receiptDir: receiptDir, serverID: serverID, agentID: agentID, adapter: adapter, serviceControl: serviceControl}
}

type removalReceipt struct {
	Fingerprint       string                  `json:"fingerprint"`
	Held              bool                    `json:"held,omitempty"`
	RecoveryAttemptID string                  `json:"recoveryAttemptId,omitempty"`
	Acknowledged      bool                    `json:"acknowledged,omitempty"`
	Report            CredentialRemovalReport `json:"report"`
}

func (e CredentialRemovalExecutor) Execute(ctx context.Context, task ConfigTask) (CredentialRemovalReport, error) {
	return e.execute(ctx, task, time.Time{})
}

// ExecuteHeld is the entry point for authenticated Manager delivery. The
// wall-clock deadline is converted to the local monotonic clock once, and
// limits admission only. An admitted mutation still needs to finish safely.
func (e CredentialRemovalExecutor) ExecuteHeld(ctx context.Context, task ConfigTask) (CredentialRemovalReport, error) {
	now := time.Now()
	if task.DeliveryExpiresAt == nil {
		return CredentialRemovalReport{}, errors.New("credential removal delivery deadline missing")
	}
	remaining := task.DeliveryExpiresAt.Sub(now)
	if remaining <= 0 || remaining > 2*time.Minute {
		return CredentialRemovalReport{}, errors.New("credential removal delivery deadline invalid")
	}
	return e.execute(ctx, task, now.Add(remaining))
}

func (e CredentialRemovalExecutor) execute(ctx context.Context, task ConfigTask, deadline time.Time) (CredentialRemovalReport, error) {
	report := CredentialRemovalReport{SchemaVersion: 1, State: "failed", Code: "invalid_request"}
	deny := func(code string) (CredentialRemovalReport, error) {
		report.Code = code
		return report, errors.New("credential removal: " + code)
	}
	request := task.CredentialRemoval
	if task.EffectiveKind() != TaskKindCredentialRemoval || request == nil || request.SchemaVersion != 1 || !request.SharedProcessRestartAcknowledged ||
		task.ServerID != e.serverID || task.AgentID != e.agentID || task.Action != "apply" || task.Status != "in_progress" {
		return deny("invalid_request")
	}
	for _, id := range []string{task.ID, task.ServerID, task.AgentID, task.ConfigVersionID, request.OperationID, request.AccountID, request.BaselineVersionID} {
		if !canonicalTaskIDPattern.MatchString(id) {
			return deny("invalid_identity")
		}
	}
	if _, ok := normalizedRemovalUUID(request.VLESSUUID); !ok || !validRemovalHash(task.ConfigHash) || !validRemovalHash(request.BaselineRuntimeHash) ||
		!validRemovalHash(request.CandidateRuntimeHash) || request.BaselineRuntimeHash == request.CandidateRuntimeHash || request.BaselineVersionID == task.ConfigVersionID {
		return deny("invalid_identity")
	}
	if !e.serviceControl || e.adapter == nil {
		return deny("service_control_unavailable")
	}
	descriptor := e.adapter.Descriptor()
	observer, ok := e.adapter.(removalGenerationObserver)
	if !ok || descriptor.Core != "sing-box" || descriptor.Protocol != "vless" {
		return deny("adapter_not_supported")
	}
	envelope, _, _, err := canonicalRemovalJSON(task.RenderedConfig)
	if err != nil || envelope["schemaVersion"] != "routegate.config.v1" {
		return deny("invalid_envelope")
	}
	server, _ := envelope["server"].(map[string]any)
	agent, _ := envelope["agent"].(map[string]any)
	if server["id"] != task.ServerID || agent["id"] != task.AgentID {
		return deny("invalid_envelope")
	}
	candidate, err := json.Marshal(envelope["singBox"])
	if err != nil {
		return deny("invalid_candidate")
	}
	_, candidate, candidateHash, err := canonicalRemovalJSON(candidate)
	if err != nil || candidateHash != request.CandidateRuntimeHash {
		return deny("candidate_hash_mismatch")
	}
	report = CredentialRemovalReport{SchemaVersion: 1, JobID: task.ID, OperationID: request.OperationID, ServerID: task.ServerID, AgentID: task.AgentID,
		AccountID: request.AccountID, BaselineVersionID: request.BaselineVersionID, ConfigVersionID: task.ConfigVersionID, ConfigHash: task.ConfigHash,
		BaselineRuntimeHash: request.BaselineRuntimeHash, State: "failed"}
	fingerprintBytes, _ := json.Marshal(struct {
		Report  CredentialRemovalReport
		Request CredentialRemovalRequest
	}{report, *request})
	fingerprintSum := sha256.Sum256(fingerprintBytes)
	fingerprint := hex.EncodeToString(fingerprintSum[:])
	lock, err := e.lock()
	if err != nil {
		return deny("local_state_unavailable")
	}
	defer func() { _ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN); _ = lock.Close() }()
	expired := func() bool { return !deadline.IsZero() && !time.Now().Before(deadline) }
	if expired() {
		return deny("delivery_expired")
	}
	// Check before receipt replay too: an interrupted legacy operation can
	// invalidate a previously successful removal's evidence.
	if !runtimeMarkerAbsent(filepath.Join(e.receiptDir, "mutation-inflight.json")) || !runtimeMarkerAbsent(filepath.Join(e.receiptDir, runtimeResultFile)) || !runtimeRecoveryStateAbsent(e.receiptDir) {
		report.State = "recovery_required"
		return deny("runtime_recovery_required")
	}
	receiptPath := filepath.Join(e.receiptDir, request.OperationID+".json")
	fencePath := filepath.Join(e.receiptDir, "inflight.json")
	if data, readErr := readRemovalFile(receiptPath, 16384, true); readErr == nil {
		var old removalReceipt
		if json.Unmarshal(data, &old) != nil || old.Fingerprint != fingerprint || !sameRemovalBinding(old.Report, report) {
			return deny("operation_identity_changed")
		}
		if validSavedRemovalFailure(old.Report) {
			return old.Report, errors.New("credential removal requires reconciliation")
		}
		if old.Report.State != "succeeded" {
			if old.Report.State == "prepared" {
				report.State = "failed"
				return deny("interrupted_before_mutation")
			}
			if old.Report.State != "failed" {
				report.State = "recovery_required"
				return deny("interrupted_or_failed_mutation")
			}
			return deny("previous_attempt_failed")
		}
		if old.Report.ActiveRuntimeHash != request.CandidateRuntimeHash || !validRemovalGeneration(old.Report.Before) || !validRemovalGeneration(old.Report.After) || old.Report.Before.InvocationID == old.Report.After.InvocationID || old.Report.ListenerPort < 1 || old.Report.ListenerPort > 65535 || old.Report.Code != "" || old.Report.SessionEffect != "shared_process_restart_observed" {
			return deny("invalid_receipt")
		}
		// Re-delivery after a lost acknowledgment is read-only. It must match
		// both the measured file and the exact service generation in the receipt.
		current, err := readRemovalFile(e.activePath, removalMaxConfigBytes, false)
		_, _, hash, hashErr := canonicalRemovalJSON(current)
		generation, generationErr := observer.ObserveGeneration(ctx, e.activePath)
		if err != nil || hashErr != nil || hash != request.CandidateRuntimeHash || generationErr != nil || generation != old.Report.After {
			report.State = "recovery_required"
			return deny("completed_evidence_changed")
		}
		if listener, err := e.adapter.CheckHealth(ctx, e.activePath); err != nil || listener.Port != old.Report.ListenerPort {
			report.State = "recovery_required"
			return deny("completed_listener_unhealthy")
		}
		current, err = readRemovalFile(e.activePath, removalMaxConfigBytes, false)
		_, _, hash, hashErr = canonicalRemovalJSON(current)
		generation, generationErr = observer.ObserveGeneration(ctx, e.activePath)
		_, enabledErr := e.adapter.IsEnabled(ctx)
		if err != nil || hashErr != nil || hash != request.CandidateRuntimeHash || generationErr != nil || generation != old.Report.After || enabledErr != nil {
			report.State = "recovery_required"
			return deny("completed_evidence_changed")
		}
		if fence, err := readRemovalFile(fencePath, 16384, true); err == nil {
			if string(fence) != fingerprint {
				report.State = "recovery_required"
				return deny("runtime_recovery_required")
			}
			if err = removeRemovalFile(fencePath); err != nil {
				report.State = "recovery_required"
				return deny("fence_clear_failed")
			}
		} else if !os.IsNotExist(err) {
			report.State = "recovery_required"
			return deny("runtime_recovery_required")
		}
		return old.Report, nil
	} else if !os.IsNotExist(readErr) {
		return deny("receipt_unreadable")
	}
	if _, err = os.Lstat(fencePath); err == nil || !os.IsNotExist(err) {
		report.State = "recovery_required"
		return deny("runtime_recovery_required")
	}
	save := func() error {
		data, _ := json.Marshal(removalReceipt{Fingerprint: fingerprint, Held: !deadline.IsZero(), Report: report})
		return writeRemovalFile(receiptPath, data)
	}
	report.State = "prepared"
	if err = save(); err != nil {
		return deny("receipt_write_failed")
	}
	mutating := false
	fail := func(code string) (CredentialRemovalReport, error) {
		report.State = "failed"
		if mutating {
			report.State = "recovery_required"
		}
		report.Code = code
		if save() != nil {
			report.Code = "receipt_write_failed"
		}
		return report, errors.New("credential removal: " + report.Code)
	}
	baseline, err := readRemovalFile(e.activePath, removalMaxConfigBytes, false)
	if err != nil {
		return fail("baseline_unreadable")
	}
	port, err := verifyRemovalRuntimeDelta(baseline, candidate, *request)
	if err != nil {
		return fail("baseline_or_delta_mismatch")
	}
	before, err := observer.ObserveGeneration(ctx, e.activePath)
	if err != nil || !validRemovalGeneration(before) {
		return fail("runtime_generation_unavailable")
	}
	if _, err = e.adapter.IsEnabled(ctx); err != nil {
		return fail("runtime_not_enabled")
	}
	stagePath := filepath.Join(e.receiptDir, task.ID+".candidate.json")
	if err = writeRemovalFile(stagePath, candidate); err != nil {
		return fail("stage_failed")
	}
	defer os.Remove(stagePath)
	if _, err = e.adapter.Validate(ctx, stagePath); err != nil {
		return fail("validation_failed")
	}
	// Slow validation may have overlapped an out-of-band edit or restart.
	current, err := readRemovalFile(e.activePath, removalMaxConfigBytes, false)
	_, _, currentHash, hashErr := canonicalRemovalJSON(current)
	currentGeneration, generationErr := observer.ObserveGeneration(ctx, e.activePath)
	if err != nil || hashErr != nil || currentHash != request.BaselineRuntimeHash || generationErr != nil || currentGeneration != before {
		return fail("baseline_changed_during_validation")
	}
	if ctx.Err() != nil {
		return fail("cancelled_before_mutation")
	}
	if expired() {
		return fail("delivery_expired")
	}
	report.Before = before
	report.State = "applying"
	if err = save(); err != nil {
		return fail("receipt_write_failed")
	}
	// The durable fence is written before replacing the active file. No other
	// removal may proceed across uncertain mutation, even for another account.
	if err = writeRemovalFile(fencePath, []byte(fingerprint)); err != nil {
		return fail("fence_write_failed")
	}
	var blockedPromotion string
	err = publishRemovalFileCheckpoint(e.activePath, candidate, true, func(step string, _ *os.File) error {
		if step != "before-rename" {
			return nil
		}
		switch {
		case ctx.Err() != nil:
			blockedPromotion = "cancelled_before_mutation"
		case expired():
			blockedPromotion = "delivery_expired"
		default:
			return nil
		}
		return ErrCredentialRemoval
	})
	if blockedPromotion != "" {
		failed, failure := fail(blockedPromotion)
		// The active file has not been touched. Clear only our own fence
		// after durably recording that the attempt stopped before mutation.
		if failed.Code == blockedPromotion && removeRemovalFile(fencePath) != nil {
			report.State = "recovery_required"
			return deny("fence_clear_failed")
		}
		return failed, failure
	}
	mutating = true
	if err != nil {
		return fail("promotion_failed")
	}
	if _, err = e.adapter.Restart(ctx); err != nil {
		return fail("restart_failed")
	}
	after, err := observeRemovalRestart(ctx, observer, e.activePath, before)
	if err != nil {
		return fail("restart_not_verified")
	}
	if _, err = e.adapter.IsEnabled(ctx); err != nil {
		return fail("runtime_not_enabled")
	}
	listener, err := e.adapter.CheckHealth(ctx, e.activePath)
	if err != nil || listener.Port != port {
		return fail("listener_not_verified")
	}
	active, err := readRemovalFile(e.activePath, removalMaxConfigBytes, false)
	_, _, activeHash, hashErr := canonicalRemovalJSON(active)
	stable, generationErr := observer.ObserveGeneration(ctx, e.activePath)
	if err != nil || hashErr != nil || activeHash != request.CandidateRuntimeHash || generationErr != nil || stable != after {
		return fail("applied_evidence_changed")
	}
	report.After = after
	report.ActiveRuntimeHash = activeHash
	report.ListenerPort = port
	report.SessionEffect = "shared_process_restart_observed"
	report.State = "succeeded"
	report.Code = ""
	if err = save(); err != nil {
		return fail("receipt_write_failed")
	}
	if err = removeRemovalFile(fencePath); err != nil {
		return fail("fence_clear_failed")
	}
	return report, nil
}

// A Type=simple unit can report restart completion before exec has made
// the replacement process observable. Retry observation, never the restart.
func observeRemovalRestart(ctx context.Context, observer removalGenerationObserver, activePath string, before RuntimeGeneration) (RuntimeGeneration, error) {
	checkCtx, cancel := context.WithTimeout(ctx, defaultListenerHealthTimeout)
	defer cancel()
	ticker := time.NewTicker(defaultListenerHealthRetryInterval)
	defer ticker.Stop()
	for checkCtx.Err() == nil {
		after, err := observer.ObserveGeneration(checkCtx, activePath)
		if err == nil && checkCtx.Err() == nil && validRemovalGeneration(after) && after.InvocationID != before.InvocationID {
			return after, nil
		}
		select {
		case <-checkCtx.Done():
		case <-ticker.C:
		}
	}
	return RuntimeGeneration{}, ErrCredentialRemoval
}

func validRemovalGeneration(value RuntimeGeneration) bool {
	raw, err := hex.DecodeString(value.InvocationID)
	started, timeErr := strconv.ParseUint(value.StartedMonotonic, 10, 64)
	return err == nil && len(raw) == 16 && value.InvocationID != strings.Repeat("0", 32) && value.PID > 0 && timeErr == nil && started > 0
}

func (e CredentialRemovalExecutor) lock() (*os.File, error) {
	if !filepath.IsAbs(e.activePath) {
		return nil, ErrCredentialRemoval
	}
	return lockRuntimeMutation(e.receiptDir)
}

func readRemovalFile(path string, limit int64, private bool) ([]byte, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > limit || info.Mode().Perm()&0022 != 0 || stat.Uid != uint32(os.Geteuid()) || (private && info.Mode().Perm()&0077 != 0) {
		return nil, ErrCredentialRemoval
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(data)) != info.Size() {
		return nil, ErrCredentialRemoval
	}
	return data, nil
}

// Like the existing platform-update receipt writer: fsync the private temp
// file, rename atomically, then fsync its directory before reporting success.
func writeRemovalFile(path string, data []byte) error {
	return publishRemovalFile(path, data, true)
}

// Immutable metadata publication must fail if any process won the destination
// race. A crash before the temporary link is removed retains both names; strict
// single-link readers fail closed, and no published evidence is removed.
func writeRemovalFileNoReplace(path string, data []byte) error {
	return publishRemovalFile(path, data, false)
}

func publishRemovalFile(path string, data []byte, replace bool) error {
	return publishRemovalFileCheckpoint(path, data, replace, nil)
}

func publishRemovalFileCheckpoint(path string, data []byte, replace bool, checkpoint func(string, *os.File) error) error {
	cut := func(step string) error {
		if checkpoint != nil {
			return checkpoint(step, nil)
		}
		return nil
	}
	dir := filepath.Dir(path)
	file, err := os.CreateTemp(dir, ".removal-*")
	if err != nil {
		return err
	}
	tmp := file.Name()
	defer os.Remove(tmp)
	defer file.Close()
	if err = file.Chmod(0600); err != nil {
		return err
	}
	if _, err = file.Write(data); err != nil {
		return err
	}
	if err = file.Sync(); err != nil {
		return err
	}
	if checkpoint != nil {
		if err = checkpoint("file", file); err != nil {
			return err
		}
	}
	if err = file.Close(); err != nil {
		return err
	}
	if replace {
		if err = cut("before-rename"); err != nil {
			return err
		}
		if err = os.Rename(tmp, path); err != nil {
			return err
		}
	} else {
		if err = os.Link(tmp, path); err != nil {
			return err
		}
		if err = cut("linked"); err != nil {
			return err
		}
		if err = os.Remove(tmp); err != nil {
			return err
		}
	}
	if err = cut("unlinked-temp"); err != nil {
		return err
	}
	directory, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer directory.Close()
	if err = directory.Sync(); err != nil {
		return err
	}
	return cut("durable")
}

func removeRemovalFile(path string) error {
	return removeRemovalFileCheckpoint(path, nil)
}

func removeRemovalFileCheckpoint(path string, checkpoint func() error) error {
	if err := os.Remove(path); err != nil {
		return err
	}
	if checkpoint != nil {
		if err := checkpoint(); err != nil {
			return err
		}
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func sameRemovalBinding(a, b CredentialRemovalReport) bool {
	return a.SchemaVersion == b.SchemaVersion && a.JobID == b.JobID && a.OperationID == b.OperationID && a.ServerID == b.ServerID && a.AgentID == b.AgentID && a.AccountID == b.AccountID && a.BaselineVersionID == b.BaselineVersionID && a.ConfigVersionID == b.ConfigVersionID && a.ConfigHash == b.ConfigHash && a.BaselineRuntimeHash == b.BaselineRuntimeHash
}

// Only fixed, secret-free outcomes with measured pre-mutation identity can be
// replayed. Keep the exact saved report after a lost HTTP acknowledgement.
func validSavedRemovalFailure(report CredentialRemovalReport) bool {
	switch report.Code {
	case "interrupted_or_failed_mutation", "promotion_failed", "restart_failed", "restart_not_verified", "runtime_not_enabled",
		"listener_not_verified", "applied_evidence_changed", "receipt_write_failed", "fence_clear_failed":
	default:
		return false
	}
	return report.State == "recovery_required" && validRemovalGeneration(report.Before) &&
		(report.After == (RuntimeGeneration{}) || validRemovalGeneration(report.After)) &&
		(report.ActiveRuntimeHash == "" || validRemovalHash(report.ActiveRuntimeHash)) &&
		(report.ListenerPort == 0 || (report.ListenerPort > 0 && report.ListenerPort <= 65535)) &&
		(report.SessionEffect == "" || report.SessionEffect == "shared_process_restart_observed")
}
