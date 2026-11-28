package recoveryenroll

import (
	"context"
	"errors"
	"regexp"
	"time"

	"github.com/ikaevus/routegate/backend/internal/auth"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Prepare is an internal, dormant Manager administrative primitive. There
// is NO HTTP handler, route, Agent polling/Runner integration, installer flag,
// enrollment activation, job admission change, or runtime fence release.
//
// The only written phase is "prepared_unattested". Explicit preparation
// NEVER means that an Agent has been enrolled or a journal verified.
var ErrEnrollmentPreparationRefused = errors.New("recovery enrollment preparation refused")

var enrollmentCanonicalUUID = regexp.MustCompile(
	`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`,
)

const PhasePreparedUnattested = "prepared_unattested"

type Preparation struct {
	ID                        string
	AgentID                   string
	ServerID                  string
	AgentCredentialGeneration int64
	Phase                     string
	PreparedAt                time.Time
}

// Preparer uses existing schema 167 and the existing PostgreSQL human
// session/role/permission tables. It introduces no new migration or grants.
type Preparer struct{ pool *pgxpool.Pool }

func NewPreparer(pool *pgxpool.Pool) *Preparer { return &Preparer{pool: pool} }

// Prepare explicitly selects ONE existing Agent and its expected Manager
// credential generation. It requires a fresh super_admin session with
// runtime_recovery:enrollment_prepare, independently revalidated under
// PostgreSQL row locks. It then writes an immutable preparation and a
// conventional attributed audit_events entry in ONE DB transaction. Schema
// 167 also writes its immutable "prepared_unattested" audit in that same
// transaction, and vetoes busy/held/not-eligible nodes.
//
// A role/session revoke or Agent rotation cannot overtake the locked
// authorization during the commit. The normal audit_events entry records
// operator identity, but is NOT a cryptographic/privileged-DB-proof ledger.
// Neither result grants permission to dispatch, mutate VPN, or unlock.
//
// A network/commit ambiguity returns an error with NO preparation receipt.
// Do not retry as "idempotent" or silently re-enroll: UNIQUE(agent_id/server_id)
// and immutable history require explicit independent review.
func (p *Preparer) Prepare(
	ctx context.Context,
	agentID string,
	expectedGeneration int64,
	operator auth.AuthenticatedUser,
) (Preparation, error) {
	var empty Preparation
	refuse := func() (Preparation, error) { return empty, ErrEnrollmentPreparationRefused }
	if p == nil || p.pool == nil || ctx == nil || ctx.Err() != nil ||
		!enrollmentCanonicalUUID.MatchString(agentID) ||
		!enrollmentCanonicalUUID.MatchString(operator.ID) ||
		!enrollmentCanonicalUUID.MatchString(operator.SessionID) ||
		expectedGeneration <= 0 ||
		!auth.CanPrepareRuntimeRecoveryEnrollment(operator) {
		return refuse()
	}

	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil { return refuse() }
	defer tx.Rollback(ctx)

	// The caller's roles and permissions are never trusted as DB truth.
	// This SELECT rechecks the LIVE session and grant; holding these share
	// locks until COMMIT serializes concurrent session revoke, user disable,
	// role revocation and permission removal.
	var authorizedSession string
	err = tx.QueryRow(ctx, `
SELECT s.id::text
  FROM auth_sessions s
  JOIN users u ON u.id=s.user_id
  JOIN user_roles ur ON ur.user_id=u.id
  JOIN roles r ON r.id=ur.role_id
  JOIN role_permissions rp ON rp.role_id=r.id
  JOIN permissions perm ON perm.id=rp.permission_id
 WHERE s.id=$1::uuid AND s.user_id=$2::uuid
   AND s.revoked_at IS NULL AND s.expires_at>clock_timestamp()
   AND u.user_type='human' AND u.status='active'
   AND r.code='super_admin'
   AND perm.code='runtime_recovery:enrollment_prepare'
 LIMIT 1
 FOR SHARE OF s,u,ur,r,rp,perm
`, operator.SessionID, operator.ID).Scan(&authorizedSession)
	if err != nil || authorizedSession != operator.SessionID { return refuse() }

	// This lock holds Agent generation and exact node identity stable up to
	// COMMIT. It agrees with migration 167's own Agent/server row locks.
	var currentGeneration int64
	var serverID string
	err = tx.QueryRow(ctx, `
SELECT a.server_id::text,a.credential_generation
 FROM agents a
 JOIN servers s ON s.id=a.server_id
 WHERE a.id=$1::uuid AND a.status<>'disabled'
   AND a.capabilities @> '{"runtimeMutationFencingV1":true}'::jsonb
 FOR UPDATE OF a,s
`, agentID).Scan(&serverID, &currentGeneration)
	if err != nil || serverID == "" || currentGeneration != expectedGeneration { return refuse() }

	var result Preparation
	err = tx.QueryRow(ctx, `
INSERT INTO vpn_runtime_recovery_enrollment_preparations(agent_id)
VALUES ($1::uuid)
RETURNING id::text,agent_id::text,server_id::text,
          agent_credential_generation,phase,prepared_at
`, agentID).Scan(
		&result.ID, &result.AgentID, &result.ServerID,
		&result.AgentCredentialGeneration, &result.Phase, &result.PreparedAt,
	)
	if err != nil || result.ServerID != serverID ||
		result.AgentID != agentID ||
		result.AgentCredentialGeneration != expectedGeneration ||
		result.Phase != PhasePreparedUnattested {
		return refuse()
	}

	// This secondary attributed audit must succeed or the new preparation
	// (including schema 167's immutable audit event) rolls back too.
	// Session ID is an identifier, NEVER a token or a secret.
	_, err = tx.Exec(ctx, `
INSERT INTO audit_events
 (actor_user_id,actor_type,action,resource_type,resource_id,result,metadata)
VALUES
 ($1::uuid,'user','runtime_recovery.enrollment_prepared_unattested',
  'agent',$2::uuid,'success',
  jsonb_build_object('session_id',$3::text,
                     'preparation_id',$4::text,
                     'credential_generation',$5::bigint,
                     'phase','prepared_unattested'))
`, operator.ID, agentID, operator.SessionID, result.ID, result.AgentCredentialGeneration)
	if err != nil { return refuse() }

	if err := tx.Commit(ctx); err != nil { return refuse() }
	return result, nil
}

// Kept separate for negative tests that inspect error classes; the external
// API only returns generic refusals to avoid disclosing Agent/operator state.
func isExpectedEnrollmentRefusal(err error) bool {
	if errors.Is(err, pgx.ErrNoRows) { return true }
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) &&
		(pgErr.Code == "23505" || pgErr.Code == "23503" ||
			pgErr.Code == "P0147" || pgErr.Code == "P0142")
}
