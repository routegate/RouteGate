package db

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// validateMigrationSequence runs before opening a database transaction or
// creating schema_migrations. Distinct names with the same ordinal (e.g.
// 000160_traffic_* and 000160_revocation_*) would otherwise both apply because
// the existing journal keys by complete filename rather than sequence.
// Lettered ordinals like 000150a and 000150b are intentionally distinct.
func validateMigrationSequence(files []string) error {
	seen := make(map[string]string, len(files))
	for _, file := range files {
		version := strings.TrimSuffix(filepath.Base(file), ".up.sql")
		ordinal := strings.SplitN(version, "_", 2)[0]
		if previous, exists := seen[ordinal]; exists {
			return fmt.Errorf("migration sequence %q reused by %q and %q", ordinal, previous, version)
		}
		seen[ordinal] = version
	}
	return nil
}

func Migrate(ctx context.Context, pool *pgxpool.Pool, migrationsDir string, logger *slog.Logger) error {
	// Fail without schema mutation when source/bundle histories collide.
	files, err := filepath.Glob(filepath.Join(migrationsDir, "*.up.sql"))
	if err != nil {
		return fmt.Errorf("list migration files: %w", err)
	}
	sort.Strings(files)
	if err := validateMigrationSequence(files); err != nil {
		return err
	}

	if _, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		);
	`); err != nil {
		return fmt.Errorf("create schema_migrations table: %w", err)
	}

	for _, file := range files {
		version := strings.TrimSuffix(filepath.Base(file), ".up.sql")

		applied, err := isApplied(ctx, pool, version)
		if err != nil {
			return err
		}

		if applied {
			continue
		}

		content, err := os.ReadFile(file)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", version, err)
		}

		tx, err := pool.Begin(ctx)
		if err != nil {
			return fmt.Errorf("begin migration %s: %w", version, err)
		}

		if _, err := tx.Exec(ctx, string(content)); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("apply migration %s: %w", version, err)
		}

		if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, version); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("record migration %s: %w", version, err)
		}

		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit migration %s: %w", version, err)
		}

		logger.Info("applied migration", "version", version)
	}

	return nil
}

func isApplied(ctx context.Context, pool *pgxpool.Pool, version string) (bool, error) {
	var exists bool

	if err := pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM schema_migrations
			WHERE version = $1
		);
	`, version).Scan(&exists); err != nil {
		return false, fmt.Errorf("check migration %s: %w", version, err)
	}

	return exists, nil
}
