package tasks

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRuntimeMutationStatePresenceIsReadOnlyAndFailClosed(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "state")
	if RuntimeMutationStatePresent(path) {
		t.Fatal("absent state enables gate")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("inspection created state", err)
	}
	if err := os.Symlink(filepath.Join(root, "missing"), path); err != nil {
		t.Fatal(err)
	}
	if !RuntimeMutationStatePresent(path) {
		t.Fatal("dangling state bypasses gate")
	}
}
