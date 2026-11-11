package tasks

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"syscall"
)

// ErrRuntimeMutationBlocked never includes paths, credentials or receipt data.
var ErrRuntimeMutationBlocked = errors.New("runtime mutation blocked: exclusive state unavailable or recovery required")

// RuntimeMutation is an admission lease, not proof of a successful operation.
// Close releases the process lock but deliberately retains the durable marker.
// Only a live caller that has verified a safe terminal runtime state may call
// Complete. There is deliberately no force-unlock or crash-recovery API here.
//
// This primitive is not yet wired to legacy handlers or detached workers. All
// participating processes MUST use the same private, stable directory as the
// CredentialRemovalExecutor, outside config staging/backup cleanup roots.
type RuntimeMutation struct {
	mu        sync.Mutex
	lock      *os.File
	path      string
	marker    []byte
	completed bool
}

// BeginRuntimeMutation serializes a legacy mutation with credential removal and
// persists its intent before the caller may alter a runtime. A repeated task is
// NOT permission to retry a mutation after a crash. Even malformed/empty markers
// block admission and require explicit reconciliation.
func BeginRuntimeMutation(dir, taskID, kind string) (*RuntimeMutation, error) {
	if !canonicalTaskIDPattern.MatchString(taskID) {
		return nil, ErrRuntimeMutationBlocked
	}
	switch kind {
	case TaskKindConfigApply, TaskKindVPNCoreService, TaskKindVPNCoreInstall, TaskKindPlatformUpdate, TaskKindMaintenance:
	default:
		return nil, ErrRuntimeMutationBlocked
	}
	lock, err := lockRuntimeMutation(dir)
	if err != nil {
		return nil, ErrRuntimeMutationBlocked
	}
	lease := &RuntimeMutation{lock: lock, path: filepath.Join(dir, "mutation-inflight.json")}
	for _, path := range []string{lease.path, filepath.Join(dir, "inflight.json")} {
		if !runtimeMarkerAbsent(path) {
			_ = lease.Close()
			return nil, ErrRuntimeMutationBlocked
		}
	}
	lease.marker, _ = json.Marshal(struct {
		SchemaVersion int    `json:"schemaVersion"`
		TaskID        string `json:"taskId"`
		Kind          string `json:"kind"`
	}{1, taskID, kind})
	if writeRemovalFile(lease.path, lease.marker) != nil {
		_ = lease.Close()
		return nil, ErrRuntimeMutationBlocked
	}
	return lease, nil
}

func runtimeMarkerAbsent(path string) bool {
	_, err := os.Lstat(path)
	return os.IsNotExist(err)
}

// Complete clears only this live lease's unchanged marker, with directory
// fsync. It does not release the lock; defer Close immediately after Begin.
// Never call this on an ambiguous result, timeout, or dispatch acknowledgement.
// In particular a detached worker must hold its own lease through mutation.
func (m *RuntimeMutation) Complete() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.lock == nil {
		return ErrRuntimeMutationBlocked
	}
	if m.completed {
		return nil
	}
	marker, err := readRemovalFile(m.path, 16384, true)
	if err != nil || string(marker) != string(m.marker) || !runtimeMarkerAbsent(filepath.Join(filepath.Dir(m.path), "inflight.json")) {
		return ErrRuntimeMutationBlocked
	}
	if removeRemovalFile(m.path) != nil {
		return ErrRuntimeMutationBlocked
	}
	m.completed = true
	return nil
}

func (m *RuntimeMutation) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.lock == nil {
		return nil
	}
	err := m.lock.Close() // flock is released by closing the owning descriptor.
	m.lock = nil
	if err != nil {
		return ErrRuntimeMutationBlocked
	}
	return nil
}

func lockRuntimeMutation(dir string) (*os.File, error) {
	if !filepath.IsAbs(dir) {
		return nil, ErrRuntimeMutationBlocked
	}
	// The stable parent must be provisioned by the caller. Creating only this
	// directory lets us durably persist its entry before writing an intent.
	if err := os.Mkdir(dir, 0700); err != nil && !os.IsExist(err) {
		return nil, err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || info.Mode().Perm() != 0700 || stat.Uid != uint32(os.Geteuid()) {
		return nil, ErrRuntimeMutationBlocked
	}
	parent, err := os.Open(filepath.Dir(dir))
	if err != nil {
		return nil, err
	}
	err = parent.Sync()
	_ = parent.Close()
	if err != nil {
		return nil, err
	}
	fd, err := syscall.Open(filepath.Join(dir, "runtime.lock"), syscall.O_RDWR|syscall.O_CREAT|syscall.O_NONBLOCK|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0600)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "runtime.lock")
	info, err = file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	stat, ok = info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 {
		_ = file.Close()
		return nil, ErrRuntimeMutationBlocked
	}
	if err = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}
