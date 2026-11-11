package db

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateMigrationSequence(t *testing.T) {
	t.Run("refuse_conflicting_000160_before_database_mutation", func(t *testing.T) {
		files := []string{
			"backend/migrations/000160_traffic_report_checkpoints.up.sql",
			"backend/migrations/000160_credential_revocation_preparations.up.sql",
		}
		err := validateMigrationSequence(files)
		if err == nil || !strings.Contains(err.Error(), "000160") {
			t.Fatalf("collision not rejected: %v", err)
		}
	})

	t.Run("grandfather_existing_shared_000003_history", func(t *testing.T) {
		files := []string{
			"backend/migrations/000003_auth_foundation.up.sql",
			"backend/migrations/000003_config_apply_jobs.up.sql",
		}
		if err := validateMigrationSequence(files); err != nil {
			t.Fatalf("legacy migration history must remain usable: %v", err)
		}
	})

	t.Run("preserve_lettered_migration_history", func(t *testing.T) {
		files := []string{
			"backend/migrations/000150a_client_profile_schema_repair.up.sql",
			"backend/migrations/000150b_client_profile_schema_invariant_repair.up.sql",
			"backend/migrations/000151_other.up.sql",
			"backend/migrations/000160_traffic_report_checkpoints.up.sql",
			"backend/migrations/000161_credential_revocation_preparations.up.sql",
		}
		if err := validateMigrationSequence(files); err != nil {
			t.Fatalf("valid forward-only ordinals rejected: %v", err)
		}
	})

	t.Run("duplicate_filename_cannot_bypass_guard", func(t *testing.T) {
		files := []string{"000160_traffic.up.sql", "000160_traffic.up.sql"}
		if err := validateMigrationSequence(files); err == nil {
			t.Fatal("duplicate migration file accepted")
		}
	})
}

func TestCurrentMigrationDirectoryHasUniqueSequences(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "..", "migrations", "*.up.sql"))
	if err != nil || len(files) == 0 {
		t.Fatalf("cannot enumerate project migrations: %v, files=%d", err, len(files))
	}
	if err := validateMigrationSequence(files); err != nil {
		t.Fatalf("current migration directory has a sequence collision: %v", err)
	}
}
