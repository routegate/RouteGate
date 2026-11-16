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
		"000150_delivery_device_scope.up.sql",
		"000150a_vpn_subscription_token_pkey_repair.up.sql",
		"000150b_client_profile_schema_invariant_repair.up.sql",
		"000159_staged_account_transfers.up.sql",
		"000160_traffic_report_checkpoints.up.sql",
	}
	if err := validateMigrationOrdinals(files); err != nil {
		t.Fatalf("grandfathered migration history was rejected: %v", err)
	}
}

func TestUnknownLegacyMigrationAliasPreventsAllDatabaseWork(t *testing.T) {
	cases := []struct {
		name  string
		files []string
	}{
		{
			name: "new alias at legacy 003",
			files: []string{
				"000003_auth_foundation.up.sql",
				"000003_config_apply_jobs.up.sql",
				"000003_unreviewed_migration.up.sql",
			},
		},
		{
			name: "new suffix at legacy 150",
			files: []string{
				"000150_delivery_device_scope.up.sql",
				"000150a_vpn_subscription_token_pkey_repair.up.sql",
				"000150b_client_profile_schema_invariant_repair.up.sql",
				"000150c_unreviewed_repair.up.sql",
			},
		},
		{
			name: "noncanonical padding alias at legacy 003",
			files: []string{
				"000003_auth_foundation.up.sql",
				"003_unreviewed_alias.up.sql",
			},
		},
		{
			name: "collision with last legacy 159",
			files: []string{
				"000159_staged_account_transfers.up.sql",
				"000159_unreviewed_migration.up.sql",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for _, name := range tc.files {
				if err := os.WriteFile(filepath.Join(dir, name), []byte("SELECT 1;"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			// Deliberately nil: a dangerous catalog must be refused before SQL.
			err := Migrate(context.Background(), nil, dir, nil)
			if err == nil || !strings.Contains(err.Error(), "ambiguous migration ordinal") ||
				!strings.Contains(err.Error(), "historical aliases") {
				t.Fatalf("new legacy ordinal collision was not rejected pre-SQL: %v", err)
			}
		})
	}
}

// A novel *non-colliding* filename is just as dangerous if its number falls
// within already shipped migrations: it would apply out of order on upgrade.
func TestUnshippedHistoricalMigrationNameFailsBeforeSQL(t *testing.T) {
	tests := []struct {
		name string
		file string
	}{
		{"unused gap in history", "000050_unreviewed_migration.up.sql"},
		{"different unused gap", "000005_unreviewed_migration.up.sql"},
		{"replaced historical singleton", "000020_rewritten_traffic_limits.up.sql"},
		{"replaced shipped 160", "000160_credential_revocation_preparations.up.sql"},
		{"replaced shipped 1430 outlier", "0001430_unreviewed_timestamp_change.up.sql"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, tt.file), []byte("SELECT 1;"), 0600); err != nil {
				t.Fatal(err)
			}
			// Nil pool proves that rejecting an unshipped legacy identity is
			// independent of and precedes CREATE TABLE schema_migrations.
			err := Migrate(context.Background(), nil, dir, nil)
			if err == nil || !strings.Contains(err.Error(), "historical migration identity") ||
				!strings.Contains(err.Error(), tt.file) {
				t.Fatalf("unshipped migration %q reached SQL or was accepted: %v", tt.file, err)
			}
		})
	}
}

func TestKnownHistoricalSingletonIsValidOnItsOwn(t *testing.T) {
	for _, name := range []string{
		"000001_init.up.sql",
		"000020_traffic_stats_limits.up.sql",
		"000159_staged_account_transfers.up.sql",
		"000160_traffic_report_checkpoints.up.sql",
		"0001430_platform_update_terminal_timestamp_proof.up.sql",
	} {
		if err := validateMigrationOrdinals([]string{name}); err != nil {
			t.Errorf("shipped singleton %q incorrectly rejected: %v", name, err)
		}
	}
}

func TestHistoricalHighOrdinalExceptionRemainsValid(t *testing.T) {
	// This unusual older filename already exists on main. Do not renumber it:
	// migration identities are full filenames recorded in schema_migrations.
	files := []string{
		"0001430_platform_update_terminal_timestamp_proof.up.sql",
		"000159_staged_account_transfers.up.sql",
		"000160_traffic_report_checkpoints.up.sql",
	}
	if err := validateMigrationOrdinals(files); err != nil {
		t.Fatalf("existing outlier ordinal was incorrectly rejected: %v", err)
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
		"000162_credential_revocation_confirmations.up.sql",
		"000163_credential_revocation_dispatch_claims.up.sql",
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
