package db

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ikaevus/routegate/backend/internal/auth"
	"github.com/ikaevus/routegate/backend/internal/revocations"
	"github.com/ikaevus/routegate/backend/internal/traffic"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Reuse the existing opt-in, disposable, two-login rehearsal and its real
// account/config fixture. Do not create a second role-test infrastructure.
func rehearseRuntimePolicy170(t *testing.T, ctx context.Context, owner, runtime *pgxpool.Pool, in revocations.PrepareInput) {
	t.Helper()
	const role = "rg140_test_manager_runtime"
	const rejectedRole = "rg140_test_policy_rejected"
	if err := CheckRuntimeRequiredPrivileges(ctx, runtime); err != nil {
		t.Fatal("complete policy refused", err)
	}
	if err := ProvisionRuntimeRole(ctx, owner, role); err == nil {
		t.Fatal("adopted an existing runtime role")
	}
	// Run preflight on an actually read-only serving connection, as after a
	// restore. Revoking a grant must cause refusal, never an implicit repair.
	cfg := runtime.Config()
	cfg.ConnConfig.RuntimeParams["default_transaction_read_only"] = "on"
	readonly, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer readonly.Close()
	if err = CheckRuntimeRequiredPrivileges(ctx, readonly); err != nil {
		t.Fatal("required grants check wrote to DB", err)
	}
	for _, policy := range runtimePolicy170 {
		table := pgx.Identifier{"public", policy.name}.Sanitize()
		for _, privilege := range []string{"SELECT", "INSERT", "UPDATE", "DELETE"} {
			required := strings.Contains(policy.privileges, privilege)
			var granted bool
			if err = runtime.QueryRow(ctx, `SELECT has_table_privilege($1,$2)`, table, privilege).Scan(&granted); err != nil {
				t.Fatal(err)
			}
			if granted != required {
				t.Fatalf("unexpected %s policy on %s: %v", privilege, policy.name, granted)
			}
			if !required {
				continue
			}
			if _, err = owner.Exec(ctx, "REVOKE "+privilege+" ON TABLE "+table+" FROM "+role); err != nil {
				t.Fatal(err)
			}
			if err = CheckRuntimeRequiredPrivileges(ctx, readonly); err == nil {
				t.Fatalf("missing %s on %s accepted", privilege, policy.name)
			}
			if err = runtime.QueryRow(ctx, `SELECT has_table_privilege($1,$2)`, table, privilege).Scan(&granted); err != nil || granted {
				t.Fatal("preflight repaired revoked ACL", err)
			}
			if _, err = owner.Exec(ctx, "GRANT "+privilege+" ON TABLE "+table+" TO "+role); err != nil {
				t.Fatal(err)
			}
		}
		if policy.lockColumn != "" {
			column := pgx.Identifier{policy.lockColumn}.Sanitize()
			if _, err = owner.Exec(ctx, "REVOKE UPDATE ("+column+") ON TABLE "+table+" FROM "+role); err != nil {
				t.Fatal(err)
			}
			if err = CheckRuntimeRequiredPrivileges(ctx, readonly); err == nil {
				t.Fatalf("missing row-lock privilege on %s accepted", policy.name)
			}
			if _, err = owner.Exec(ctx, "GRANT UPDATE ("+column+") ON TABLE "+table+" TO "+role); err != nil {
				t.Fatal(err)
			}
		}
	}
	var sequence string
	if err = owner.QueryRow(ctx, `SELECT pg_get_serial_sequence('public.vpn_credential_revocation_events','sequence')`).Scan(&sequence); err != nil {
		t.Fatal(err)
	}
	// pg_get_serial_sequence returns a quoted, server-generated SQL identifier.
	if _, err = owner.Exec(ctx, "REVOKE USAGE ON SEQUENCE "+sequence+" FROM "+role); err != nil {
		t.Fatal(err)
	}
	if err = CheckRuntimeRequiredPrivileges(ctx, readonly); err == nil {
		t.Fatal("missing event-sequence USAGE accepted")
	}
	if _, err = owner.Exec(ctx, "GRANT USAGE ON SEQUENCE "+sequence+" TO "+role); err != nil {
		t.Fatal(err)
	}
	if err = CheckRuntimeRequiredPrivileges(ctx, readonly); err != nil {
		t.Fatal("reconciled policy refused", err)
	}

	// Exercise real repositories under the restricted login, including UPSERT,
	// row/table locks and SECURITY INVOKER event/traffic trigger dependencies.
	if err = auth.NewRepository(runtime).EnsureBuiltIns(ctx); err != nil {
		t.Fatal("runtime auth seed/upsert", err)
	}
	service := revocations.NewService(runtime)
	preparation, err := service.Prepare(ctx, in)
	if err != nil {
		t.Fatal("runtime revocation preparation", err)
	}
	if _, err = service.Cancel(ctx, preparation.ID, in.Actor); err != nil {
		t.Fatal("runtime revocation cancellation", err)
	}
	var eventCount int
	if err = runtime.QueryRow(ctx, `SELECT count(*) FROM vpn_credential_revocation_events WHERE operation_id=$1::uuid`, preparation.ID).Scan(&eventCount); err != nil || eventCount != 2 {
		t.Fatalf("invoker audit trigger: count=%d err=%v", eventCount, err)
	}
	var tokenHash string
	if err = runtime.QueryRow(ctx, `SELECT token_hash FROM agents WHERE server_id=$1::uuid`, in.ServerID).Scan(&tokenHash); err != nil {
		t.Fatal(err)
	}
	events := []traffic.CreateUsageEventInput{{VPNAccountID: in.AccountID, RxBytes: 17, TxBytes: 23, ObservedAt: time.Now().UTC()}}
	report, err := traffic.NewRepository(runtime).ReportUsageBatch(ctx, tokenHash, events, "11111111-1111-4111-8111-111111111111", 1)
	if err != nil || !report.OK {
		t.Fatal("runtime checkpoint/traffic/rollup", err)
	}
	if _, err = runtime.Exec(ctx, `DELETE FROM traffic_usage_events WHERE server_id=$1::uuid`, in.ServerID); err != nil {
		t.Fatal("runtime retention/rollup", err)
	}
	var total int64
	if err = runtime.QueryRow(ctx, `SELECT COALESCE(sum(total_bytes),0)::bigint FROM traffic_usage_daily WHERE server_id=$1::uuid`, in.ServerID).Scan(&total); err != nil || total != 0 {
		t.Fatal("retention did not reconcile daily usage", err)
	}
	for _, statement := range []string{
		`UPDATE vpn_credential_revocation_events SET actor=actor`,
		`DELETE FROM vpn_credential_revocation_events`,
		`UPDATE vpn_credential_removal_dispatch_authorizations SET operation_id=operation_id`,
		`DELETE FROM vpn_credential_removal_dispatch_stages`,
		`INSERT INTO vpn_runtime_recovery_reservations DEFAULT VALUES`,
		`INSERT INTO vpn_runtime_pre_dispatch_checkpoints DEFAULT VALUES`,
		`UPDATE vpn_runtime_recovery_attempts SET state=state`,
		`UPDATE vpn_runtime_recovery_reservations SET requested_by=requested_by`,
	} {
		_, err = runtime.Exec(ctx, statement)
		var pgerr *pgconn.PgError
		if !errors.As(err, &pgerr) || pgerr.Code != "42501" {
			t.Fatalf("excess ordinary DML was not denied: %s: %v", statement, err)
		}
	}
	// Even with zero rows PostgreSQL checks row-lock privileges. The immutable
	// trigger remains responsible for refusing edits to the lock-only id column.
	if _, err = runtime.Exec(ctx, `SELECT id FROM vpn_runtime_recovery_attempts FOR SHARE;
  SELECT id FROM vpn_runtime_recovery_reservations FOR KEY SHARE`); err != nil {
		t.Fatal("narrow lock-column grant is insufficient", err)
	}

	refused := func(reason string) {
		t.Helper()
		if err := ProvisionRuntimeRole(ctx, owner, rejectedRole); err == nil {
			t.Fatal("provisioning accepted " + reason)
		}
		var exists bool
		if err := owner.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_roles WHERE rolname=$1)`, rejectedRole).Scan(&exists); err != nil || exists {
			t.Fatal("failed provisioning left a role", err)
		}
	}
	if _, err = owner.Exec(ctx, `CREATE TABLE public.rg140_policy_unknown(id integer)`); err != nil {
		t.Fatal(err)
	}
	refused("unknown public table")
	if _, err = owner.Exec(ctx, `DROP TABLE public.rg140_policy_unknown`); err != nil {
		t.Fatal(err)
	}
	if _, err = owner.Exec(ctx, `GRANT UPDATE(actor) ON public.vpn_credential_revocation_events TO PUBLIC`); err != nil {
		t.Fatal(err)
	}
	refused("PUBLIC write on immutable event column")
	if _, err = owner.Exec(ctx, `REVOKE UPDATE(actor) ON public.vpn_credential_revocation_events FROM PUBLIC`); err != nil {
		t.Fatal(err)
	}
	if _, err = owner.Exec(ctx, `INSERT INTO public.schema_migrations(version) VALUES ('000175_credential_removal_dispatch_stages')`); err != nil {
		t.Fatal(err)
	}
	refused("historical schema 175")
	if _, err = owner.Exec(ctx, `DELETE FROM public.schema_migrations WHERE version='000175_credential_removal_dispatch_stages'`); err != nil {
		t.Fatal(err)
	}
	if err = CheckExternalMigrationRuntimeRole(ctx, runtime); err != nil {
		t.Fatal("runtime authority changed during policy rehearsal", err)
	}
}
