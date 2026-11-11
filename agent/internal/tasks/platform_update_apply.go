package tasks

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
)

const (
	platformUpdateVerifiedUpdater           = "/usr/local/lib/routegate/update/routegate-update-verified.sh"
	platformUpdateAgentBinary               = "/usr/local/bin/routegate-agent"
	platformUpdateSystemdRun                = "/usr/bin/systemd-run"
	platformUpdateRollbackIncompleteExitCode = 75
)

var (
	platformUpdateWorkerEnvironment = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin"}

	// ErrPlatformUpdateDispatchAmbiguous means the caller must not terminalize
	// or retry the remote update. Once the systemd-run process has started, a
	// later acknowledgement error cannot prove that the transient unit was not
	// accepted by systemd.
	ErrPlatformUpdateDispatchAmbiguous = errors.New("platform update dispatch outcome is ambiguous")
)

// DetachedPlatformUpdateCommand returns the fixed transient-systemd invocation
// used to detach a VPN update from routegate-agent.service. The caller supplies
// only one canonical task UUID; every privileged selector is reconstructed by
// trusted local RouteGate policy.
func DetachedPlatformUpdateCommand(taskID string) (string, []string, error) {
	if !canonicalTaskIDPattern.MatchString(taskID) {
		return "", nil, fmt.Errorf("platform update task id must be canonical UUIDv4")
	}
	unit := "routegate-vpn-update-" + taskID
	return platformUpdateSystemdRun, []string{
		platformUpdateSystemdRun,
		"--unit=" + unit,
		"--collect",
		"--no-block",
		"--property=UMask=0077",
		"--property=NoNewPrivileges=yes",
		platformUpdateAgentBinary,
		"--platform-update-worker-task=" + taskID,
	}, nil
}

// StartDetachedPlatformUpdate distinguishes the only deterministic launch
// failure from the post-start ambiguity boundary. If exec.Start fails, the
// systemd-run process never existed and no unit can have been accepted through
// this invocation. After Start succeeds, every Wait/context/non-zero failure is
// conservative ambiguity because systemd may already have accepted the unit.
func StartDetachedPlatformUpdate(ctx context.Context, taskID string) error {
	// Staging can take minutes. Revalidate the complete fixed execution runtime
	// immediately before resolving and starting systemd-run so readiness cannot
	// go stale across the download window.
	if !PlatformUpdateRuntimeReady() {
		return fmt.Errorf("platform update runtime is not safely ready immediately before detached launch")
	}
	path, argv, err := DetachedPlatformUpdateCommand(taskID)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, path, argv[1:]...)
	cmd.Env = platformUpdateWorkerEnvironment
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start detached platform update launcher: %w", err)
	}
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("%w: wait for detached platform update launcher: %v", ErrPlatformUpdateDispatchAmbiguous, err)
	}
	return nil
}

// RecordPlatformUpdatePreDispatchFailure durably records a deterministic
// failure before any host mutation. The create is no-replace: if any prior or
// concurrent receipt exists, callers must treat the task as ambiguous rather
// than overwriting evidence or making a replay runnable again.
func RecordPlatformUpdatePreDispatchFailure(taskID, targetVersion, code string) error {
	store := fixedPlatformUpdateReceiptStore()
	if _, err := store.CreatePrepared(taskID, targetVersion); err != nil {
		return fmt.Errorf("%w: persist prepared receipt for pre-dispatch failure: %v", ErrPlatformUpdateDispatchAmbiguous, err)
	}
	if _, err := store.MarkPreDispatchFailed(taskID, code); err != nil {
		return fmt.Errorf("persist platform update pre-dispatch failure: %w", err)
	}
	return nil
}

type platformUpdateStageFunc func(context.Context, string, PlatformUpdateRequest) (PlatformUpdateStagedCandidate, error)
type platformUpdateRuntimeReadyFunc func() bool

