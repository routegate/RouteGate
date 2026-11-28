package tasks

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestRetainedRecoveryBlocksBothMutationPaths(t *testing.T) {
	for _, name := range []string{"recovery-consumption.json", "recovery-submission.json", "recovery-acknowledgement.json", "recovery-future.json", "enrollment-continuity-anchor-v1.json", "event-000001.json"} {
		for _, kind := range []string{"file", "directory", "symlink"} {
			t.Run(name+"/"+kind, func(t *testing.T) {
				e, task, adapter, baseline := removalExecutorFixture(t)
				if err := os.MkdirAll(e.receiptDir, 0700); err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(e.receiptDir, name)
				var err error
				switch kind {
				case "file":
					err = os.WriteFile(path, []byte("corrupt"), 0600)
				case "directory":
					err = os.Mkdir(path, 0700)
				case "symlink":
					err = os.Symlink("missing", path)
				}
				if err != nil {
					t.Fatal(err)
				}
				if lease, err := BeginRuntimeMutation(e.receiptDir, task.ID, TaskKindConfigApply); err == nil {
					lease.Close()
					t.Fatal("legacy mutation admitted")
				}
				report, err := e.Execute(context.Background(), task)
				if err == nil || report.Code != "runtime_recovery_required" || adapter.restarts != 0 {
					t.Fatal("removal admitted", err, report.Code)
				}
				active, err := os.ReadFile(e.activePath)
				if err != nil || !bytes.Equal(active, baseline) {
					t.Fatal("active config changed")
				}
				if _, err := os.Lstat(path); err != nil {
					t.Fatal("retained state changed", err)
				}
			})
		}
	}
}
