package tasks

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func recoveryEnrollmentAliasRootV1(t *testing.T, actualParent string) string {
	t.Helper()
	alias := filepath.Join(t.TempDir(), "ancestor-alias")
	if err := os.Symlink(actualParent, alias); err != nil {
		t.Fatal(err)
	}
	return alias
}

// An enrollment anchor placed in a nested or symlink-aliased journal is not
// independent of the runtime root, even when both file contents are canonical.
func TestRuntimeRecoveryEnrollmentGenesisOfflineV1RejectsOverlappingAndAliasedWrites(t *testing.T) {
	for _, mode := range []string{
		"same_directory",
		"journal_inside_runtime",
		"symlink_ancestor_runtime",
		"symlink_ancestor_journal",
	} {
		t.Run(mode, func(t *testing.T) {
			runtimeDir, journalDir := recoveryEnrollmentOfflineDirectoriesV1(t)
			switch mode {
			case "same_directory":
				journalDir = runtimeDir
			case "journal_inside_runtime":
				journalDir = filepath.Join(runtimeDir, "nested-journal")
				if err := os.Mkdir(journalDir, 0700); err != nil {
					t.Fatal(err)
				}
			case "symlink_ancestor_runtime":
				runtimeDir = filepath.Join(
					recoveryEnrollmentAliasRootV1(t, filepath.Dir(runtimeDir)),
					filepath.Base(runtimeDir),
				)
			case "symlink_ancestor_journal":
				journalDir = filepath.Join(
					recoveryEnrollmentAliasRootV1(t, filepath.Dir(journalDir)),
					filepath.Base(journalDir),
				)
			}
			if err := validateRecoveryEnrollmentDistinctRootsV1(runtimeDir, journalDir); err == nil {
				t.Fatal("overlapping or symlink-aliased roots passed topology validation")
			}
			requireRecoveryEnrollmentGenesisRefusedV1(t,
				writeRecoveryEnrollmentGenesisOfflineV1(
					context.Background(), runtimeDir, journalDir,
					recoveryEnrollmentGenesisFixtureV1(), nil,
				),
			)
			if _, err := os.Lstat(filepath.Join(runtimeDir, recoveryEnrollmentAnchorFilenameV1)); !os.IsNotExist(err) {
				t.Fatalf("refused enrollment still wrote an anchor: %v", err)
			}
			if _, err := os.Lstat(filepath.Join(journalDir, recoveryEnrollmentGenesisFilenameV1)); !os.IsNotExist(err) {
				t.Fatalf("refused enrollment still wrote a journal genesis: %v", err)
			}
		})
	}
}

// Verify the check is applied to historical reads, not just initial writes.
// These two cases would have produced a valid-looking Unattested observation
// before path topology was checked.
func TestRuntimeRecoveryEnrollmentGenesisOfflineV1RejectsAliasedHistoricalReads(t *testing.T) {
	for _, mode := range []string{
		"journal_moved_inside_runtime",
		"symlink_ancestor_runtime",
		"symlink_ancestor_journal",
	} {
		t.Run(mode, func(t *testing.T) {
			runtimeDir, journalDir := recoveryEnrollmentOfflineDirectoriesV1(t)
			genesis := recoveryEnrollmentGenesisFixtureV1()
			if err := writeRecoveryEnrollmentGenesisOfflineV1(
				context.Background(), runtimeDir, journalDir, genesis, nil,
			); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "journal_moved_inside_runtime":
				nested := filepath.Join(runtimeDir, "nested-journal")
				if err := os.Rename(journalDir, nested); err != nil {
					t.Fatal(err)
				}
				journalDir = nested
			case "symlink_ancestor_runtime":
				runtimeDir = filepath.Join(
					recoveryEnrollmentAliasRootV1(t, filepath.Dir(runtimeDir)),
					filepath.Base(runtimeDir),
				)
			case "symlink_ancestor_journal":
				journalDir = filepath.Join(
					recoveryEnrollmentAliasRootV1(t, filepath.Dir(journalDir)),
					filepath.Base(journalDir),
				)
			}
			result, err := inspectRecoveryEnrollmentGenesisOfflineV1(
				context.Background(), runtimeDir, journalDir, genesis,
			)
			requireRecoveryEnrollmentGenesisRefusedV1(t, err)
			if result != (recoveryEnrollmentGenesisObservationV1{}) {
				t.Fatalf("path-aliased historical files were observed: %+v", result)
			}
		})
	}
}
