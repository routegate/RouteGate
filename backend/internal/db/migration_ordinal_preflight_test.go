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

func TestLegacyMigrationIdentityIsPreservedVerbatim(t *testing.T) {
	// Real RouteGate migration history includes both numbered 000003 files
	// and letter-suffixed legacy variants. An upgrade gate must never force
	// renaming or removal of production-applied filenames.
	files := []string{
		"000003_auth_foundation.up.sql",
		"000003_config_apply_jobs.up.sql",
		"000150b_client_profile_repair.up.sql",
		"000159_staged_account_transfers.up.sql",
		"000160_traffic_report_checkpoints.up.sql",
	}
	if err := validateMigrationOrdinals(files); err != nil {
		t.Fatalf("grandfathered migration history was rejected: %v", err)
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
		"000161b_noncanonical.up.sql",
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
		"000162_runtime_recovery_reservations.up.sql",
		"000163_runtime_recovery_challenge_ledger.up.sql",
		"000164_runtime_recovery_attempt_ledger.up.sql",
		"000165_runtime_recovery_observation_nonce.up.sql",
		"000166_runtime_pre_dispatch_checkpoint.up.sql",
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

 
// Keep a cheap, pre-database guard for the five new PL/pgSQL bodies. A
// single-dollar terminator breaks Manager startup even when Go compiles.
// Full PostgreSQL integration tests still validate migration semantics.
func TestPreDispatchCheckpointMigrationDollarQuotesAreBalanced(t *testing.T) {
	data, err := os.ReadFile("../../migrations/000166_runtime_pre_dispatch_checkpoint.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(data)
	if strings.Contains(sql, "END; $;") ||
		strings.Count(sql, "AS $$") != 5 ||
		strings.Count(sql, "END; $$;") != 5 ||
		strings.Count(sql, "$$") != 10 {
		t.Fatal("pre-dispatch checkpoint migration has malformed PL/pgSQL dollar quoting")
	}
}
