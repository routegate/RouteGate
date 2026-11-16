package db

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
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

// Historical migrations (below 160) contain intentional duplicate numeric
// prefixes (for example, both 000003_auth_foundation and 000003_config_apply_jobs)
// and legacy suffixed versions such as 000150b. Their full filenames are
// already persisted in production schema_migrations and MUST remain unchanged.
//
// Starting with current-main's 000160_traffic_report_checkpoints, reject
// duplicate numeric ordinals before any SQL. Never rename an applied 000160
// to accommodate the old, unmerged RG-140 000160 revocation preparation.
func validateMigrationOrdinals(files []string) error {
	const forwardOnlyBoundary uint64 = 160
	seen := make(map[uint64]string, len(files))
	for _, path := range files {
		name := filepath.Base(path)
		version := strings.TrimSuffix(name, ".up.sql")
		// Extract the leading decimal ordinal while retaining historical
		// letter-suffix variants, which predate this preflight.
		prefixLen := 0
		for prefixLen < len(version) && version[prefixLen] >= '0' && version[prefixLen] <= '9' {
			prefixLen++
		}
		if prefixLen == 0 {
			return fmt.Errorf("invalid migration name %q: no leading decimal ordinal", name)
		}
		ordinal, err := strconv.ParseUint(version[:prefixLen], 10, 64)
		if err != nil {
			return fmt.Errorf("invalid migration ordinal in %q: %w", name, err)
		}
		if ordinal < forwardOnlyBoundary {
			continue // freeze established legacy identities verbatim
		}
		if prefixLen >= len(version)-1 || version[prefixLen] != '_' {
			return fmt.Errorf("invalid migration name %q: new migrations require <decimal>_<description>.up.sql", name)
		}
		if original, conflict := seen[ordinal]; conflict {
			return fmt.Errorf("ambiguous migration ordinal %d: %q conflicts with %q; assign a new forward-only ordinal", ordinal, original, name)
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