// preparePlatformUpdateReceiptBeforeReadiness closes the crash window between
// Manager claiming a job and the potentially slow verifier/runtime readiness
// probe. The no-replace prepared receipt is durable before the probe starts, so
// an Agent restart can always reconcile an in_progress task without redispatch.
func preparePlatformUpdateReceiptBeforeReadiness(store platformUpdateReceiptStore, taskID string, request PlatformUpdateRequest, runtimeReady platformUpdateRuntimeReadyFunc) error {
	if runtimeReady == nil {
		return fmt.Errorf("platform update runtime readiness probe is unavailable")
	}
	if _, err := store.CreatePrepared(taskID, request.TargetVersion); err != nil {
		return fmt.Errorf("%w: persist prepared receipt before runtime readiness: %v", ErrPlatformUpdateDispatchAmbiguous, err)
	}
	if runtimeReady() {
		return nil
	}
	if _, err := store.MarkPreDispatchFailed(taskID, "runtime_not_ready"); err != nil {
		return fmt.Errorf("%w: platform update runtime is not safely ready; persist deterministic failure: %v", ErrPlatformUpdateDispatchAmbiguous, err)
	}
	return fmt.Errorf("platform update runtime is not safely ready before staging")
}

// stagePlatformUpdateWithExistingPreparedReceipt performs staging only after a
// matching durable prepared receipt already exists. Deterministic staging and
// identity failures monotonically close that same receipt.
func stagePlatformUpdateWithExistingPreparedReceipt(ctx context.Context, store platformUpdateReceiptStore, taskID string, request PlatformUpdateRequest, stage platformUpdateStageFunc) (PlatformUpdateStagedCandidate, error) {
	if stage == nil {
		return PlatformUpdateStagedCandidate{}, fmt.Errorf("platform update stager is unavailable")
	}
	if err := acceptPreparedPlatformUpdateReceipt(store, taskID, request.TargetVersion); err != nil {
		return PlatformUpdateStagedCandidate{}, fmt.Errorf("%w: prepared receipt is not runnable before staging: %v", ErrPlatformUpdateDispatchAmbiguous, err)
	}

	candidate, err := stage(ctx, taskID, request)
	if err != nil {
		if _, receiptErr := store.MarkPreDispatchFailed(taskID, "staging_failed"); receiptErr != nil {
			return PlatformUpdateStagedCandidate{}, fmt.Errorf("%w: platform update staging failed: %v; persist deterministic failure: %v", ErrPlatformUpdateDispatchAmbiguous, err, receiptErr)
		}
		return PlatformUpdateStagedCandidate{}, err
	}
	if candidate.TaskID != taskID || candidate.TargetVersion != request.TargetVersion {
		if _, receiptErr := store.MarkPreDispatchFailed(taskID, "staged_identity_mismatch"); receiptErr != nil {
			return PlatformUpdateStagedCandidate{}, fmt.Errorf("%w: staged identity mismatch; persist deterministic failure: %v", ErrPlatformUpdateDispatchAmbiguous, receiptErr)
		}
		return PlatformUpdateStagedCandidate{}, fmt.Errorf("platform update staged identity mismatch")
	}
	return candidate, nil
}

// stagePlatformUpdateWithPreparedReceipt remains the focused test/helper entry
// point for proving that staging never starts before a no-replace receipt. The
// production dispatch path creates the receipt even earlier, before readiness.
func stagePlatformUpdateWithPreparedReceipt(ctx context.Context, store platformUpdateReceiptStore, taskID string, request PlatformUpdateRequest, stage platformUpdateStageFunc) (PlatformUpdateStagedCandidate, error) {
	if _, err := store.CreatePrepared(taskID, request.TargetVersion); err != nil {
		// A concurrent or prior writer may already have durable handoff evidence.
		// Never reinterpret that state as permission to retry staging or mutation.
		return PlatformUpdateStagedCandidate{}, fmt.Errorf("%w: persist prepared receipt before staging: %v", ErrPlatformUpdateDispatchAmbiguous, err)
	}
	return stagePlatformUpdateWithExistingPreparedReceipt(ctx, store, taskID, request, stage)
}

