package db

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"
)

// TestMigrationUpgradeFrom159To160PreservesExistingData exercises the actual
// PostgreSQL migrator on an already-applied RouteGate catalog. It intentionally
// starts with the full historical lexical list BEFORE 000160 and then upgrades
// the same database to the checked-in current catalog. This is not a test of
// an external customer's older release, which needs separate acceptance.
func TestMigrationUpgradeFrom159To160PreservesExistingData(t *testing.T) {
	databaseURL := os.Getenv("ROUTEGATE_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("ROUTEGATE_TEST_DATABASE_URL is not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	pool, err := Connect(ctx, databaseURL, logger)
	if err != nil {
		t.Fatalf("connect to disposable PostgreSQL: %v", err)
	}
	defer pool.Close()
	resetPublicSchema(t, ctx, pool)

	legacyCatalog := copyMigrationsBefore(t, "../../migrations", "000160_traffic_report_checkpoints.up.sql")
	if err := Migrate(ctx, pool, legacyCatalog, logger); err != nil {
		t.Fatalf("apply historic migrations through 159: %v", err)
	}
	var beforeCount int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM schema_migrations").Scan(&beforeCount); err != nil {
		t.Fatal(err)
	}
	if beforeCount != 84 {
		t.Fatalf("legacy catalog recorded %d migrations, want 84 including historical 1430", beforeCount)
	}
	beforeVersion, err := NewSchemaVersionRepository(pool).AppliedSchemaVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if beforeVersion != "000159_staged_account_transfers" {
		t.Fatalf("last legacy migration = %q; expected 000159_staged_account_transfers", beforeVersion)
	}

	// A real row must survive an upgrade. Comparing applied_at also proves
	// the old migration identities were not silently reapplied or rewritten.
	var serverID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO servers (name, status)
		VALUES ('migration-upgrade-159-fixture', 'active')
		RETURNING id::text
	`).Scan(&serverID); err != nil {
		t.Fatalf("seed existing server: %v", err)
	}
	var beforeApplied time.Time
	if err := pool.QueryRow(ctx, `
		SELECT applied_at FROM schema_migrations
		WHERE version = '000159_staged_account_transfers'
	`).Scan(&beforeApplied); err != nil {
		t.Fatalf("read legacy migration timestamp: %v", err)
	}

	for run := 1; run <= 2; run++ {
		if err := Migrate(ctx, pool, "../../migrations", logger); err != nil {
			t.Fatalf("upgrade/retry run %d: %v", run, err)
		}
		var count int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM schema_migrations").Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 85 {
			t.Fatalf("upgrade/retry run %d recorded %d migrations, want 85", run, count)
		}
		var name string
		if err := pool.QueryRow(ctx, "SELECT name FROM servers WHERE id = $1::uuid", serverID).Scan(&name); err != nil {
			t.Fatalf("existing server missing after migration run %d: %v", run, err)
		}
		if name != "migration-upgrade-159-fixture" {
			t.Fatalf("existing server changed after migration run %d: %q", run, name)
		}
		var afterApplied time.Time
		if err := pool.QueryRow(ctx, `
			SELECT applied_at FROM schema_migrations
			WHERE version = '000159_staged_account_transfers'
		`).Scan(&afterApplied); err != nil {
			t.Fatal(err)
		}
		if !beforeApplied.Equal(afterApplied) {
			t.Fatalf("legacy migration timestamp was modified on run %d", run)
		}
	}
	version, err := NewSchemaVersionRepository(pool).AppliedSchemaVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if version != "000160_traffic_report_checkpoints" {
		t.Fatalf("last migration = %q, want 000160_traffic_report_checkpoints", version)
	}
}
