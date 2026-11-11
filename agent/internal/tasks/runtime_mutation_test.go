package tasks

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
)

func TestRuntimeMutationAdmission(t *testing.T) {
	for _, kind := range []string{TaskKindConfigApply, TaskKindVPNCoreService, TaskKindVPNCoreInstall, TaskKindPlatformUpdate, TaskKindMaintenance} {
		t.Run(kind, func(t *testing.T) {
			e, task, adapter, before := removalExecutorFixture(t)
			lease, err := BeginRuntimeMutation(e.receiptDir, task.ID, kind)
			if err != nil {
				t.Fatal(err)
			}
			defer lease.Close()
			if other, err := BeginRuntimeMutation(e.receiptDir, task.ID, kind); err == nil {
				other.Close()
				t.Fatal("overlapping lease")
			}
			if _, err := e.Execute(context.Background(), task); err == nil {
				t.Fatal("removal overlapped live mutation")
			}
			if adapter.restarts != 0 || adapter.validations != 0 {
				t.Fatal("runtime touched")
			}
			after, _ := os.ReadFile(e.activePath)
			if string(after) != string(before) {
				t.Fatal("config changed")
			}
			if err := lease.Complete(); err != nil {
				t.Fatal(err)
			}
			if err := lease.Complete(); err != nil {
				t.Fatal("complete retry", err)
			}
			if _, err := e.Execute(context.Background(), task); err == nil {
				t.Fatal("complete released live lock")
			}
			if err := lease.Close(); err != nil {
				t.Fatal(err)
			}
			if err := lease.Complete(); err == nil {
				t.Fatal("closed lease authorized completion")
			}
			if _, err := e.Execute(context.Background(), task); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRuntimeMutationBlocksRemovalReplay(t *testing.T) {
	e, task, a, _ := removalExecutorFixture(t)
	if _, err := e.Execute(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	lease, err := BeginRuntimeMutation(e.receiptDir, task.ID, TaskKindConfigApply)
	if err != nil {
		t.Fatal(err)
	}
	lease.Close() // Unknown outcome is not a safe completion.
	r, err := e.Execute(context.Background(), task)
	if err == nil || r.State != "recovery_required" || a.restarts != 1 {
		t.Fatal(r, err)
	}
	if other, err := BeginRuntimeMutation(e.receiptDir, task.ID, TaskKindConfigApply); err == nil {
		other.Close()
		t.Fatal("same task retried uncertain mutation")
	}
}

func TestRuntimeMutationBlockedByRemoval(t *testing.T) {
	e, task, a, _ := removalExecutorFixture(t)
	a.validationHook = func() error {
		if lease, err := BeginRuntimeMutation(e.receiptDir, task.ID, TaskKindConfigApply); err == nil {
			lease.Close()
			t.Error("legacy mutation overlapped removal")
		}
		return nil
	}
	a.restartHook = func() error { return errors.New("uncertain restart") }
	if _, err := e.Execute(context.Background(), task); err == nil {
		t.Fatal("expected failure")
	}
	if lease, err := BeginRuntimeMutation(e.receiptDir, task.ID, TaskKindConfigApply); err == nil {
		lease.Close()
		t.Fatal("ignored durable removal fence")
	}
}

func TestRuntimeMutationUnsafeState(t *testing.T) {
	for _, name := range []string{"empty marker", "symlink marker", "directory marker", "changed marker", "fifo lock", "hardlink lock", "public directory", "relative path", "unknown kind", "invalid id"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.Chmod(dir, 0700); err != nil {
				t.Fatal(err)
			}
			id, kind := removalAccount, TaskKindConfigApply
			path := filepath.Join(dir, "mutation-inflight.json")
			var err error
			switch name {
			case "empty marker":
				err = os.WriteFile(path, nil, 0600)
			case "symlink marker":
				err = os.Symlink(filepath.Join(dir, "absent"), path)
			case "directory marker":
				err = os.Mkdir(path, 0700)
			case "changed marker":
				lease, beginErr := BeginRuntimeMutation(dir, id, kind)
				if beginErr != nil {
					t.Fatal(beginErr)
				}
				defer lease.Close()
				if err := os.WriteFile(path, []byte("changed"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := lease.Complete(); err == nil {
					t.Fatal("cleared changed marker")
				}
				return
			case "fifo lock":
				err = syscall.Mkfifo(filepath.Join(dir, "runtime.lock"), 0600)
			case "hardlink lock":
				err = os.WriteFile(filepath.Join(dir, "alias"), nil, 0600)
				if err == nil {
					err = os.Link(filepath.Join(dir, "alias"), filepath.Join(dir, "runtime.lock"))
				}
			case "public directory":
				err = os.Chmod(dir, 0755)
			case "relative path":
				dir = "relative"
			case "unknown kind":
				kind = "unknown"
			case "invalid id":
				id = "invalid"
			}
			if err != nil {
				t.Fatal(err)
			}
			lease, err := BeginRuntimeMutation(dir, id, kind)
			if !errors.Is(err, ErrRuntimeMutationBlocked) {
				if lease != nil {
					lease.Close()
				}
				t.Fatal("unsafe admission", err)
			}
		})
	}
}

// A separate process exits without Close; the kernel releases flock but the
// fsynced marker must still prevent both kinds of mutation after restart.
func TestRuntimeMutationProcessExit(t *testing.T) {
	if dir := os.Getenv("RG_TEST_MUTATION_CHILD_DIR"); dir != "" {
		if _, err := BeginRuntimeMutation(dir, removalAccount, TaskKindConfigApply); err != nil {
			os.Exit(2)
		}
		os.Exit(0)
	}
	e, task, a, _ := removalExecutorFixture(t)
	cmd := exec.Command(os.Args[0], "-test.run=^TestRuntimeMutationProcessExit$")
	cmd.Env = append(os.Environ(), "RG_TEST_MUTATION_CHILD_DIR="+e.receiptDir)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("child: %v %s", err, output)
	}
	if lease, err := BeginRuntimeMutation(e.receiptDir, task.ID, TaskKindConfigApply); err == nil {
		lease.Close()
		t.Fatal("crash fence lost")
	}
	r, err := e.Execute(context.Background(), task)
	if err == nil || r.State != "recovery_required" || a.restarts != 0 {
		t.Fatal(r, err)
	}
}
