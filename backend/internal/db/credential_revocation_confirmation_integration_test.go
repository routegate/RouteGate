package db

import (
	"errors"
	"os"
	"testing"

	"github.com/ikaevus/routegate/backend/internal/auth"
	"github.com/ikaevus/routegate/backend/internal/revocations"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestCredentialRevocationConfirmationIsImmutableAndNotDispatched(t *testing.T) {
	ctx, pool, input, _, done := removalPreparationFixture(t)
	defer done()

	user := auth.AuthenticatedUser{UserProfile: auth.UserProfile{
		ID: "11111111-1111-4111-8111-111111111111",
		UserType: auth.UserTypeHuman, Status: "active",
		Roles: []string{auth.RoleSuperAdmin},
		Permissions: []string{"system:manage", "configs:apply", "vpn_users:disable"},
	}, SessionID: "22222222-2222-4222-8222-222222222222"}
	userCtx := auth.ContextWithUser(ctx, user)
	input.Actor = ""
	prepared, err := revocations.NewAuthorizedPreparation(revocations.NewService(pool)).Prepare(userCtx, input, true)
	if err != nil {
		t.Fatalf("prepare existing account: %v", err)
	}
	var candidateHash string
	if err := pool.QueryRow(ctx,
		"SELECT candidate_hash FROM vpn_credential_revocations WHERE id=$1::uuid", prepared.ID).Scan(&candidateHash); err != nil {
		t.Fatal(err)
	}
	svc := revocations.NewAuthorizedConfirmation(pool)
	confirmInput := revocations.ConfirmInput{
		OperationID: prepared.ID,
		ExpectedCandidateHash: candidateHash,
		SharedProcessRestartAcknowledged: true,
	}
	for name, makeBad := range map[string]func(*revocations.ConfirmInput){
		"no acknowledgement": func(i *revocations.ConfirmInput) { i.SharedProcessRestartAcknowledged = false },
		"wrong preview digest": func(i *revocations.ConfirmInput) { i.ExpectedCandidateHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" },
		"malformed operation": func(i *revocations.ConfirmInput) { i.OperationID = "not-a-uuid" },
	} {
		t.Run(name, func(t *testing.T) {
			input := confirmInput
			makeBad(&input)
			if result, err := svc.Confirm(userCtx, input); err != revocations.ErrPreflight || result != (revocations.Confirmation{}) {
				t.Fatalf("unsafe confirmation accepted: %+v error=%v", result, err)
			}
		})
	}
	second := user
	second.ID = "33333333-3333-4333-8333-333333333333"
	second.SessionID = "44444444-4444-4444-8444-444444444444"
	if result, err := svc.Confirm(auth.ContextWithUser(ctx, second), confirmInput); err != revocations.ErrPreflight || result != (revocations.Confirmation{}) {
		t.Fatalf("another administrator assumed initial operator's confirmation: %+v, %v", result, err)
	}
	var noConfirmation int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM vpn_credential_revocation_confirmations").Scan(&noConfirmation); err != nil || noConfirmation != 0 {
		t.Fatalf("rejected calls persisted confirmation: %d, %v", noConfirmation, err)
	}
	first, err := svc.Confirm(userCtx, confirmInput)
	if err != nil || first.OperationID != prepared.ID || first.ConfirmedBy != user.ID || first.ConfirmedAt.IsZero() {
		t.Fatalf("confirmation failed: %+v, %v", first, err)
	}
	again, err := svc.Confirm(userCtx, confirmInput)
	if err != nil || again != first {
		t.Fatalf("retry altered one-time confirmation: %+v, %v", again, err)
	}
	var count, agentJobs int
	if err := pool.QueryRow(ctx, `
		SELECT
		  (SELECT count(*) FROM vpn_credential_revocation_confirmations),
		  (SELECT count(*) FROM agent_operation_jobs WHERE server_id=$1::uuid)
	`, input.ServerID).Scan(&count, &agentJobs); err != nil || count != 1 || agentJobs != 0 {
		t.Fatalf("duplicate confirmation or unexpected Agent dispatch: confirmations=%d jobs=%d err=%v", count, agentJobs, err)
	}

	// Even direct SQL may not forge the baseline/Agent snapshot, alter an
	// existing confirmation, delete it, or overwrite the audit timestamp.
	rejected := []struct { name, sql string; args []any }{
		{"forged snapshot", `INSERT INTO vpn_credential_revocation_confirmations
		 (operation_id,confirmed_by,baseline_version_id,agent_id,agent_credential_generation,candidate_hash,candidate_runtime_hash,shared_restart_acknowledged)
		 SELECT id,requested_by,baseline_version_id,agent_id,agent_credential_generation,repeat('b',64),candidate_runtime_hash,true
		 FROM vpn_credential_revocations WHERE id=$1::uuid`, []any{prepared.ID}},
		{"alter operator", `UPDATE vpn_credential_revocation_confirmations SET confirmed_by=$2 WHERE operation_id=$1::uuid`, []any{prepared.ID, second.ID}},
		{"delete proof", `DELETE FROM vpn_credential_revocation_confirmations WHERE operation_id=$1::uuid`, []any{prepared.ID}},
	}
	for _, tc := range rejected {
		t.Run(tc.name, func(t *testing.T) {
			_, err := pool.Exec(ctx,tc.sql,tc.args...)
			var pgErr *pgconn.PgError
			if !errors.As(err,&pgErr) || pgErr.Code!="P0141" {
				t.Fatalf("confirmation proof mutated or wrong SQL rejection: %v",err)
			}
		})
	}

	// A cancellation AFTER confirmation is still permitted in schema 162,
	// but the durable confirmation cannot be used for a cancelled operation.
	if _, err := revocations.NewAuthorizedPreparation(revocations.NewService(pool)).Cancel(userCtx,prepared.ID); err != nil {
		t.Fatalf("undispatched preparation cancellation: %v",err)
	}
	if _, err := svc.Confirm(userCtx,confirmInput); err != revocations.ErrPreflight {
		t.Fatalf("cancelled operation was reconfirmed: %v",err)
	}
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM vpn_credential_revocation_confirmations").Scan(&count); err != nil || count!=1 {
		t.Fatalf("cancelled proof was lost: %d %v",count,err)
	}
	down, err := os.ReadFile("../../migrations/000162_credential_revocation_confirmations.down.sql")
	if err != nil {t.Fatal(err)}
	if _, err := pool.Exec(ctx,string(down)); err==nil {
		t.Fatal("downgrade discarded confirmation proof")
	}
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM vpn_credential_revocation_confirmations").Scan(&count); err != nil || count!=1 {
		t.Fatalf("failed downgrade partially discarded history: %d %v",count,err)
	}
}

