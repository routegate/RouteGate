package db

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestProductionLikeDownMigrationIsAtomicWithHistoryRemoval(t *testing.T) {
	t.Setenv("RG_UPDATE_LOCK_FILE", filepath.Join(t.TempDir(), "update.lock"))
	databaseURL := os.Getenv("ROUTEGATE_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("ROUTEGATE_TEST_DATABASE_URL is not set")
	}
	if _, err := exec.LookPath("psql"); err != nil {
		t.Fatalf("psql is required when ROUTEGATE_TEST_DATABASE_URL is set: %v", err)
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Fatalf("python3 is required when ROUTEGATE_TEST_DATABASE_URL is set: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	pool, err := Connect(ctx, databaseURL, logger)
	if err != nil {
		t.Fatalf("connect to PostgreSQL: %v", err)
	}
	defer pool.Close()

	deployScript, err := filepath.Abs("../../../scripts/deploy-production-like-bundle.sh")
	if err != nil {
		t.Fatalf("resolve deploy script: %v", err)
	}
	migrationsDir, err := filepath.Abs("../../migrations")
	if err != nil {
		t.Fatalf("resolve migrations directory: %v", err)
	}

	t.Run("000151 rolls physical DDL back when history deletion fails", func(t *testing.T) {
		resetPublicSchema(t, ctx, pool)
		if err := Migrate(ctx, pool, migrationsDir, logger); err != nil {
			t.Fatalf("apply migrations: %v", err)
		}

		installMigrationHistoryDeleteBlocker(t, ctx, pool, "000151_delivery_token_generation")
		err := runProductionLikeDownMigration(ctx, deployScript, databaseURL, migrationsDir, "000151_delivery_token_generation")
		if err == nil {
			t.Fatal("expected rollback helper to fail when schema_migrations deletion is rejected")
		}
		if !testColumnExists(t, ctx, pool, "deliveries", "subscription_token_id") {
			t.Fatal("deliveries.subscription_token_id was dropped despite transaction rollback")
		}
		if !testMigrationRecorded(t, ctx, pool, "000151_delivery_token_generation") {
			t.Fatal("000151 history row disappeared despite transaction rollback")
		}
	})

	t.Run("000149 outer transaction wrapper is safely absorbed by runner", func(t *testing.T) {
		resetPublicSchema(t, ctx, pool)
		if err := Migrate(ctx, pool, migrationsDir, logger); err != nil {
			t.Fatalf("apply migrations: %v", err)
		}

		for _, version := range []string{
			"000151_delivery_token_generation",
			"000150b_client_profile_schema_invariant_repair",
			"000150a_vpn_subscription_token_pkey_repair",
			"000150_delivery_device_scope",
		} {
			if err := runProductionLikeDownMigration(ctx, deployScript, databaseURL, migrationsDir, version); err != nil {
				t.Fatalf("prepare rollback chain at %s: %v", version, err)
			}
		}

		installMigrationHistoryDeleteBlocker(t, ctx, pool, "000149_vpn_account_devices")
		err := runProductionLikeDownMigration(ctx, deployScript, databaseURL, migrationsDir, "000149_vpn_account_devices")
		if err == nil {
			t.Fatal("expected 000149 rollback helper to fail when schema_migrations deletion is rejected")
		}
		if !testTableExists(t, ctx, pool, "vpn_account_devices") {
			t.Fatal("vpn_account_devices was dropped despite transaction rollback")
		}
		if !testMigrationRecorded(t, ctx, pool, "000149_vpn_account_devices") {
			t.Fatal("000149 history row disappeared despite transaction rollback")
		}
	})
}

func runProductionLikeDownMigration(ctx context.Context, deployScript, databaseURL, migrationsDir, version string) error {
	downFile := filepath.Join(migrationsDir, version+".down.sql")
	cmd := exec.CommandContext(ctx, "bash", deployScript, "--run-down-migration", databaseURL, downFile, version)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w: %s", version, err, string(output))
	}
	return nil
}

func installMigrationHistoryDeleteBlocker(t *testing.T, ctx context.Context, pool *pgxpool.Pool, version string) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		CREATE OR REPLACE FUNCTION rg_test_reject_migration_history_delete()
		RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF OLD.version = '`+version+`' THEN
				RAISE EXCEPTION 'injected schema_migrations delete failure';
			END IF;
			RETURN OLD;
		END;
		$$;
		DROP TRIGGER IF EXISTS rg_test_reject_migration_history_delete_trigger ON schema_migrations;
		CREATE TRIGGER rg_test_reject_migration_history_delete_trigger
		BEFORE DELETE ON schema_migrations
		FOR EACH ROW EXECUTE FUNCTION rg_test_reject_migration_history_delete();
	`); err != nil {
		t.Fatalf("install schema_migrations delete blocker: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DROP TRIGGER IF EXISTS rg_test_reject_migration_history_delete_trigger ON schema_migrations`)
		_, _ = pool.Exec(context.Background(), `DROP FUNCTION IF EXISTS rg_test_reject_migration_history_delete()`)
	})
}

func testMigrationRecorded(t *testing.T, ctx context.Context, pool *pgxpool.Pool, version string) bool {
	t.Helper()
	var exists bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)`, version).Scan(&exists); err != nil {
		t.Fatalf("check migration %s: %v", version, err)
	}
	return exists
}

func testColumnExists(t *testing.T, ctx context.Context, pool *pgxpool.Pool, tableName, columnName string) bool {
	t.Helper()
	var exists bool
	if err := pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_schema = 'public' AND table_name = $1 AND column_name = $2
		)
	`, tableName, columnName).Scan(&exists); err != nil {
		t.Fatalf("check column %s.%s: %v", tableName, columnName, err)
	}
	return exists
}

func testTableExists(t *testing.T, ctx context.Context, pool *pgxpool.Pool, tableName string) bool {
	t.Helper()
	var exists bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass('public.' || $1) IS NOT NULL`, tableName).Scan(&exists); err != nil {
		t.Fatalf("check table %s: %v", tableName, err)
	}
	return exists
}
