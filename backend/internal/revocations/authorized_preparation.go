package revocations

import (
	"context"

	"github.com/ikaevus/routegate/backend/internal/auth"
)

// preparationReservation is deliberately narrow: the authorization adapter
// may only stage/cancel a dormant ledger reservation. It has no method to
// dispatch Agent work, update a config version, or change account status.
type preparationReservation interface {
	Prepare(context.Context, PrepareInput) (Preparation, error)
	Cancel(context.Context, string, string) (Preparation, error)
}

// AuthorizedPreparation is an internal API boundary, not an HTTP handler.
// The caller MUST pass through auth.Middleware (session token revalidation)
// before supplying a request context. A future execution/confirmation API must
// separately reauthenticate and reauthorize; this object never dispatches.
type AuthorizedPreparation struct {
	reservation preparationReservation
}

func NewAuthorizedPreparation(service *Service) *AuthorizedPreparation {
	return &AuthorizedPreparation{reservation: service}
}

// authenticatedRevocationActor rejects service identities, disabled users,
// read-only/operator roles, empty sessions and callers without the full
// permission set. Never accept an actor ID from a browser request body.
func authenticatedRevocationActor(ctx context.Context) (string, error) {
	user, ok := auth.UserFromContext(ctx)
	if !ok || !evidenceID.MatchString(user.ID) ||
		!evidenceID.MatchString(user.SessionID) ||
		user.UserType != auth.UserTypeHuman || user.Status != "active" ||
		!auth.HasRole(user.UserProfile, auth.RoleSuperAdmin) ||
		!auth.HasPermission(user.UserProfile, "system:manage") ||
		!auth.HasPermission(user.UserProfile, "configs:apply") ||
		!auth.HasPermission(user.UserProfile, "vpn_users:disable") {
		return "", ErrPreflight
	}
	return user.ID, nil
}

// Prepare takes an explicit acknowledgement that future execution would
// restart a shared sing-box process and terminate unrelated VPN sessions.
// Acknowledgement is NOT execution approval: the future dispatch stage must
// obtain a fresh, separately authenticated confirmation tied to an unchanged
// preview and exact persisted operation, and independently recheck freshness.
//
// Actor is always sourced from the authenticated Manager session, never
// client input. Even an explicitly supplied Actor is refused as ambiguous.
func (a *AuthorizedPreparation) Prepare(ctx context.Context, in PrepareInput, sharedProcessRestartAcknowledged bool) (Preparation, error) {
	if !sharedProcessRestartAcknowledged || in.Actor != "" || a == nil || a.reservation == nil {
		return Preparation{}, ErrPreflight
	}
	actor, err := authenticatedRevocationActor(ctx)
	if err != nil {
		return Preparation{}, ErrPreflight
	}
	in.Actor = actor
	return a.reservation.Prepare(ctx, in)
}

// Cancel can only cancel a never-dispatched, prepared reservation. Its actor
// is the authenticated super-admin's user ID; a different super-admin may
// cancel the reservation and remains visible as last_action_by in the ledger.
func (a *AuthorizedPreparation) Cancel(ctx context.Context, operationID string) (Preparation, error) {
	if a == nil || a.reservation == nil || !evidenceID.MatchString(operationID) {
		return Preparation{}, ErrPreflight
	}
	actor, err := authenticatedRevocationActor(ctx)
	if err != nil {
		return Preparation{}, ErrPreflight
	}
	return a.reservation.Cancel(ctx, operationID, actor)
}
