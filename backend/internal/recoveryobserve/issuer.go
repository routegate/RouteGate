package recoveryobserve

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrObservationIssueRefused intentionally does not distinguish an unknown
// attempt, wrong Agent token, rotated generation, missing fence or replay.
var ErrObservationIssueRefused = errors.New("recovery observation issuance refused")

// ObservationIssuer is an internal, off-by-default Manager primitive.
// It has no HTTP route and cannot initiate a task, restart sing-box or
// release Agent mutation-inflight.json. A future caller MUST source the
// token from existing authenticated Agent bearer middleware.
type ObservationIssuer struct{ pool *pgxpool.Pool }

func NewObservationIssuer(pool *pgxpool.Pool) *ObservationIssuer {
	return &ObservationIssuer{pool: pool}
}

// IssuedObservationNonce contains a raw 256-bit secret which is returned
// ONCE, after its SHA-256 and mandatory issued audit have COMMITTED.
// Manager persists only the hash. Request evidence is explicitly a
// token-holder's claim, NOT independently attested on-disk file provenance.
type IssuedObservationNonce struct {
	SchemaVersion             int               `json:"schemaVersion"`
	ObservationID             string            `json:"observationId"`
	ReservationID             string            `json:"reservationId"`
	AttemptID                 string            `json:"attemptId"`
	AttemptEpoch              int64             `json:"attemptEpoch"`
	Nonce                     string            `json:"nonce"`
	IssuedAt                  time.Time         `json:"issuedAt"`
	ExpiresAt                 time.Time         `json:"expiresAt"`
	TaskID                    string            `json:"taskId"`
	AgentID                   string            `json:"agentId"`
	ServerID                  string            `json:"serverId"`
	AgentCredentialGeneration int64             `json:"agentCredentialGeneration"`
	ExpectedEvidence          EvidenceDigestsV1 `json:"expectedEvidence"`
}

const observationNonceTTL = 45 * time.Second

func validObservationIssueClaim(e EvidenceDigestsV1) bool {
	return e.SchemaVersion == 1 &&
		canonicalUUID.MatchString(e.TaskID) &&
		canonicalUUID.MatchString(e.AgentID) &&
		canonicalUUID.MatchString(e.ServerID) &&
		canonicalHash.MatchString(e.MarkerSHA256) &&
		canonicalHash.MatchString(e.ReceiptSHA256) &&
		canonicalHash.MatchString(e.EnvelopeSHA256) &&
		canonicalHash.MatchString(e.WitnessSHA256)
}

func refuseObservationIssue(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrObservationIssueRefused
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23503", "23505", "23514", "P0145":
			return ErrObservationIssueRefused
		}
	}
	return err
}

