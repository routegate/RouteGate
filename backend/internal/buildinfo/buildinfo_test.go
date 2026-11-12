package buildinfo

import (
	"os"
	"strconv"
	"strings"
	"testing"
)

func TestCurrentUsesDevelopmentDefaults(t *testing.T) {
	info := Current()

	if info.Version != "dev" || info.GitCommit != "unknown" || info.BuildDate != "unknown" {
		t.Fatalf("unexpected build defaults: %+v", info)
	}
	if info.AgentProtocolVersion != 1 || info.MinimumAgentProtocolVersion != 1 {
		t.Fatalf("unexpected protocol defaults: %+v", info)
	}
	if ExpectedDatabaseSchemaVersion != 167 {
		t.Fatalf("schema version = %d, want 167", info.ExpectedDatabaseSchemaVersion)
	}
	if info.AutomaticUpdatesSupported {
		t.Fatal("automatic updates must remain disabled for the MVP")
	}
}

func TestExpectedDatabaseSchemaVersionMatchesLatestMigration(t *testing.T) {
	entries, err := os.ReadDir("../../migrations")
	if err != nil {
		t.Fatalf("read migrations: %v", err)
	}

	latest := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".up.sql") || len(name) < 6 {
			continue
		}
		generation, err := strconv.Atoi(name[:6])
		if err != nil {
			continue
		}
		if generation > latest {
			latest = generation
		}
	}
	if latest == 0 {
		t.Fatal("no numbered up migrations found")
	}
	if ExpectedDatabaseSchemaVersion != latest {
		t.Fatalf("expected database schema version=%d, latest migration=%d", ExpectedDatabaseSchemaVersion, latest)
	}
}
