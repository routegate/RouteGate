package tasks

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Bind the observed service process to the same binary that validates candidates
// and to exactly one runtime JSON source. Multiple merged configs, stdin,
// relative paths, symlinks and unknown command options fail closed.
func verifyRemovalProcessSource(pid int, binary, activePath string) error {
	expected, err := exec.LookPath(binary)
	if err != nil {
		return ErrCredentialRemoval
	}
	expectedInfo, err := os.Stat(expected)
	if err != nil {
		return ErrCredentialRemoval
	}
	proc := filepath.Join("/proc", strconv.Itoa(pid))
	actualInfo, err := os.Stat(filepath.Join(proc, "exe"))
	if err != nil || !os.SameFile(expectedInfo, actualInfo) {
		return ErrCredentialRemoval
	}
	command, err := os.ReadFile(filepath.Join(proc, "cmdline"))
	if err != nil || len(command) == 0 || len(command) > 65536 || command[len(command)-1] != 0 {
		return ErrCredentialRemoval
	}
	parts := bytes.Split(command[:len(command)-1], []byte{0})
	args := make([]string, len(parts))
	for i, part := range parts {
		args[i] = string(part)
	}
	return verifyRemovalConfigArgs(args, activePath)
}

func verifyRemovalConfigArgs(args []string, activePath string) error {
	resolved, err := filepath.EvalSymlinks(activePath)
	if err != nil || !filepath.IsAbs(activePath) || resolved != filepath.Clean(activePath) {
		return ErrCredentialRemoval
	}
	var sources []string
	run := false
	for i := 1; i < len(args); i++ {
		option, value, inline := strings.Cut(args[i], "=")
		switch option {
		case "run":
			if run || inline {
				return ErrCredentialRemoval
			}
			run = true
		case "--disable-color":
			if inline && value != "true" && value != "false" {
				return ErrCredentialRemoval
			}
		case "-c", "--config", "-C", "--config-directory", "-D", "--directory":
			if !inline {
				i++
				if i >= len(args) {
					return ErrCredentialRemoval
				}
				value = args[i]
			}
			if !filepath.IsAbs(value) {
				return ErrCredentialRemoval
			}
			resolvedValue, err := filepath.EvalSymlinks(value)
			if err != nil || resolvedValue != filepath.Clean(value) {
				return ErrCredentialRemoval
			}
			switch option {
			case "-c", "--config":
				sources = append(sources, resolvedValue)
			case "-C", "--config-directory":
				entries, err := os.ReadDir(resolvedValue)
				if err != nil {
					return ErrCredentialRemoval
				}
				for _, entry := range entries {
					if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
						continue
					}
					if entry.Type()&os.ModeSymlink != 0 {
						return ErrCredentialRemoval
					}
					sources = append(sources, filepath.Join(resolvedValue, entry.Name()))
				}
			}
		default:
			return ErrCredentialRemoval
		}
	}
	if !run || len(sources) != 1 || sources[0] != resolved {
		return ErrCredentialRemoval
	}
	return nil
}