// IssueFromAuthenticatedAgentClaim creates exactly one short-lived nonce for
// a *held* recovery attempt. The Agent token is checked against current DB
// identity/generation and its fencing capability in the same transaction
// that inserts the nonce and appends the mandatory audit event.
//
// This function deliberately accepts digest CLAIMS from the current Agent
// bearer holder. A stolen token or compromised Agent can invent hashes.
// It MUST NOT be called a trusted evidence-enrollment API, remote
// attestation or a basis for unlock. It is not network-wired today.
//
// A lost/ambiguous commit response returns zero credentials. Since there is
// at most one nonce EVER per attempt, this may strand recovery permanently
// until a separately reviewed cancellation process exists. Do not retry
// by overwriting existing nonce material or clearing the runtime fence.
func (i *ObservationIssuer) IssueFromAuthenticatedAgentClaim(
	ctx context.Context, agentToken, attemptID string, claimed EvidenceDigestsV1,
) (IssuedObservationNonce, error) {
	var empty IssuedObservationNonce
	if i == nil || i.pool == nil || ctx.Err() != nil ||
		!validAgentBearerV1(agentToken) ||
		!canonicalUUID.MatchString(attemptID) ||
		!validObservationIssueClaim(claimed) {
		return empty, ErrObservationIssueRefused
	}
	tokenHash := sha256.Sum256([]byte(agentToken))
	tx, err := i.pool.Begin(ctx)
	if err != nil {
		return empty, fmt.Errorf("begin private observation issuance: %w", err)
	}
	defer tx.Rollback(ctx)

	// A bearer token never determines a target node by an untrusted header.
	// All reservation/attempt identity and Agent generation come from the
	// locked database rows. Agent identity cannot rotate until commit.
	var held struct {
		reservationID, agentID, serverID, taskID string
		attemptEpoch, attemptGeneration, currentGeneration int64
		fenced bool
	}
	err = tx.QueryRow(ctx, `
SELECT p.reservation_id::text,p.agent_id::text,p.server_id::text,
       p.job_id::text,p.attempt_epoch,p.agent_credential_generation,
       a.credential_generation,
       a.capabilities @> '{"runtimeMutationFencingV1":true}'::jsonb
  FROM vpn_runtime_recovery_attempts p
  JOIN vpn_runtime_recovery_reservations r
    ON r.id=p.reservation_id AND r.agent_id=p.agent_id
    AND r.server_id=p.server_id AND r.job_id=p.job_id
    AND r.agent_credential_generation=p.agent_credential_generation
  JOIN agents a ON a.id=p.agent_id AND a.server_id=p.server_id
 WHERE p.id=$1::uuid AND p.state='awaiting_fresh_observation'
   AND a.token_hash=$2
 FOR SHARE OF a
 FOR KEY SHARE OF p,r
`, attemptID, hex.EncodeToString(tokenHash[:])).Scan(
		&held.reservationID,&held.agentID,&held.serverID,&held.taskID,
		&held.attemptEpoch,&held.attemptGeneration,&held.currentGeneration,
		&held.fenced,
	)
	if err != nil {
		return empty, refuseObservationIssue(err)
	}
	if !held.fenced || held.attemptGeneration <= 0 ||
		held.attemptGeneration != held.currentGeneration ||
		claimed.TaskID != held.taskID || claimed.AgentID != held.agentID ||
		claimed.ServerID != held.serverID {
		return empty, ErrObservationIssueRefused
	}

	// Generate the only raw nonce after validating the held identity.
	// The secret is never logged or written to SQL.
	rawNonce := make([]byte, 32)
	if _, err := rand.Read(rawNonce); err != nil {
		return empty, fmt.Errorf("generate observation nonce: %w", err)
	}
	nonceHash := sha256.Sum256(rawNonce)
	var id string
	var issuedAt, expiresAt time.Time
	err = tx.QueryRow(ctx, `
WITH issued AS (SELECT clock_timestamp() AS issued_at)
INSERT INTO vpn_runtime_recovery_observation_nonces (
 attempt_id,reservation_id,server_id,agent_id,task_id,attempt_epoch,
 agent_credential_generation,expected_marker_sha256,expected_receipt_sha256,
 expected_runtime_witness_sha256,nonce_sha256,issued_at,expires_at
)
SELECT $1::uuid,$2::uuid,$3::uuid,$4::uuid,$5::uuid,$6,$7,
       $8,$9,$10,$11,issued.issued_at,
       issued.issued_at + interval '45 seconds'
  FROM issued
RETURNING id::text,issued_at,expires_at
`,attemptID,held.reservationID,held.serverID,held.agentID,held.taskID,
		held.attemptEpoch,held.attemptGeneration,claimed.MarkerSHA256,
		claimed.ReceiptSHA256,claimed.WitnessSHA256,
		hex.EncodeToString(nonceHash[:]),
	).Scan(&id,&issuedAt,&expiresAt)
	if err != nil {
		return empty, refuseObservationIssue(err)
	}
	// This also commits 000165's mandatory "issued" audit event.
	if err := tx.Commit(ctx); err != nil {
		return empty, fmt.Errorf("commit observation issuance: %w", err)
	}
	return IssuedObservationNonce{
		SchemaVersion:1,ObservationID:id,ReservationID:held.reservationID,
		AttemptID:attemptID,AttemptEpoch:held.attemptEpoch,
		Nonce:base64.RawURLEncoding.EncodeToString(rawNonce),
		IssuedAt:issuedAt.UTC(),ExpiresAt:expiresAt.UTC(),
		TaskID:held.taskID,AgentID:held.agentID,ServerID:held.serverID,
		AgentCredentialGeneration:held.attemptGeneration,
		ExpectedEvidence:claimed,
	},nil
}
