// Package recoveryauth defines offline-only, narrowly scoped authorization
// prerequisites for RG-140. Nothing here releases a runtime mutation fence.
package recoveryauth

import (
    "crypto/rand"
    "crypto/sha256"
    "crypto/subtle"
    "encoding/base64"
    "encoding/hex"
    "encoding/json"
    "errors"
    "io"
    "regexp"
    "time"

    "github.com/ikaevus/routegate/backend/internal/auth"
)

const (
    challengeBytes = 32
    challengeLifetime = 90 * time.Second
    challengeDomain = "routegate/rg140/runtime-recovery/challenge/v1"
)

var (
    ErrAuthorizationRefused = errors.New("runtime recovery authorization prerequisites not met")
    canonicalID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
    canonicalSHA256 = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// Reservation is the immutable Manager-side evidence identity from migration
// 000162, not proof of current Agent runtime or credential revocation.
type Reservation struct {
    ID                        string `json:"reservationId"`
    ServerID                  string `json:"serverId"`
    AgentID                   string `json:"agentId"`
    JobID                     string `json:"jobId"`
    JobKind                   string `json:"jobKind"`
    AgentCredentialGeneration int64  `json:"agentCredentialGeneration"`
    MutationIntentSHA256      string `json:"mutationIntentSha256"`
    ResultReceiptSHA256       string `json:"resultReceiptSha256"`
    RuntimeWitnessSHA256      string `json:"runtimeWitnessSha256"`
}

func (r Reservation) valid() bool {
    return canonicalID.MatchString(r.ID) &&
        canonicalID.MatchString(r.ServerID) &&
        canonicalID.MatchString(r.AgentID) &&
        canonicalID.MatchString(r.JobID) &&
        (r.JobKind == "config_apply" || r.JobKind == "vpn_core_service") &&
        r.AgentCredentialGeneration > 0 &&
        canonicalSHA256.MatchString(r.MutationIntentSHA256) &&
        canonicalSHA256.MatchString(r.ResultReceiptSHA256) &&
        canonicalSHA256.MatchString(r.RuntimeWitnessSHA256)
}

// Commitment deliberately does not contain the cleartext challenge.
// This value can eventually be persisted, but by itself does NOT provide
// one-time semantics. Only a future transactional database consume can do so.
type Commitment struct {
    SchemaVersion int         `json:"schemaVersion"`
    Reservation   Reservation `json:"reservation"`
    OperatorID    string      `json:"operatorId"`
    SessionID     string      `json:"sessionId"`
    IssuedAt      time.Time   `json:"issuedAt"`
    ExpiresAt     time.Time   `json:"expiresAt"`
    ContextSHA256 string      `json:"contextSha256"`
    ChallengeSHA256 string    `json:"challengeSha256"`
}

type bindingContext struct {
    SchemaVersion int         `json:"schemaVersion"`
    Purpose       string      `json:"purpose"`
    Reservation   Reservation `json:"reservation"`
    OperatorID    string      `json:"operatorId"`
    SessionID     string      `json:"sessionId"`
    IssuedUnix    int64       `json:"issuedUnix"`
    ExpiresUnix   int64       `json:"expiresUnix"`
}

func bindingDigest(c Commitment) ([32]byte, error) {
    data, err := json.Marshal(bindingContext{
        SchemaVersion: c.SchemaVersion,
        Purpose: challengeDomain,
        Reservation: c.Reservation,
        OperatorID: c.OperatorID,
        SessionID: c.SessionID,
        IssuedUnix: c.IssuedAt.Unix(),
        ExpiresUnix: c.ExpiresAt.Unix(),
    })
    if err != nil {
        return [32]byte{}, err
    }
    return sha256.Sum256(data), nil
}

func tokenDigest(contextHash [32]byte, token []byte) [32]byte {
    h := sha256.New()
    _, _ = h.Write([]byte(challengeDomain))
    _, _ = h.Write([]byte{0})
    _, _ = h.Write(contextHash[:])
    _, _ = h.Write([]byte{0})
    _, _ = h.Write(token)
    var sum [32]byte
    copy(sum[:], h.Sum(nil))
    return sum
}

// BuildChallenge prepares an unissued challenge in memory. A future service
// MUST transactionally persist its commitment and reauthorize the live human
// session BEFORE returning the plaintext token. No such service exists yet.
func BuildChallenge(operator auth.AuthenticatedUser, reservation Reservation, now time.Time) (string, Commitment, error) {
    return buildChallengeWithEntropy(operator, reservation, now, rand.Reader)
}

func buildChallengeWithEntropy(operator auth.AuthenticatedUser, reservation Reservation, now time.Time, entropy io.Reader) (string, Commitment, error) {
    if !auth.CanAuthorizeRuntimeRecovery(operator) ||
        !canonicalID.MatchString(operator.ID) ||
        !canonicalID.MatchString(operator.SessionID) ||
        !reservation.valid() || now.IsZero() || entropy == nil {
        return "", Commitment{}, ErrAuthorizationRefused
    }
    issued := now.UTC().Truncate(time.Second)
    c := Commitment{
        SchemaVersion: 1,
        Reservation: reservation,
        OperatorID: operator.ID,
        SessionID: operator.SessionID,
        IssuedAt: issued,
        ExpiresAt: issued.Add(challengeLifetime),
    }
    digest, err := bindingDigest(c)
    if err != nil {
        return "", Commitment{}, ErrAuthorizationRefused
    }
    secret := make([]byte, challengeBytes)
    if _, err := io.ReadFull(entropy, secret); err != nil {
        return "", Commitment{}, ErrAuthorizationRefused
    }
    challengeHash := tokenDigest(digest, secret)
    c.ContextSHA256 = hex.EncodeToString(digest[:])
    c.ChallengeSHA256 = hex.EncodeToString(challengeHash[:])
    return base64.RawURLEncoding.EncodeToString(secret), c, nil
}

// MatchesBoundChallenge verifies only the cryptographic/identity/time
// prerequisite. It CANNOT authorize an unlock or consume a challenge.
// A future DB transaction MUST prove this challenge has never been used,
// require the current permission/session, and atomically mark it consumed.
func MatchesBoundChallenge(token string, c Commitment, operator auth.AuthenticatedUser, current Reservation, now time.Time) bool {
    if !auth.CanAuthorizeRuntimeRecovery(operator) ||
        c.SchemaVersion != 1 || !c.Reservation.valid() || !current.valid() ||
        c.Reservation != current || c.OperatorID != operator.ID ||
        c.SessionID != operator.SessionID ||
        !canonicalSHA256.MatchString(c.ContextSHA256) ||
        !canonicalSHA256.MatchString(c.ChallengeSHA256) ||
        c.IssuedAt.IsZero() || now.Before(c.IssuedAt) ||
        !now.Before(c.ExpiresAt) ||
        c.ExpiresAt.Sub(c.IssuedAt) != challengeLifetime {
        return false
    }
    provided, err := base64.RawURLEncoding.DecodeString(token)
    if err != nil || len(provided) != challengeBytes ||
        base64.RawURLEncoding.EncodeToString(provided) != token {
        return false
    }
    digest, err := bindingDigest(c)
    if err != nil {
        return false
    }
    contextHash, err := hex.DecodeString(c.ContextSHA256)
    if err != nil || subtle.ConstantTimeCompare(digest[:], contextHash) != 1 {
        return false
    }
    expected := tokenDigest(digest, provided)
    stored, err := hex.DecodeString(c.ChallengeSHA256)
    return err == nil && subtle.ConstantTimeCompare(expected[:], stored) == 1
}
