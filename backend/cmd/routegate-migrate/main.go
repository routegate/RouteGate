package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/ikaevus/routegate/backend/internal/db"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Explicit opt-in one-shot migrator, shipped with the matching Manager and SQL.
// Installation does not invoke it or enable external migration mode.
func main() {
	// Adapt #639's explicit role-provisioning command here, alongside the
	// existing privileged migrator, rather than adding another Manager DDL path.
	runtimeRole := flag.String("create-runtime-role", "", "create a new NOLOGIN runtime role on the fully migrated canonical schema, then exit")
	checkOnly := flag.Bool("check-runtime", false, "verify runtime isolation, migration history and required grants without changing the database")
	checkConnections := flag.Bool("check-connections", false, "verify both independent database identities and runtime grants without applying migrations")
	flag.Parse()
	requested := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "create-runtime-role" {
			requested = true
		}
	})
	if flag.NArg() != 0 || (requested && (*runtimeRole == "" || *checkOnly || *checkConnections)) || (*checkOnly && *checkConnections) {
		flag.Usage()
		os.Exit(2)
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	var err error
	if *checkConnections {
		err = runMode(ctx, logger, false)
	} else if *checkOnly {
		err = checkRuntime(ctx)
	} else if requested {
		err = createRuntimeRole(ctx, logger, *runtimeRole)
	} else {
		err = run(ctx, logger)
	}
	if err != nil {
		logger.Error("dedicated migration stopped", "error", err)
		os.Exit(1)
	}
}

func createRuntimeRole(ctx context.Context, logger *slog.Logger, name string) error {
	// No runtime password, role adoption, LOGIN activation or ACL repair. This
	// owner connection is explicit and is never placed in the Manager service.
	migratorURL := os.Getenv("ROUTEGATE_MIGRATOR_DATABASE_URL")
	if migratorURL == "" {
		return errors.New("ROUTEGATE_MIGRATOR_DATABASE_URL is required for role provisioning")
	}
	owner, err := db.Connect(ctx, migratorURL, logger)
	if err != nil {
		return err
	}
	defer owner.Close()
	if err = db.RequireAppliedMigrations(ctx, owner, "migrations"); err != nil {
		return err
	}
	if err = db.ProvisionRuntimeRole(ctx, owner, name); err != nil {
		return err
	}
	logger.Info("created NOLOGIN runtime role; authentication is not enabled")
	return nil
}

func run(ctx context.Context, logger *slog.Logger) error {
	return runMode(ctx, logger, true)
}

func runMode(ctx context.Context, logger *slog.Logger, apply bool) error {
	migratorURL := os.Getenv("ROUTEGATE_MIGRATOR_DATABASE_URL")
	runtimeURL := os.Getenv("ROUTEGATE_DATABASE_URL")
	if err := db.ValidateDedicatedMigrationURLs(migratorURL, runtimeURL); err != nil {
		return err
	}
	// Never write to a remote DB before verifying both independent logins.
	runtime, err := db.Connect(ctx, runtimeURL, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		return err
	}
	defer runtime.Close()
	owner, err := db.Connect(ctx, migratorURL, logger)
	if err != nil {
		return err
	}
	defer owner.Close()
	var runtimeUser, ownerUser string
	if err = runtime.QueryRow(ctx, `SELECT session_user`).Scan(&runtimeUser); err != nil {
		return err
	}
	if err = owner.QueryRow(ctx, `SELECT session_user`).Scan(&ownerUser); err != nil {
		return err
	}
	if runtimeUser == ownerUser {
		return db.ErrUnseparatedMigrationCredentials
	}
	var member bool
	if err = owner.QueryRow(ctx, `SELECT pg_has_role($1::name,$2::name,'MEMBER')`,
		runtimeUser, ownerUser).Scan(&member); err != nil {
		return err
	}
	if member {
		return db.ErrUnseparatedMigrationCredentials
	}
	// The private dispatch schema does not exist at the current schema head.
	// Reject pre-existing owner authority even before it is introduced.
	if err = db.CheckExternalMigrationRuntimeRole(ctx, runtime); err != nil {
		return err
	}
	if err = db.CheckHeldDispatchRuntimeIsolation(ctx, runtime); err != nil {
		return err
	}
	if apply {
		if err = db.Migrate(ctx, owner, "migrations", logger); err != nil {
			return err
		}
	}
	if err = verifyRuntime(ctx, runtime); err != nil {
		return err
	}
	logger.Info("database verification complete", "migrations_applied", apply)
	return nil
}

// checkRuntime uses only the runtime login. It cannot apply DDL, create roles,
// repair grants or consume owner credentials during rollback validation.
func checkRuntime(ctx context.Context) error {
	runtimeURL := os.Getenv("ROUTEGATE_DATABASE_URL")
	if runtimeURL == "" {
		return errors.New("ROUTEGATE_DATABASE_URL is required for runtime verification")
	}
	runtime, err := db.Connect(ctx, runtimeURL, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		return err
	}
	defer runtime.Close()
	return verifyRuntime(ctx, runtime)
}

func verifyRuntime(ctx context.Context, runtime *pgxpool.Pool) error {
	// Shared with the post-DDL check; verification never repairs authority.
	if err := db.CheckExternalMigrationRuntimeRole(ctx, runtime); err != nil {
		return err
	}
	if err := db.CheckHeldDispatchRuntimeIsolation(ctx, runtime); err != nil {
		return err
	}
	if err := db.RequireAppliedMigrations(ctx, runtime, "migrations"); err != nil {
		return err
	}
	if err := db.CheckRuntimeRequiredPrivileges(ctx, runtime); err != nil {
		return err
	}
	return ctx.Err()
}
