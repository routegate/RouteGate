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

func Migrate(ctx context.Context, pool *pgxpool.Pool, migrationsDir string, logger *slog.Logger) error {
	// Check the entire migration catalog BEFORE any SQL, including CREATE TABLE.
	// schema_migrations keys by full filename; two different 000160_* names
	// would otherwise both be treated as unapplied and executed.
	files, err := filepath.Glob(filepath.Join(migrationsDir, "*.up.sql"))
	if err != nil {
		return fmt.Errorf("list migration files: %w", err)
	}
	sort.Strings(files)
	if err := validateMigrationOrdinals(files); err != nil {
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

// validateMigrationOrdinals is a fail-closed upgrade gate. Migration names
// record a unique, monotonically sortable decimal ordinal followed by "_"
// and a descriptive suffix. Comparing canonical decimal prefixes also
// refuses ambiguous padding (e.g. 000160_foo and 160_bar). Never rename an
// already-applied migration to resolve a conflict: allocate a fresh ordinal.
func validateMigrationOrdinals(files []string) error {
	seen := make(map[string]string, len(files))
	for _, path := range files {
		name := filepath.Base(path)
		version := strings.TrimSuffix(name, ".up.sql")
		raw, suffix, ok := strings.Cut(version, "_")
		if !ok || suffix == "" || raw == "" {
			return fmt.Errorf("invalid migration name %q: require <decimal>_<description>.up.sql", name)
		}
		for _, digit := range raw {
			if digit < '0' || digit > '9' {
				return fmt.Errorf("invalid migration ordinal in %q: require decimal prefix", name)
			}
		}
		ordinal := strings.TrimLeft(raw, "0")
		if ordinal == "" {
			ordinal = "0"
		}
		if original, conflict := seen[ordinal]; conflict {
			return fmt.Errorf("ambiguous migration ordinal %s: %q conflicts with %q; assign a new forward-only ordinal", ordinal, original, name)
		}
		seen[ordinal] = name
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
