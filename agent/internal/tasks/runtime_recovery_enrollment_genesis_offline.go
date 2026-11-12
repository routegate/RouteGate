package tasks

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

// RG-140 OFFLINE, PACKAGE-PRIVATE PROTOTYPE ONLY. None of these functions is
// called by the Agent Runner, Manager, installer, startup, or mutation gate.
// In particular a Manager "prepared_unattested" row does not enroll the host.
//
// The first locally durable record is deliberately INCOMPATIBLE with the
// existing prototype mutation journal reader: the latter refuses this
// additional filename and cannot silently run from an incomplete enrollment.
// A future versioned journal grammar must be reviewed before any activation.
const recoveryEnrollmentGenesisFilenameV1 = "enrollment-genesis-v1.json"
const recoveryEnrollmentGenesisDomainV1 = "routegate/rg140/offline-enrollment-genesis/v1"

type recoveryEnrollmentGenesisV1 struct {
	SchemaVersion             int    `json:"schemaVersion"`
	ManagerPreparationID      string `json:"managerPreparationId"`
	AgentID                   string `json:"agentId"`
	ServerID                  string `json:"serverId"`
	AgentCredentialGeneration int64  `json:"agentCredentialGeneration"`
	ManagerPreparationPhase   string `json:"managerPreparationPhase"`
	NextJournalSequence       int    `json:"nextJournalSequence"`
}

// Deliberately no IsEnrolled, CanDispatch, CanUnlock, ValidatedOrigin,
// ReadyForMutation or CanRecover flag.
type recoveryEnrollmentGenesisObservationV1 struct {
	GenesisSHA256 string
	Unattested    bool
}

// The caller must provide information separately obtained from a trusted
// Manager channel in a FUTURE protocol. This offline comparison does not
// authenticate that channel; a compromised Agent can forge these fields.
func validRecoveryEnrollmentGenesisV1(v recoveryEnrollmentGenesisV1) bool {
	return v.SchemaVersion == 1 &&
		canonicalTaskIDPattern.MatchString(v.ManagerPreparationID) &&
		canonicalTaskIDPattern.MatchString(v.AgentID) &&
		canonicalTaskIDPattern.MatchString(v.ServerID) &&
		v.AgentCredentialGeneration > 0 &&
		v.ManagerPreparationPhase == "prepared_unattested" &&
		v.NextJournalSequence == 1
}