// PrepareAndStartDetachedPlatformUpdate performs the durable handoff required
// before systemd may accept a detached host mutation. Any evidence of a prior
// attempt is fail-closed to ambiguity so a replay can never become a second
// mutation attempt.
func PrepareAndStartDetachedPlatformUpdate(ctx context.Context, taskID string, request PlatformUpdateRequest) (PlatformUpdateStagedCandidate, error) {
	// No safe detached handoff exists for the experimental shared runtime fence.
	if RuntimeMutationStatePresent(DefaultRuntimeMutationDir) {
		return PlatformUpdateStagedCandidate{}, fmt.Errorf("%w: %w", ErrPlatformUpdateDispatchAmbiguous, ErrRuntimeMutationBlocked)
	}
	if !canonicalTaskIDPattern.MatchString(taskID) {
		return PlatformUpdateStagedCandidate{}, fmt.Errorf("platform update task id must be canonical UUIDv4")
	}
	if _, err := DecodePlatformUpdateRequest(mustMarshalPlatformUpdateRequest(request)); err != nil {
		return PlatformUpdateStagedCandidate{}, err
	}
	if prior, err := platformUpdateDispatchHasPriorState(taskID); err != nil {
		return PlatformUpdateStagedCandidate{}, fmt.Errorf("inspect prior platform update dispatch state: %w", err)
	} else if prior {
		return PlatformUpdateStagedCandidate{}, fmt.Errorf("%w: task already has local dispatch state", ErrPlatformUpdateDispatchAmbiguous)
	}

	store := fixedPlatformUpdateReceiptStore()
	// The receipt must exist before even the runtime readiness probe. That probe
	// hashes and executes the pinned verifier and can take seconds; a crash there
	// must still leave deterministic pre-mutation reconciliation evidence.
	if err := preparePlatformUpdateReceiptBeforeReadiness(store, taskID, request, PlatformUpdateRuntimeReady); err != nil {
		return PlatformUpdateStagedCandidate{}, err
	}
	candidate, err := stagePlatformUpdateWithExistingPreparedReceipt(ctx, store, taskID, request, NewPlatformUpdateStager().Stage)
	if err != nil {
		return PlatformUpdateStagedCandidate{}, err
	}
	// A second Agent process or reconciliation path may have terminalized a
	// prepared receipt while staging was in progress. Re-read it before starting
	// systemd so a terminal receipt can never be followed by a new worker launch.
	if err := acceptPreparedPlatformUpdateReceipt(store, taskID, request.TargetVersion); err != nil {
		return PlatformUpdateStagedCandidate{}, fmt.Errorf("%w: prepared receipt is no longer runnable before detached launch: %v", ErrPlatformUpdateDispatchAmbiguous, err)
	}
	if err := StartDetachedPlatformUpdate(ctx, taskID); err != nil {
		if errors.Is(err, ErrPlatformUpdateDispatchAmbiguous) {
			return PlatformUpdateStagedCandidate{}, err
		}
		// No systemd-run process was successfully accepted through this path, so
		// this remains a deterministic pre-dispatch failure and is reconcilable.
		if _, receiptErr := store.MarkPreDispatchFailed(taskID, "detached_launch_failed"); receiptErr != nil {
			return PlatformUpdateStagedCandidate{}, fmt.Errorf("detached launch failed: %v; persist pre-dispatch failure: %w", err, receiptErr)
		}
		return PlatformUpdateStagedCandidate{}, err
	}
	return candidate, nil
}

