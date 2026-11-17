package db

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExternalMigrationURLSeparation(t *testing.T) {
	const migrator="postgres://rg_migrator:test@127.0.0.1:5432/routegate_ci?sslmode=disable"
	const runtime="postgres://rg_runtime:test@127.0.0.1:5432/routegate_ci?sslmode=disable"
	if err:=ValidateDedicatedMigrationURLs(migrator,runtime);err!=nil {
		t.Fatal("distinct connections to same disposable database refused",err)
	}
	for _,tc:=range []struct{name string;migrator,runtime string}{
		{"missing_migrator","",runtime},
		{"missing_runtime",migrator,""},
		{"same_role",migrator,migrator},
		{"other_database",migrator,"postgres://rg_runtime:test@127.0.0.1:5432/other_db?sslmode=disable"},
		{"other_port",migrator,"postgres://rg_runtime:test@127.0.0.1:5433/routegate_ci?sslmode=disable"},
		{"other_host",migrator,"postgres://rg_runtime:test@other.internal:5432/routegate_ci?sslmode=disable"},
		{"malformed",migrator,"not a database URL"},
	} {
		t.Run(tc.name,func(t *testing.T){
			if err:=ValidateDedicatedMigrationURLs(tc.migrator,tc.runtime);!errors.Is(err,ErrUnseparatedMigrationCredentials){
				t.Fatal("unsafe external migration connection pair accepted",err)
			}
		})
	}
}

func TestExternalMigrationReadOnlySchemaPreflight(t *testing.T) {
	ctx,pool,done:=setupAppliedSettingsTest(t)
	defer done()
	if err:=RequireAppliedMigrations(ctx,pool,"../../migrations");err!=nil {
		t.Fatal("fully migrated schema failed read-only inspection",err)
	}
	dir:=t.TempDir()
	if err:=os.WriteFile(filepath.Join(dir,"000171_missing_held_dispatch.up.sql"),
		[]byte("SELECT 1;"),0600);err!=nil {t.Fatal(err)}
	if err:=RequireAppliedMigrations(ctx,pool,dir);!errors.Is(err,ErrExternalMigrationIncomplete) ||
		!strings.Contains(err.Error(),"unapplied 000171_missing_held_dispatch") {
		t.Fatal("pending migration did not block external-mode runtime",err)
	}
	if err:=RequireAppliedMigrations(ctx,pool,t.TempDir());!errors.Is(err,ErrExternalMigrationIncomplete){
		t.Fatal("empty migration directory unexpectedly permitted",err)
	}
}
