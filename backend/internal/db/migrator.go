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
// and legacy suffixed versions such as 000150b. Their exact names are
// grandfathered; NEW aliases at those ordinals must be rejected. The original
// applied filenames in schema_migrations MUST remain unchanged.
//
// Starting with current-main's 000160_traffic_report_checkpoints, reject
// duplicate numeric ordinals before any SQL. Never rename an applied 000160
// to accommodate the old, unmerged RG-140 000160 revocation preparation.
func validateMigrationOrdinals(files []string) error {
	const forwardOnlyBoundary uint64 = 160
	// Only the actual pre-160 aliases already shipped by RouteGate are
	// grandfathered. New collisions at old ordinals are never safe merely
	// because the number precedes the forward-only boundary.
	legacyAliases := map[uint64]map[string]struct{}{
        3: {
            "000003_auth_foundation.up.sql": {},
            "000003_config_apply_jobs.up.sql": {},
        },
        101: {
            "000101_initial_setup_tokens.up.sql": {},
            "000101_vless_reality_credentials.up.sql": {},
        },
        102: {
            "000102_agent_protocol_version.up.sql": {},
            "000102_config_apply_success_marks_version_applied.up.sql": {},
            "000102_vpn_client_profiles.up.sql": {},
        },
        103: {
            "000103_agent_operation_jobs.up.sql": {},
            "000103_protocol_settings_timestamp.up.sql": {},
        },
        130: {
            "000130_multi_protocol_account_profiles.up.sql": {},
            "000130z_client_profile_schema_invariant_repair.up.sql": {},
        },
        131: {
            "000131_security_event_visibility.up.sql": {},
            "000131z_safe_client_protocol_activation_repair.up.sql": {},
            "000131zz_runtime_install_operations_repair.up.sql": {},
        },
        140: {
            "000140_platform_update_rollouts.up.sql": {},
            "000140_platform_update_unknown_outcome_interlock.up.sql": {},
        },
        141: {
            "000141_platform_update_rollout_planning_snapshot.up.sql": {},
            "000141z_platform_update_history_truncate_guard.up.sql": {},
        },
        150: {
            "000150_delivery_device_scope.up.sql": {},
            "000150a_vpn_subscription_token_pkey_repair.up.sql": {},
            "000150b_client_profile_schema_invariant_repair.up.sql": {},
        },
	}
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
			if original, conflict := seen[ordinal]; conflict {
				allowed := legacyAliases[ordinal]
				_, originalKnown := allowed[original]
				_, nameKnown := allowed[name]
				if !originalKnown || !nameKnown {
					return fmt.Errorf("ambiguous migration ordinal %d: %q conflicts with %q; only known historical aliases are grandfathered", ordinal, original, name)
				}
			}
			seen[ordinal] = name
			continue // keep established historical filenames and execution order intact
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