func platformUpdateDispatchHasPriorState(taskID string) (bool, error) {
	store := fixedPlatformUpdateReceiptStore()
	if _, err := store.Read(taskID); err == nil {
		return true, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}

	finalDir := filepath.Join(platformUpdateStagingRoot, taskID)
	if _, err := os.Lstat(finalDir); err == nil {
		return true, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	partials, err := filepath.Glob(filepath.Join(platformUpdateStagingRoot, ".partial-"+taskID+"-*"))
	if err != nil {
		return false, err
	}
	return len(partials) != 0, nil
}

// RunPlatformUpdateWorker is local-only. A Manager-facing dispatch must have
// already staged the exact release and durably persisted a matching prepared
// receipt before systemd-run is invoked. The worker verifies the handoff and
// monotonically crosses mutation_started before invoking the verified updater.
func RunPlatformUpdateWorker(taskID string) error {
	// A previously scheduled worker must check even if dispatch refused.
	if RuntimeMutationStatePresent(DefaultRuntimeMutationDir) {
		return ErrRuntimeMutationBlocked
	}
	if os.Geteuid() != 0 {
		return fmt.Errorf("platform update worker must run as root")
	}
	stageDir, bundle, err := validatedPlatformUpdateStage(taskID)
	if err != nil {
		return err
	}
	targetVersion, err := platformUpdateVersionFromBundle(bundle)
	if err != nil {
		return err
	}
	store := fixedPlatformUpdateReceiptStore()
	if err := acceptPreparedPlatformUpdateReceipt(store, taskID, targetVersion); err != nil {
		return err
	}

	// Revalidate the complete toolchain, verifier, fixed executables and every
	// parent chain inside the detached worker immediately before crossing the
	// durable mutation boundary. A runtime changed after staging therefore
	// terminalizes as a deterministic pre-dispatch failure, never as mutation.
	if !PlatformUpdateRuntimeReady() {
		if _, receiptErr := store.MarkPreDispatchFailed(taskID, "runtime_not_ready_before_mutation"); receiptErr != nil {
			return fmt.Errorf("platform update runtime is not safely ready before mutation; persist failure receipt: %w", receiptErr)
		}
		return fmt.Errorf("platform update runtime is not safely ready before mutation")
	}
	if _, err := store.MarkMutationStarted(taskID); err != nil {
		return fmt.Errorf("persist platform update mutation start: %w", err)
	}

	argv := []string{
		"apply",
		"--manifest", filepath.Join(stageDir, "release-manifest.json"),
		"--manifest-attestation", filepath.Join(stageDir, "release-manifest.attestation.json"),
		"--checksums", filepath.Join(stageDir, "SHA256SUMS"),
		"--bundle", bundle,
		"--bundle-attestation", filepath.Join(stageDir, "release-bundles.attestation.json"),
		"--role", "vpn",
	}
	cmd := exec.Command(platformUpdateVerifiedUpdater, argv...)
	cmd.Env = platformUpdateWorkerEnvironment
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		_, receiptErr := store.MarkFailed(taskID, "updater_start_failed")
		if receiptErr != nil {
			return fmt.Errorf("start verified platform updater: %v; persist failure receipt: %w", err, receiptErr)
		}
		return fmt.Errorf("start verified platform updater: %w", err)
	}

	signals := make(chan os.Signal, 2)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(signals)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	for {
		select {
		case sig := <-signals:
			if cmd.Process != nil {
				_ = cmd.Process.Signal(sig)
			}
		case waitErr := <-done:
			if waitErr == nil {
				if _, err := store.MarkSucceeded(taskID); err != nil {
					return fmt.Errorf("persist platform update success receipt: %w", err)
				}
				return nil
			}
			if platformUpdateWaitOutcomeUnknown(waitErr) {
				code := platformUpdateOutcomeUnknownCode(waitErr)
				if _, err := store.MarkOutcomeUnknown(taskID, code); err != nil {
					return fmt.Errorf("verified platform updater outcome unknown: %v; persist unknown receipt: %w", waitErr, err)
				}
				return fmt.Errorf("verified platform updater outcome unknown: %w", waitErr)
			}
			if _, err := store.MarkFailed(taskID, "verified_updater_failed"); err != nil {
				return fmt.Errorf("verified platform updater failed: %v; persist failure receipt: %w", waitErr, err)
			}
			return fmt.Errorf("verified platform updater failed: %w", waitErr)
		}
	}
}

func platformUpdateWaitOutcomeUnknown(waitErr error) bool {
	var exitErr *exec.ExitError
	if !errors.As(waitErr, &exitErr) || exitErr.ProcessState == nil {
		return true
	}
	status, ok := exitErr.ProcessState.Sys().(syscall.WaitStatus)
	if !ok {
		return true
	}
	return status.Signaled() || exitErr.ExitCode() == platformUpdateRollbackIncompleteExitCode
}

func platformUpdateOutcomeUnknownCode(waitErr error) string {
	var exitErr *exec.ExitError
	if errors.As(waitErr, &exitErr) && exitErr.ExitCode() == platformUpdateRollbackIncompleteExitCode {
		return "rollback_incomplete"
	}
	return "verified_updater_signaled"
}