// The exact SQL guard locks the parent first: when a parent cancellation
// commits before confirmation, a stale confirmation cannot be inserted.
func TestCredentialRevocationConfirmationRefusesCancelledParent(t *testing.T) {
	ctx, pool, input, _, done := removalPreparationFixture(t)
	defer done()
	input.Actor = "11111111-1111-4111-8111-111111111111"
	p, err := revocations.NewService(pool).Prepare(ctx,input)
	if err != nil { t.Fatal(err) }
	if _, err := revocations.NewService(pool).Cancel(ctx,p.ID,input.Actor); err != nil {t.Fatal(err)}
	_, err = pool.Exec(ctx,`
		INSERT INTO vpn_credential_revocation_confirmations
		(operation_id,confirmed_by,baseline_version_id,agent_id,agent_credential_generation,candidate_hash,candidate_runtime_hash,shared_restart_acknowledged)
		SELECT id,requested_by,baseline_version_id,agent_id,agent_credential_generation,candidate_hash,candidate_runtime_hash,true
		FROM vpn_credential_revocations WHERE id=$1::uuid
	`,p.ID)
	var pgErr *pgconn.PgError
	if !errors.As(err,&pgErr) || pgErr.Code!="P0141" {
		t.Fatalf("stale prepared intent was confirmed after cancellation: %v",err)
	}
}
