package tasks

import (
	"os"
	"path/filepath"
	"strings"
)

// This OFFLINE genesis consists of two distinct local files held under two
// different directory flocks. A nested root or an ancestor-symlink alias
// defeats the claimed separation of those roots. Reject such path topology
// before creating either write-once record.
//
// This is NOT a trusted-host check: a privileged attacker may still remove
// both independent roots, replace a mounted filesystem or roll back a VM.
func validateRecoveryEnrollmentDistinctRootsV1(runtimeDir, journalDir string) error {
	for _, root := range []string{runtimeDir, journalDir} {
		if !filepath.IsAbs(root) || filepath.Clean(root) != root {
			return errRecoveryJournalUntrustedV1
		}
		resolved, err := filepath.EvalSymlinks(root)
		if err != nil || resolved != root {
			return errRecoveryJournalUntrustedV1
		}
	}
	for _, pair := range [][2]string{
		{runtimeDir, journalDir},
		{journalDir, runtimeDir},
	} {
		rel, err := filepath.Rel(pair[0], pair[1])
		if err != nil {
			return errRecoveryJournalUntrustedV1
		}
		// "." means identical paths; a relative child path does not
		// start with ".." as an entire path segment.
		if rel == "." || (rel != ".." &&
			!strings.HasPrefix(rel, ".."+string(os.PathSeparator))) {
			return errRecoveryJournalUntrustedV1
		}
	}
	return nil
}