func recoveryEnrollmentGenesisDigestV1(v recoveryEnrollmentGenesisV1) (string, error) {
	if !validRecoveryEnrollmentGenesisV1(v) {
		return "", errRecoveryJournalUntrustedV1
	}
	raw, err := json.Marshal(v)
	if err != nil || len(raw) == 0 || len(raw) > recoveryJournalDiskRecordLimitV1 {
		return "", errRecoveryJournalUntrustedV1
	}
	h := sha256.New()
	_, _ = h.Write([]byte(recoveryEnrollmentGenesisDomainV1))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write(raw)
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Only an ALREADY EXISTING, owner-controlled runtime-mutation root may be
// used. lockRuntimeMutation can provision missing state for legacy callers,
// so we refuse a missing root OR missing lock BEFORE calling it. This
// prevents this prototype from creating a runtime state on legacy Agent
// installations that were never configured for mutation fencing.
func holdExistingRuntimeMutationForGenesisV1(dir string) (*os.File, error) {
	if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir {
		return nil, errRecoveryJournalUntrustedV1
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return nil, errRecoveryJournalUntrustedV1
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != uint32(os.Geteuid()) {
		return nil, errRecoveryJournalUntrustedV1
	}
	lockPath := filepath.Join(dir, "runtime.lock")
	info, err = os.Lstat(lockPath)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return nil, errRecoveryJournalUntrustedV1
	}
	st, ok = info.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != uint32(os.Geteuid()) || st.Nlink != 1 {
		return nil, errRecoveryJournalUntrustedV1
	}
	held, err := lockRuntimeMutation(dir)
	if err != nil {
		return nil, errRecoveryJournalUntrustedV1
	}
	// Always acquire the existing mutation flock FIRST, journal-directory
	// flock SECOND. Do not reverse this ordering anywhere in the protocol.
	for _, name := range []string{"mutation-inflight.json", "inflight.json", runtimeResultFile} {
		if !runtimeMarkerAbsent(filepath.Join(dir, name)) {
			_ = held.Close()
			return nil, errRecoveryJournalUntrustedV1
		}
	}
	return held, nil
}

// writeRecoveryEnrollmentGenesisOfflineV1 simulates the first durable local
// enrollment write after a Manager PREPARATION. It only operates on two
// existing private test directories and is never invoked by startup.
//
// Requires both the REAL existing Agent runtime-mutation flock AND the
// journal-directory flock, both held through two file + directory fsync
// sequences (runtime continuity anchor first, journal genesis second).
// Both created files are immutable O_EXCL with no cleanup, retry, rename or repair.
// Even on success no remote origin, Agent process, or journal completeness
// is attested. Fault steps model syscall boundaries, not power-loss physics.
func writeRecoveryEnrollmentGenesisOfflineV1(
	ctx context.Context,
	runtimeDir, journalDir string,
	genesis recoveryEnrollmentGenesisV1,
	step func(string) error,
) error {
	if ctx == nil || ctx.Err() != nil || !validRecoveryEnrollmentGenesisV1(genesis) ||
		validateRecoveryEnrollmentDistinctRootsV1(runtimeDir, journalDir) != nil {
		return errRecoveryJournalUntrustedV1
	}
	runtimeLock, err := holdExistingRuntimeMutationForGenesisV1(runtimeDir)
	if err != nil {
		return errRecoveryJournalUntrustedV1
	}
	defer runtimeLock.Close()
	runtimeDirectory, err := openRecoveryEnrollmentAnchorRootV1(runtimeDir)
	if err != nil {
		return errRecoveryJournalUntrustedV1
	}
	defer runtimeDirectory.Close()
	journalLock, err := openRecoveryJournalDiskV1(journalDir)
	if err != nil {
		return errRecoveryJournalUntrustedV1
	}
	defer journalLock.Close()

	names, err := journalLock.Readdirnames(-1)
	if err != nil || len(names) != 0 || ctx.Err() != nil {
		return errRecoveryJournalUntrustedV1
	}
	raw, err := json.Marshal(genesis)
	if err != nil || len(raw) == 0 || len(raw) > recoveryJournalDiskRecordLimitV1 {
		return errRecoveryJournalUntrustedV1
	}
	// A runtime-root continuity anchor is fsynced FIRST under runtime.lock.
	// If the journal genesis disappears, this immutable anchor refuses a
	// second "first use". A crash between the two files stays fail-closed.
	if err := writeRecoveryEnrollmentAnchorUnderRuntimeLockV1(runtimeDirectory, raw, step); err != nil {
		return errRecoveryJournalUntrustedV1
	}
	fd, err := syscall.Openat(int(journalLock.Fd()), recoveryEnrollmentGenesisFilenameV1,
		syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL|
			syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0600)
	if err != nil {
		return errRecoveryJournalUntrustedV1
	}
	file := os.NewFile(uintptr(fd), recoveryEnrollmentGenesisFilenameV1)
	defer file.Close()
	if step != nil && step("after_create") != nil {
		return errRecoveryJournalUntrustedV1
	}
	if ctx.Err() != nil {
		return errRecoveryJournalUntrustedV1
	}
	n, err := file.Write(raw)
	if err != nil || n != len(raw) {
		return errRecoveryJournalUntrustedV1
	}
	if step != nil && step("after_write") != nil {
		return errRecoveryJournalUntrustedV1
	}
	if err := file.Sync(); err != nil {
		return errRecoveryJournalUntrustedV1
	}
	if step != nil && step("after_file_fsync") != nil {
		return errRecoveryJournalUntrustedV1
	}
	if err := file.Close(); err != nil {
		return errRecoveryJournalUntrustedV1
	}
	if err := journalLock.Sync(); err != nil {
		return errRecoveryJournalUntrustedV1
	}
	if step != nil && step("after_directory_fsync") != nil {
		return errRecoveryJournalUntrustedV1
	}
	if ctx.Err() != nil {
		return errRecoveryJournalUntrustedV1
	}
	return nil
}

func readRecoveryEnrollmentGenesisUnderJournalLockV1(
	journalLock *os.File,
) (recoveryEnrollmentGenesisV1, error) {
	deny := func() (recoveryEnrollmentGenesisV1, error) {
		return recoveryEnrollmentGenesisV1{}, errRecoveryJournalUntrustedV1
	}
	names, err := journalLock.Readdirnames(-1)
	if err != nil || len(names) != 1 || names[0] != recoveryEnrollmentGenesisFilenameV1 {
		return deny()
	}
	fd, err := syscall.Openat(int(journalLock.Fd()), recoveryEnrollmentGenesisFilenameV1,
		syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return deny()
	}
	file := os.NewFile(uintptr(fd), recoveryEnrollmentGenesisFilenameV1)
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return deny()
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 ||
		st.Uid != uint32(os.Geteuid()) || st.Nlink != 1 ||
		info.Size() <= 0 || info.Size() > recoveryJournalDiskRecordLimitV1 {
		return deny()
	}
	raw, err := io.ReadAll(io.LimitReader(file, recoveryJournalDiskRecordLimitV1+1))
	if err != nil || int64(len(raw)) != info.Size() {
		return deny()
	}
	var got recoveryEnrollmentGenesisV1
	if err := json.Unmarshal(raw, &got); err != nil || !validRecoveryEnrollmentGenesisV1(got) {
		return deny()
	}
	canonical, err := json.Marshal(got)
	if err != nil || !bytes.Equal(raw, canonical) {
		// Reject duplicate/unknown/mixed-case JSON keys and noncanonical order.
		return deny()
	}
	return got, nil
}

// This is a STRICTLY HISTORICAL read; both locks are released before the
// caller receives any observation. The expected Manager tuple can be forged
// by the caller and a matching SHA-256 is not a live host attestation.
func inspectRecoveryEnrollmentGenesisOfflineV1(
	ctx context.Context,
	runtimeDir, journalDir string,
	expected recoveryEnrollmentGenesisV1,
) (recoveryEnrollmentGenesisObservationV1, error) {
	deny := func() (recoveryEnrollmentGenesisObservationV1, error) {
		return recoveryEnrollmentGenesisObservationV1{}, errRecoveryJournalUntrustedV1
	}
	if ctx == nil || ctx.Err() != nil || !validRecoveryEnrollmentGenesisV1(expected) ||
		validateRecoveryEnrollmentDistinctRootsV1(runtimeDir, journalDir) != nil {
		return deny()
	}
	runtimeLock, err := holdExistingRuntimeMutationForGenesisV1(runtimeDir)
	if err != nil {
		return deny()
	}
	defer runtimeLock.Close()
	runtimeDirectory, err := openRecoveryEnrollmentAnchorRootV1(runtimeDir)
	if err != nil {
		return deny()
	}
	defer runtimeDirectory.Close()
	journalLock, err := openRecoveryJournalDiskV1(journalDir)
	if err != nil {
		return deny()
	}
	defer journalLock.Close()
	anchor, err := readRecoveryEnrollmentAnchorUnderRuntimeLockV1(runtimeDirectory)
	if err != nil || anchor != expected {
		return deny()
	}
	got, err := readRecoveryEnrollmentGenesisUnderJournalLockV1(journalLock)
	if err != nil || got != anchor || ctx.Err() != nil {
		return deny()
	}
	digest, err := recoveryEnrollmentGenesisDigestV1(got)
	if err != nil {
		return deny()
	}
	// A successful comparison is NEVER enrollment or authorization.
	return recoveryEnrollmentGenesisObservationV1{
		GenesisSHA256: digest, Unattested: true,
	}, nil
}
