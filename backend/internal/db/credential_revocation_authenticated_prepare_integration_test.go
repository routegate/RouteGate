package db

import (
	"testing"

	"github.com/ikaevus/routegate/backend/internal/auth"
	"github.com/ikaevus/routegate/backend/internal/revocations"
)

// Authorization is an internal review gate only: there is no registered
// HTTP route or Agent dispatch. Test the real schema-161 ledger and existing
// Service.Prepare, not only a mock permission check.
func TestRevocationPreparationUsesAuthenticatedManagerActor(t *testing.T) {
	ctx, pool, input, _, done := removalPreparationFixture(t)
	defer done()

	input.Actor = "" // callers cannot supply their own audit identity.
	first := auth.AuthenticatedUser{UserProfile: auth.UserProfile{
		ID: "11111111-1111-4111-8111-111111111111",
		UserType: auth.UserTypeHuman, Status: "active",
		Roles: []string{auth.RoleSuperAdmin},
		Permissions: []string{"system:manage", "configs:apply", "vpn_users:disable"},
	}, SessionID: "22222222-2222-4222-8222-222222222222"}
	authorized := revocations.NewAuthorizedPreparation(revocations.NewService(pool))
	firstCtx := auth.ContextWithUser(ctx, first)

	// No explicit shared-process impact acknowledgement can reserve a node.
	if _, err := authorized.Prepare(firstCtx, input, false); err != revocations.ErrPreflight {
		t.Fatalf("missing acknowledgement did not fail closed: %v", err)
	}
	var reservations int
	if err := pool.QueryRow(ctx,"SELECT count(*) FROM vpn_credential_revocations").Scan(&reservations); err != nil || reservations != 0 {
		t.Fatalf("rejected operation mutated ledger: count=%d err=%v", reservations, err)
	}

	prepared, err := authorized.Prepare(firstCtx, input, true)
	if err != nil || prepared.RequestedBy != first.ID || prepared.State != "prepared" {
		t.Fatalf("authorized preparation failed: %+v err=%v", prepared, err)
	}
	same, err := authorized.Prepare(firstCtx, input, true)
	if err != nil || same.ID != prepared.ID {
		t.Fatalf("same-session retry created another reservation: %+v err=%v", same, err)
	}

	second := first
	second.ID = "33333333-3333-4333-8333-333333333333"
	second.SessionID = "44444444-4444-4444-8444-444444444444"
	secondCtx := auth.ContextWithUser(ctx, second)
	if _, err := authorized.Prepare(secondCtx, input, true); err != revocations.ErrPreflight {
		t.Fatalf("another principal captured an already prepared operation: %v", err)
	}

	var auditActor string
	if err := pool.QueryRow(ctx,`
		SELECT requested_by FROM vpn_credential_revocations WHERE id=$1::uuid
	`, prepared.ID).Scan(&auditActor); err != nil || auditActor != first.ID {
		t.Fatalf("durable audit actor was not bound to session: actor=%q err=%v", auditActor, err)
	}
	if _, err := authorized.Cancel(secondCtx, prepared.ID); err != nil {
		t.Fatalf("separate authorized super-admin cannot cancel a never-dispatched intent: %v", err)
	}
	var state, lastActor string
	if err := pool.QueryRow(ctx,`
		SELECT state,last_action_by FROM vpn_credential_revocations WHERE id=$1::uuid
	`, prepared.ID).Scan(&state, &lastActor); err != nil || state != "cancelled" || lastActor != second.ID {
		t.Fatalf("cancellation audit was not durable: state=%q actor=%q err=%v", state, lastActor, err)
	}
	var events, agentJobs int
	if err := pool.QueryRow(ctx,`
		SELECT
		  (SELECT count(*) FROM vpn_credential_revocation_events WHERE operation_id=$1::uuid),
		  (SELECT count(*) FROM agent_operation_jobs WHERE server_id=$2::uuid)
	`, prepared.ID, input.ServerID).Scan(&events, &agentJobs); err != nil || events != 2 || agentJobs != 0 {
		t.Fatalf("authorized intent created dispatch or incorrect audit: events=%d jobs=%d err=%v", events, agentJobs, err)
	}
}
