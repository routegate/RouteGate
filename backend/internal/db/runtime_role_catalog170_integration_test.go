package db

import (
	"sort"
	"testing"
)

func TestRG140Schema170PublicTableInventory(t *testing.T) {
	ctx, pool, done := setupAppliedSettingsTest(t)
	defer done()
	var current bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1 FROM public.schema_migrations
		WHERE version='000170_credential_removal_dispatch_stages'
	)`).Scan(&current); err != nil || !current {
		t.Fatalf("canonical schema 170 was not applied: current=%v err=%v", current, err)
	}
	rows, err := pool.Query(ctx, `SELECT c.relname FROM pg_catalog.pg_class c
		JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
		WHERE n.nspname='public' AND c.relkind IN ('r','p')
		AND c.relname <> 'schema_migrations'`)
	if err != nil {
		t.Fatal(err)
	}
	actual := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		actual[name] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		t.Fatal(err)
	}
	expected := map[string]bool{}
	for _, name := range runtimeTableNames() {
		if expected[name] {
			t.Fatalf("duplicate schema-170 table identity: %s", name)
		}
		expected[name] = true
	}
	var missing, unexpected []string
	for name := range expected {
		if !actual[name] {
			missing = append(missing, name)
		}
	}
	for name := range actual {
		if !expected[name] {
			unexpected = append(unexpected, name)
		}
	}
	sort.Strings(missing)
	sort.Strings(unexpected)
	if len(missing) > 0 || len(unexpected) > 0 {
		t.Fatalf("schema-170 catalog mismatch; missing=%v unexpected=%v", missing, unexpected)
	}
}
