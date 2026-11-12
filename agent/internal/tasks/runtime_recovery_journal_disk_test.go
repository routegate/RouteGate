package tasks

import (
	"bytes"
	"errors"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func recoveryJournalDiskFixtureV1(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestRuntimeRecoveryJournalDiskV1DurablePrefixStillUnattested(t *testing.T) {
	dir := recoveryJournalDiskFixtureV1(t)
	events := syntheticRecoveryJournalV1(t)
	for i, event := range events {
		if err := appendRecoveryJournalDiskV1(dir, event, nil); err != nil {
			t.Fatalf("record %d was not persisted: %v", i+1, err)
		}
		review, err := reviewRecoveryJournalDiskV1(dir)
		if err != nil || !review.Unattested || review.EventsSeen != i+1 ||
			review.LastPhase != event.Phase || !validRemovalHash(review.HeadSHA256) {
			t.Fatalf("fsynced journal became authoritative or unreadable: %+v %v", review, err)
		}
		raw, err := os.ReadFile(filepath.Join(dir, recoveryJournalDiskNameV1(i+1)))
		if err != nil || len(raw) == 0 {
			t.Fatal("persisted record missing", err)
		}
		if err := appendRecoveryJournalDiskV1(dir, event, nil); !errors.Is(err, errRecoveryJournalUntrustedV1) {
			t.Fatal("duplicate sequence was silently overwritten", err)
		}
	}
	if err := appendRecoveryJournalDiskV1(dir, events[4], nil); !errors.Is(err, errRecoveryJournalUntrustedV1) {
		t.Fatal("terminal phase replay admitted", err)
	}
}

func TestRuntimeRecoveryJournalDiskV1CrashAfterEveryPersistenceBoundary(t *testing.T) {
	events := syntheticRecoveryJournalV1(t)
	for _, stage := range []string{
		"after_create",
		"after_write",
		"after_file_fsync",
		"after_directory_fsync",
	} {
		t.Run(stage, func(t *testing.T) {
			dir := recoveryJournalDiskFixtureV1(t)
			reached := false
			err := appendRecoveryJournalDiskV1(dir, events[0], func(point string) error {
				if point == stage {
					reached = true
					return errors.New("simulated termination")
				}
				return nil
			})
			if !reached || !errors.Is(err, errRecoveryJournalUntrustedV1) {
				t.Fatalf("fault point not exercised or error ignored: %v", err)
			}
			// No automatic deletion/overwrite even if the partial record is
			// parseable. A present complete-looking record stays UNATTESTED.
			path := filepath.Join(dir, recoveryJournalDiskNameV1(1))
			if _, err := os.Lstat(path); err != nil {
				t.Fatalf("fault was silently repaired: %v", err)
			}
			review, err := reviewRecoveryJournalDiskV1(dir)
			if stage == "after_create" {
				if !errors.Is(err, errRecoveryJournalUntrustedV1) ||
					review != (recoveryJournalIntegrityObservationV1{}) {
					t.Fatalf("partial record was incorrectly accepted: %+v %v", review, err)
				}
			} else if err != nil || !review.Unattested || review.EventsSeen != 1 {
				t.Fatalf("ambiguous persistence became trusted or unreadable: %+v %v", review, err)
			}
			if err := appendRecoveryJournalDiskV1(dir, events[0], nil); !errors.Is(err, errRecoveryJournalUntrustedV1) {
				t.Fatal("uncertain write was automatically retried", err)
			}
		})
	}
}

func TestRuntimeRecoveryJournalDiskV1CorruptionAndLinksFailClosed(t *testing.T) {
	first := syntheticRecoveryJournalV1(t)[0]
	for _, mode := range []string{
		"empty", "partial", "duplicate_key", "world_readable", "hardlink",
		"symlink", "unknown_entry", "gap", "altered_chain",
	} {
		t.Run(mode, func(t *testing.T) {
			dir := recoveryJournalDiskFixtureV1(t)
			if mode != "empty" && mode != "partial" && mode != "duplicate_key" &&
				mode != "symlink" && mode != "gap" {
				if err := appendRecoveryJournalDiskV1(dir, first, nil); err != nil {
					t.Fatal(err)
				}
			}
			path := filepath.Join(dir, recoveryJournalDiskNameV1(1))
			switch mode {
			case "empty":
				if err := os.WriteFile(path, nil, 0600); err != nil { t.Fatal(err) }
			case "partial":
				if err := os.WriteFile(path, []byte("{"), 0600); err != nil { t.Fatal(err) }
			case "duplicate_key":
				// A valid-looking JSON payload with repeated schema keys
				// is not an unambiguous journal transcript.
				original := syntheticRecoveryJournalV1(t)[0]
				raw, err := json.Marshal(original)
				if err != nil { t.Fatal(err) }
				raw = bytes.Replace(raw, []byte("\"phase\":"), []byte("\"phase\":\"bad\",\"phase\":"), 1)
				if err := os.WriteFile(path, raw, 0600); err != nil { t.Fatal(err) }
			case "world_readable":
				if err := os.Chmod(path, 0644); err != nil { t.Fatal(err) }
			case "hardlink":
				if err := os.Link(path, filepath.Join(t.TempDir(), "alias")); err != nil { t.Fatal(err) }
			case "symlink":
				if err := os.Symlink(filepath.Join(t.TempDir(), "missing"), path); err != nil { t.Fatal(err) }
			case "unknown_entry":
				if err := os.WriteFile(filepath.Join(dir, "unexpected"), []byte("x"), 0600); err != nil { t.Fatal(err) }
			case "gap":
				if err := os.WriteFile(filepath.Join(dir, recoveryJournalDiskNameV1(2)), []byte("{}"), 0600); err != nil { t.Fatal(err) }
			case "altered_chain":
				raw, err := os.ReadFile(path)
				if err != nil { t.Fatal(err) }
				raw = bytes.Replace(raw, []byte(first.MarkerSHA256), []byte("dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"), 1)
				if err := os.WriteFile(path, raw, 0600); err != nil { t.Fatal(err) }
			}
			review, err := reviewRecoveryJournalDiskV1(dir)
			if !errors.Is(err, errRecoveryJournalUntrustedV1) ||
				review != (recoveryJournalIntegrityObservationV1{}) {
				t.Fatalf("bad journal content was admitted: %+v %v", review, err)
			}
			if err := appendRecoveryJournalDiskV1(dir, first, nil); !errors.Is(err, errRecoveryJournalUntrustedV1) {
				t.Fatal("corrupted journal accepted another event", err)
			}
		})
	}
}

func TestRuntimeRecoveryJournalDiskV1MissingOrUnsafeDirectory(t *testing.T) {
	missing := filepath.Join(recoveryJournalDiskFixtureV1(t), "missing")
	first := syntheticRecoveryJournalV1(t)[0]
	if _, err := reviewRecoveryJournalDiskV1(missing); !errors.Is(err, errRecoveryJournalUntrustedV1) {
		t.Fatal("missing journal treated as clean", err)
	}
	if err := appendRecoveryJournalDiskV1(missing, first, nil); !errors.Is(err, errRecoveryJournalUntrustedV1) {
		t.Fatal("journal created an unprovisioned directory", err)
	}
	if _, err := os.Lstat(missing); !os.IsNotExist(err) {
		t.Fatal("private prototype implicitly provisioned journal state", err)
	}
	for _, mode := range []string{"relative", "symlink_directory", "public_directory"} {
		t.Run(mode, func(t *testing.T) {
			dir := recoveryJournalDiskFixtureV1(t)
			switch mode {
			case "relative":
				dir = "relative"
			case "symlink_directory":
				target := dir
				dir = filepath.Join(t.TempDir(), "link")
				if err := os.Symlink(target, dir); err != nil { t.Fatal(err) }
			case "public_directory":
				if err := os.Chmod(dir, 0755); err != nil { t.Fatal(err) }
			}
			if err := appendRecoveryJournalDiskV1(dir, first, nil); !errors.Is(err, errRecoveryJournalUntrustedV1) {
				t.Fatal("unsafe directory was writable", err)
			}
		})
	}
}

func TestRuntimeRecoveryJournalDiskV1ConcurrentAppendOneWinner(t *testing.T) {
	dir := recoveryJournalDiskFixtureV1(t)
	first := syntheticRecoveryJournalV1(t)[0]
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results <- appendRecoveryJournalDiskV1(dir, first, nil)
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	success, refused := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, errRecoveryJournalUntrustedV1) {
			refused++
		} else {
			t.Fatalf("unexpected concurrent write error: %v", err)
		}
	}
	if success != 1 || refused != 1 {
		t.Fatalf("concurrent writers created duplicate entries: success=%d refused=%d", success, refused)
	}
	review, err := reviewRecoveryJournalDiskV1(dir)
	if err != nil || !review.Unattested || review.EventsSeen != 1 {
		t.Fatalf("concurrent append damaged storage: %+v %v", review, err)
	}
}

func TestRuntimeRecoveryJournalDiskV1TailLossUndetectableWithoutAnchor(t *testing.T) {
	dir := recoveryJournalDiskFixtureV1(t)
	events := syntheticRecoveryJournalV1(t)
	for _, event := range events {
		if err := appendRecoveryJournalDiskV1(dir, event, nil); err != nil {
			t.Fatal(err)
		}
	}
	// A shortened but internally valid prefix cannot prove whether a crash
	// occurred or somebody deleted the tail. The reader MUST say Unattested.
	if err := os.Remove(filepath.Join(dir, recoveryJournalDiskNameV1(5))); err != nil {
		t.Fatal(err)
	}
	review, err := reviewRecoveryJournalDiskV1(dir)
	if err != nil || !review.Unattested || review.EventsSeen != 4 {
		t.Fatalf("valid truncation was mistaken for authorization: %+v %v", review, err)
	}
}
