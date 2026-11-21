package tasks

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

const recoveryJournalDiskRecordLimitV1 = 4096

// All storage functions in this file are PRIVATE, OFFLINE test primitives.
// They are NOT called by Agent Runner, mutation admission, Manager, or any
// recovery/unlock operation. A local journal is never trusted attestation.
//
// Each entry has an immutable name in a DEDICATED, pre-existing 0700
// directory. O_EXCL prevents overwrites; the directory flock serializes
// cooperating reviewers and writers. A crash during a direct write may
// leave a partial entry: the reader then refuses it instead of repairing,
// truncating, skipping, deleting or authorizing an Agent mutation.
func openRecoveryJournalDiskV1(dir string) (*os.File, error) {
	if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir {
		return nil, errRecoveryJournalUntrustedV1
	}
	fd, err := syscall.Open(dir, syscall.O_RDONLY|syscall.O_DIRECTORY|
		syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, errRecoveryJournalUntrustedV1
	}
	file := os.NewFile(uintptr(fd), dir)
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, errRecoveryJournalUntrustedV1
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || info.Mode().Perm() != 0700 ||
		st.Uid != uint32(os.Geteuid()) {
		_ = file.Close()
		return nil, errRecoveryJournalUntrustedV1
	}
	// LOCK_NB avoids waiting for a stuck writer. The lock is intentionally
	// scoped to this dedicated directory, not a new runtime-mutation lease.
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, errRecoveryJournalUntrustedV1
	}
	return file, nil
}

func recoveryJournalDiskNameV1(sequence int) string {
	return fmt.Sprintf("event-%06d.json", sequence)
}

// readRecoveryJournalDiskEntriesV1 requires an exact contiguous prefix.
// Unknown filenames, gaps, extra entries, links and malformed JSON are
// terminal review failures, never an invitation to salvage a later event.
func readRecoveryJournalDiskEntriesV1(dir *os.File) ([]recoveryJournalEventV1, error) {
	names, err := dir.Readdirnames(-1)
	if err != nil || len(names) > 5 {
		return nil, errRecoveryJournalUntrustedV1
	}
	seen := make(map[string]bool, len(names))
	for _, name := range names {
		if seen[name] {
			return nil, errRecoveryJournalUntrustedV1
		}
		seen[name] = true
	}
	events := make([]recoveryJournalEventV1, 0, len(names))
	for i := 1; i <= len(names); i++ {
		name := recoveryJournalDiskNameV1(i)
		if !seen[name] {
			return nil, errRecoveryJournalUntrustedV1
		}
		entry, err := readRecoveryJournalDiskEntryV1(dir, name)
		if err != nil {
			return nil, errRecoveryJournalUntrustedV1
		}
		events = append(events, entry)
	}
	if len(events) != 0 {
		if _, err := reviewRecoveryJournalChainV1(events); err != nil {
			return nil, errRecoveryJournalUntrustedV1
		}
	}
	return events, nil
}

func readRecoveryJournalDiskEntryV1(dir *os.File, name string) (recoveryJournalEventV1, error) {
	deny := func() (recoveryJournalEventV1, error) {
		return recoveryJournalEventV1{}, errRecoveryJournalUntrustedV1
	}
	fd, err := syscall.Openat(int(dir.Fd()), name,
		syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return deny()
	}
	file := os.NewFile(uintptr(fd), name)
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
	var event recoveryJournalEventV1
	if err := json.Unmarshal(raw, &event); err != nil {
		return deny()
	}
	canonical, err := json.Marshal(event)
	// Byte-exact encoding refuses unknown, duplicate and case-alias JSON
	// keys as well as whitespace/order ambiguity, before chain validation.
	if err != nil || !bytes.Equal(raw, canonical) {
		return deny()
	}
	return event, nil
}

// reviewRecoveryJournalDiskV1 is only a syntactic read. Even a fully
// fsynced, valid chain ALWAYS returns Unattested and can never clear a fence.
func reviewRecoveryJournalDiskV1(dir string) (recoveryJournalIntegrityObservationV1, error) {
	fd, err := openRecoveryJournalDiskV1(dir)
	if err != nil {
		return recoveryJournalIntegrityObservationV1{}, errRecoveryJournalUntrustedV1
	}
	defer fd.Close()
	entries, err := readRecoveryJournalDiskEntriesV1(fd)
	if err != nil {
		return recoveryJournalIntegrityObservationV1{}, errRecoveryJournalUntrustedV1
	}
	return reviewRecoveryJournalChainV1(entries)
}

// appendRecoveryJournalDiskV1 is a deliberately unregistered prototype.
// The step hook is used ONLY by fault-injection tests to simulate an
// interruption after each persistence boundary. It is not a production
// crash simulator or proof of power-loss safety on real storage.
//
// An ambiguous write can leave a present final file; the caller must
// NOT interpret either an error or a syntactically valid file as approval.
// No automatic retry, cleanup, repair, or release code exists here.
func appendRecoveryJournalDiskV1(dir string, event recoveryJournalEventV1, step func(string) error) error {
	directory, err := openRecoveryJournalDiskV1(dir)
	if err != nil {
		return errRecoveryJournalUntrustedV1
	}
	defer directory.Close()
	previous, err := readRecoveryJournalDiskEntriesV1(directory)
	if err != nil || len(previous) >= 5 {
		return errRecoveryJournalUntrustedV1
	}
	candidate := append(append([]recoveryJournalEventV1(nil), previous...), event)
	if _, err := reviewRecoveryJournalChainV1(candidate); err != nil {
		return errRecoveryJournalUntrustedV1
	}
	raw, err := json.Marshal(event)
	if err != nil || len(raw) > recoveryJournalDiskRecordLimitV1 {
		return errRecoveryJournalUntrustedV1
	}

	fd, err := syscall.Openat(int(directory.Fd()), recoveryJournalDiskNameV1(len(candidate)),
		syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL|
			syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0600)
	if err != nil {
		return errRecoveryJournalUntrustedV1
	}
	file := os.NewFile(uintptr(fd), recoveryJournalDiskNameV1(len(candidate)))
	defer file.Close()
	if step != nil {
		if err := step("after_create"); err != nil {
			return errRecoveryJournalUntrustedV1
		}
	}
	n, err := file.Write(raw)
	if err != nil || n != len(raw) {
		return errRecoveryJournalUntrustedV1
	}
	if step != nil {
		if err := step("after_write"); err != nil {
			return errRecoveryJournalUntrustedV1
		}
	}
	if err := file.Sync(); err != nil {
		return errRecoveryJournalUntrustedV1
	}
	if step != nil {
		if err := step("after_file_fsync"); err != nil {
			return errRecoveryJournalUntrustedV1
		}
	}
	if err := file.Close(); err != nil {
		return errRecoveryJournalUntrustedV1
	}
	if err := directory.Sync(); err != nil {
		return errRecoveryJournalUntrustedV1
	}
	if step != nil {
		if err := step("after_directory_fsync"); err != nil {
			return errRecoveryJournalUntrustedV1
		}
	}
	return nil
}
