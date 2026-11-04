package app

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// The service user may not be able to traverse the private conversion directory.
// Inspect the directory itself before interpreting a missing child as evidence.
func checkStartupLatch(directory string) error {
	info, err := os.Lstat(directory)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect credential conversion directory: %w", err)
	}
	if !info.IsDir() {
		return errors.New("invalid credential conversion directory")
	}
	_, err = os.Lstat(filepath.Join(directory, "conversion.pending"))
	if err == nil {
		return errors.New("credential conversion pending")
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("inspect credential conversion marker: %w", err)
	}
	// A crash can leave just the directory. A pin-only legacy layout can
	// pass this startup check; rollback verifies the pin contents separately.
	pin, err := os.Lstat(filepath.Join(directory, "restore.pin"))
	if err != nil {
		return fmt.Errorf("inspect legacy credential restore pin: %w", err)
	}
	if !pin.Mode().IsRegular() {
		return errors.New("invalid credential restore pin")
	}
	return nil
}
