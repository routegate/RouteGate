package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRuntimeMutationFencingConfig(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		text := "manager_url: http://127.0.0.1:8080\n"
		if enabled {
			text += "experimental_runtime_mutation_fencing: true\n"
		}
		cfg, err := Load(writeTestConfig(t, text))
		if err != nil || cfg.ExperimentalRuntimeMutationFencing != enabled {
			t.Fatal(cfg, err)
		}
		path := filepath.Join(t.TempDir(), "agent.yaml")
		if err := cfg.Save(path); err != nil {
			t.Fatal(err)
		}
		again, err := Load(path)
		if err != nil || again.ExperimentalRuntimeMutationFencing != enabled {
			t.Fatal("lost registration setting", err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !enabled && strings.Contains(string(data), "experimental_runtime_mutation_fencing") {
			t.Fatal("default configs changed")
		}
	}
	for _, value := range []string{"maybe", "", "secret-value"} {
		_, err := Load(writeTestConfig(t, "manager_url: http://localhost\nexperimental_runtime_mutation_fencing: "+value+"\n"))
		if err == nil || strings.Contains(err.Error(), "secret-value") {
			t.Fatal("invalid value accepted or leaked")
		}
	}
}
