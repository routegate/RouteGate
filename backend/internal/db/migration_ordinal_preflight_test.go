package db

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// This guard protects upgrades from existing schema 160 (traffic checkpoints)
// when integrating old RG-140 preparations that ALSO used ordinal 160.
// Rejection must happen before even CREATE TABLE schema_migrations.
func TestMigrationOrdinalCollisionPreventsAllDatabaseWork(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{
		"000160_traffic_report_checkpoints.up.sql",
		"000160_credential_revocation_preparations.up.sql",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("SELECT 1;"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	// nil pool is intentional: a collision must fail before a DB call or panic.
	err := Migrate(context.Background(), nil, dir, nil)
	if err == nil || !strings.Contains(err.Error(), "ambiguous migration ordinal 160") ||
		!strings.Contains(err.Error(), "traffic_report_checkpoints") ||
		!strings.Contains(err.Error(), "credential_revocation_preparations") {
		t.Fatalf("missing safe pre-SQL collision refusal: %v", err)
	}
}

func TestMigrationOrdinalCollisionRejectsInconsistentPadding(t *testing.T) {
	files := []string{"000160_traffic_report_checkpoints.up.sql", "160_other.up.sql"}
	if err := validateMigrationOrdinals(files); err == nil {
		t.Fatal("equivalent numeric prefixes accepted as separate migrations")
	}
}

func TestMigrationOrdinalPreflightRejectsInvalidFileNames(t *testing.T) {
	for _, name := range []string{
		"badprefix_test.up.sql",
		"000161.up.sql",
		"000161_.up.sql",
		"_unnumbered.up.sql",
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateMigrationOrdinals([]string{name}); err == nil {
				t.Fatal("invalid migration name was accepted")
			}
		})
	}
}

func TestMigrationOrdinalPreflightAllowsForwardOnlyNextMigration(t *testing.T) {
	files := []string{
		"000159_staged_account_transfers.up.sql",
		"000160_traffic_report_checkpoints.up.sql",
		"000161_credential_revocation_preparations.up.sql",
	}
	if err := validateMigrationOrdinals(files); err != nil {
		t.Fatalf("valid migration sequence rejected: %v", err)
	}
}

func TestCurrentMigrationCatalogHasNoOrdinalCollisions(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "..", "migrations", "*.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no migrations found: cannot verify catalog")
	}
	if err := validateMigrationOrdinals(files); err != nil {
		t.Fatalf("checked-in migration catalog is ambiguous: %v", err)
	}
}
