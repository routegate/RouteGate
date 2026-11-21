package revocations

import (
	"context"
	"testing"

	"github.com/ikaevus/routegate/backend/internal/auth"
)

type fakePreparationReservation struct {
	prepared int
	cancelled int
	got PrepareInput
	actor string
}

func (f *fakePreparationReservation) Prepare(_ context.Context, in PrepareInput) (Preparation, error) {
	f.prepared++
	f.got = in
	return Preparation{ID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", RequestedBy: in.Actor, State: "prepared"}, nil
}
func (f *fakePreparationReservation) Cancel(_ context.Context, operationID, actor string) (Preparation, error) {
	f.cancelled++
	f.actor = actor
	return Preparation{ID: operationID, RequestedBy: actor, State: "cancelled"}, nil
}

const (
	reviewUserID = "11111111-1111-4111-8111-111111111111"
	reviewSessionID = "22222222-2222-4222-8222-222222222222"
	reviewOperationID = "33333333-3333-4333-8333-333333333333"
)

func authorizedReviewUser() auth.AuthenticatedUser {
	return auth.AuthenticatedUser{
		UserProfile: auth.UserProfile{
			ID: reviewUserID,
			UserType: auth.UserTypeHuman,
			Status: "active",
			Roles: []string{auth.RoleSuperAdmin},
			Permissions: []string{"system:manage", "configs:apply", "vpn_users:disable"},
		},
		SessionID: reviewSessionID,
	}
}

func TestDormantPreparationAuthorizationFailsBeforeLedger(t *testing.T) {
	base := authorizedReviewUser()
	input := PrepareInput{AccountID: reviewOperationID, ServerID: reviewOperationID}
	cases := []struct {
		name string
		user auth.AuthenticatedUser
		anonymous bool
		ack bool
		actor string
	}{
		{name: "anonymous", anonymous: true, ack: true},
		{name: "no explicit restart acknowledgement", user: base},
		{name: "request-supplied actor", user: base, ack: true, actor: "another-operator"},
		{name: "not active", user: func() auth.AuthenticatedUser { u:=base; u.Status="disabled"; return u }(), ack: true},
		{name: "service identity", user: func() auth.AuthenticatedUser { u:=base; u.UserType="agent"; return u }(), ack: true},
		{name: "operator cannot impersonate super admin", user: func() auth.AuthenticatedUser { u:=base; u.Roles=[]string{auth.RoleOperator}; return u }(), ack: true},
		{name: "missing system permission", user: func() auth.AuthenticatedUser { u:=base; u.Permissions=[]string{"configs:apply","vpn_users:disable"}; return u }(), ack: true},
		{name: "missing apply permission", user: func() auth.AuthenticatedUser { u:=base; u.Permissions=[]string{"system:manage","vpn_users:disable"}; return u }(), ack: true},
		{name: "missing disable permission", user: func() auth.AuthenticatedUser { u:=base; u.Permissions=[]string{"system:manage","configs:apply"}; return u }(), ack: true},
		{name: "missing session", user: func() auth.AuthenticatedUser { u:=base; u.SessionID=""; return u }(), ack: true},
		{name: "malformed session", user: func() auth.AuthenticatedUser { u:=base; u.SessionID="browser-user-supplied"; return u }(), ack: true},
		{name: "malformed actor ID", user: func() auth.AuthenticatedUser { u:=base; u.ID="attacker"; return u }(), ack: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakePreparationReservation{}
			a := &AuthorizedPreparation{reservation:f}
			ctx := context.Background()
			if !tc.anonymous {
				ctx = auth.ContextWithUser(ctx, tc.user)
			}
			in := input
			in.Actor = tc.actor
			result, err := a.Prepare(ctx, in, tc.ack)
			if err != ErrPreflight || result != (Preparation{}) || f.prepared != 0 {
				t.Fatalf("denied prepare reached ledger: state=%+v err=%v calls=%d", result, err, f.prepared)
			}
			cancel, cancelErr := a.Cancel(ctx, reviewOperationID)
			if tc.anonymous || tc.user.Status!="active" || tc.user.UserType!=auth.UserTypeHuman ||
				!auth.HasRole(tc.user.UserProfile,auth.RoleSuperAdmin) ||
				!auth.HasPermission(tc.user.UserProfile,"system:manage") ||
				!auth.HasPermission(tc.user.UserProfile,"configs:apply") ||
				!auth.HasPermission(tc.user.UserProfile,"vpn_users:disable") ||
				tc.user.SessionID=="" || tc.user.SessionID=="browser-user-supplied" ||
				tc.user.ID=="attacker" {
				if cancelErr != ErrPreflight || cancel != (Preparation{}) || f.cancelled != 0 {
					t.Fatalf("unauthorized cancel reached ledger: result=%+v err=%v calls=%d", cancel, cancelErr, f.cancelled)
				}
			}
		})
	}
}

func TestDormantPreparationPrincipalOwnsAuditIdentity(t *testing.T) {
	f := &fakePreparationReservation{}
	a := &AuthorizedPreparation{reservation:f}
	ctx := auth.ContextWithUser(context.Background(), authorizedReviewUser())
	in := PrepareInput{AccountID: reviewOperationID, ServerID: reviewOperationID, BaselineHash: "ignored-by-fake"}
	p, err := a.Prepare(ctx, in, true)
	if err != nil || f.prepared != 1 || f.got.Actor != reviewUserID || p.RequestedBy != reviewUserID {
		t.Fatalf("authenticated actor not bound to preparation: result=%+v err=%v input=%+v", p, err, f.got)
	}
	c, err := a.Cancel(ctx, p.ID)
	if err != nil || f.cancelled != 1 || f.actor != reviewUserID || c.State != "cancelled" {
		t.Fatalf("authenticated actor not bound to cancel: result=%+v err=%v", c, err)
	}
	denied, err := a.Cancel(ctx, "not-a-uuid")
	if err != ErrPreflight || denied != (Preparation{}) || f.cancelled != 1 {
		t.Fatal("invalid operation ID reached cancellation")
	}
	var missing *AuthorizedPreparation
	if _, err := missing.Prepare(ctx, in, true); err != ErrPreflight {
		t.Fatal("nil adapter accepted preparation")
	}
}
