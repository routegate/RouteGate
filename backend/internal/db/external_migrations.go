package db

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrExternalMigrationIncomplete = errors.New("external database migrations are not completely applied")
var ErrUnseparatedMigrationCredentials = errors.New("migration and runtime PostgreSQL connections must use separate identities on the same database")

// ValidateDedicatedMigrationURLs rejects accidentally running the migrator
// with the same login or against a different database. It deliberately does
// not log either URL (both can contain secrets). Distinct names alone do not
// prove SQL role isolation: the runtime role is checked separately as well.
func ValidateDedicatedMigrationURLs(migratorURL, runtimeURL string) error {
	if strings.TrimSpace(migratorURL)=="" || strings.TrimSpace(runtimeURL)=="" {
		return ErrUnseparatedMigrationCredentials
	}
	m,err:=pgxpool.ParseConfig(migratorURL)
	if err!=nil {return ErrUnseparatedMigrationCredentials}
	r,err:=pgxpool.ParseConfig(runtimeURL)
	if err!=nil {return ErrUnseparatedMigrationCredentials}
	if m.ConnConfig.User=="" || r.ConnConfig.User=="" || m.ConnConfig.User==r.ConnConfig.User ||
		m.ConnConfig.Database!=r.ConnConfig.Database ||
		m.ConnConfig.Host!=r.ConnConfig.Host || m.ConnConfig.Port!=r.ConnConfig.Port {
		return ErrUnseparatedMigrationCredentials
	}
	return nil
}

// RequireAppliedMigrations is a read-only startup check for an independently
// migrated database. It never creates schema_migrations, runs migration SQL,
// repairs history or advances the schema. Unexpected newer/foreign migration
// identities also fail closed, preventing an old Manager from running against
// a database initialized by another incompatible code line.
func RequireAppliedMigrations(ctx context.Context,pool *pgxpool.Pool,migrationsDir string) error {
	files,err:=filepath.Glob(filepath.Join(migrationsDir,"*.up.sql"))
	if err!=nil {return fmt.Errorf("scan migration catalog: %w",err)}
	sort.Strings(files)
	if len(files)==0 {return fmt.Errorf("%w: empty migration catalog",ErrExternalMigrationIncomplete)}
	if err=validateMigrationOrdinals(files);err!=nil {return err}
	wanted:=make(map[string]struct{},len(files))
	for _,file:=range files {
		wanted[strings.TrimSuffix(filepath.Base(file),".up.sql")]=struct{}{}
	}
	rows,err:=pool.Query(ctx,`SELECT version FROM public.schema_migrations`)
	if err!=nil {return fmt.Errorf("%w: cannot read migration history: %v",ErrExternalMigrationIncomplete,err)}
	defer rows.Close()
	seen:=make(map[string]struct{},len(files))
	for rows.Next(){
		var version string
		if err=rows.Scan(&version);err!=nil {return fmt.Errorf("%w: unreadable migration history",ErrExternalMigrationIncomplete)}
		seen[version]=struct{}{}
	}
	if err=rows.Err();err!=nil {return fmt.Errorf("%w: interrupted history read: %v",ErrExternalMigrationIncomplete,err)}
	for _,file:=range files {
		version:=strings.TrimSuffix(filepath.Base(file),".up.sql")
		if _,ok:=seen[version];!ok {
			return fmt.Errorf("%w: unapplied %s",ErrExternalMigrationIncomplete,version)
		}
	}
	for version:=range seen {
		if _,ok:=wanted[version];!ok {
			return fmt.Errorf("%w: unknown applied migration %s",ErrExternalMigrationIncomplete,version)
		}
	}
	return nil
}