func acceptPreparedPlatformUpdateReceipt(store platformUpdateReceiptStore, taskID, targetVersion string) error {
	receipt, err := store.Read(taskID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("platform update prepared receipt is missing")
		}
		return fmt.Errorf("inspect platform update prepared receipt: %w", err)
	}
	if receipt.TargetVersion != targetVersion {
		return fmt.Errorf("platform update prepared receipt version mismatch")
	}
	if receipt.Phase == PlatformUpdateReceiptMutationStarted {
		reconciled, reconcileErr := store.ReconcileInterrupted(taskID)
		if reconcileErr != nil {
			return fmt.Errorf("reconcile orphaned platform update receipt: %w", reconcileErr)
		}
		return fmt.Errorf("platform update task has unknown prior outcome: %s", reconciled.Code)
	}
	if receipt.Phase != PlatformUpdateReceiptPrepared || receipt.MutationStarted {
		return fmt.Errorf("platform update task receipt is not runnable in phase %s", receipt.Phase)
	}
	return nil
}

func validatedPlatformUpdateStage(taskID string) (string, string, error) {
	if !canonicalTaskIDPattern.MatchString(taskID) {
		return "", "", fmt.Errorf("platform update task id must be canonical UUIDv4")
	}
	if err := validateRootOwnedNonWritableDir(platformUpdateStagingRoot); err != nil {
		return "", "", fmt.Errorf("platform update staging root is unsafe: %w", err)
	}
	stageDir := filepath.Join(platformUpdateStagingRoot, taskID)
	if err := validateRootOwnedNonWritableDir(stageDir); err != nil {
		return "", "", fmt.Errorf("platform update staged candidate is unsafe: %w", err)
	}

	required := map[string]struct{}{
		"release-manifest.json":             {},
		"release-manifest.attestation.json": {},
		"SHA256SUMS":                        {},
		"release-bundles.attestation.json":  {},
	}
	entries, err := os.ReadDir(stageDir)
	if err != nil {
		return "", "", fmt.Errorf("read platform update staged candidate: %w", err)
	}
	if len(entries) != 5 {
		return "", "", fmt.Errorf("platform update staged candidate must contain exactly five files")
	}

	bundle := ""
	for _, entry := range entries {
		name := entry.Name()
		path := filepath.Join(stageDir, name)
		if _, ok := required[name]; ok {
			if err := validateRootOwnedNonWritableRegular(path); err != nil {
				return "", "", fmt.Errorf("unsafe staged platform update file %s: %w", name, err)
			}
			continue
		}
		if bundle != "" || !isCanonicalPlatformUpdateBundleName(name) {
			return "", "", fmt.Errorf("platform update staged candidate contains unexpected or duplicate bundle entry")
		}
		if err := validateRootOwnedNonWritableRegular(path); err != nil {
			return "", "", fmt.Errorf("unsafe staged platform update bundle: %w", err)
		}
		bundle = path
	}
	if bundle == "" {
		return "", "", fmt.Errorf("platform update staged candidate bundle is missing")
	}
	return stageDir, bundle, nil
}

func isCanonicalPlatformUpdateBundleName(name string) bool {
	if filepath.Base(name) != name {
		return false
	}
	_, err := platformUpdateVersionFromBundle(name)
	return err == nil
}

func platformUpdateVersionFromBundle(path string) (string, error) {
	name := filepath.Base(path)
	const prefix = "routegate-"
	if !strings.HasPrefix(name, prefix) {
		return "", fmt.Errorf("platform update bundle name is not canonical")
	}
	for _, arch := range []string{"amd64", "arm64"} {
		suffix := "-linux-" + arch + ".tar.gz"
		if !strings.HasSuffix(name, suffix) {
			continue
		}
		version := strings.TrimSuffix(strings.TrimPrefix(name, prefix), suffix)
		if routeGateReleaseVersionPattern.MatchString(version) {
			return version, nil
		}
	}
	return "", fmt.Errorf("platform update bundle name is not canonical")
}

func validateRootOwnedNonWritableDir(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("not a regular directory")
	}
	return validateRootOwnedNonWritable(info)
}

func validateRootOwnedNonWritableRegular(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("not a regular file")
	}
	return validateRootOwnedNonWritable(info)
}

func validateRootOwnedNonWritable(info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("filesystem ownership metadata unavailable")
	}
	if stat.Uid != 0 {
		return fmt.Errorf("not root-owned")
	}
	if info.Mode().Perm()&0022 != 0 {
		return fmt.Errorf("group/world writable")
	}
	return nil
}
