package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/ikaevus/routegate/backend/internal/db"
)

// Explicit opt-in, source-only one-shot migrator. It is deliberately NOT
// installed or invoked by install.sh or the Manager systemd unit.
func main() {
	logger:=slog.New(slog.NewTextHandler(os.Stderr,nil))
	if err:=run(context.Background(),logger);err!=nil {
		logger.Error("dedicated migration stopped", "error",err)
		os.Exit(1)
	}
}

func run(parent context.Context,logger *slog.Logger) error {
	migratorURL:=os.Getenv("ROUTEGATE_MIGRATOR_DATABASE_URL")
	runtimeURL:=os.Getenv("ROUTEGATE_DATABASE_URL")
	if err:=db.ValidateDedicatedMigrationURLs(migratorURL,runtimeURL);err!=nil {
		return err
	}
	ctx,cancel:=signal.NotifyContext(parent,os.Interrupt,syscall.SIGTERM)
	defer cancel()
	// Never write to a remote DB before verifying both independent logins.
	runtime,err:=db.Connect(ctx,runtimeURL,slog.New(slog.NewTextHandler(io.Discard,nil)))
	if err!=nil {return err}
	defer runtime.Close()
	owner,err:=db.Connect(ctx,migratorURL,logger)
	if err!=nil {return err}
	defer owner.Close()
	var runtimeUser,ownerUser string
	if err=runtime.QueryRow(ctx,`SELECT session_user`).Scan(&runtimeUser);err!=nil {return err}
	if err=owner.QueryRow(ctx,`SELECT session_user`).Scan(&ownerUser);err!=nil {return err}
	if runtimeUser==ownerUser {return db.ErrUnseparatedMigrationCredentials}
	var member bool
	if err=owner.QueryRow(ctx,`SELECT pg_has_role($1::name,$2::name,'MEMBER')`,
		runtimeUser,ownerUser).Scan(&member);err!=nil {return err}
	if member {return db.ErrUnseparatedMigrationCredentials}
	// The private dispatch schema does not exist at the current schema head.
	// Reject pre-existing owner authority even before it is introduced.
	if err=db.CheckExternalMigrationRuntimeRole(ctx,runtime);err!=nil {return err}
	if err=db.CheckHeldDispatchRuntimeIsolation(ctx,runtime);err!=nil {return err}
	if err=db.Migrate(ctx,owner,"migrations",logger);err!=nil {return err}
	if err=db.CheckHeldDispatchRuntimeIsolation(ctx,runtime);err!=nil {return err}
	if err=db.RequireAppliedMigrations(ctx,runtime,"migrations");err!=nil {return err}
	if err=ctx.Err();err!=nil {return err}
	logger.Info("dedicated migration complete; runtime schema verified")
	return nil
}
