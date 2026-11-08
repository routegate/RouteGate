package db

import (
	"context"
	"crypto/ed25519"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ikaevus/routegate/backend/internal/auth"
	"github.com/ikaevus/routegate/backend/internal/revocations"
)

func testFreshRemovalRecoveryApproval(t *testing.T, f dispatchGateFixture, dispatch revocations.RemovalDispatchDecision, signer ed25519.PrivateKey, failure []byte) {
	t.Helper()
	user, _ := auth.UserFromContext(f.ctx)
	if err := f.pool.QueryRow(f.ctx, `INSERT INTO auth_sessions(user_id,token_hash,expires_at)
VALUES($1::uuid,'fresh-removal-recovery-subject',clock_timestamp()+interval '1 hour')
RETURNING id::text`, user.ID).Scan(&user.SessionID); err != nil {
		t.Fatal(err)
	}
	ctx := auth.ContextWithUser(f.ctx, user)
	if _, err := f.pool.Exec(ctx, `UPDATE agents SET last_authenticated_heartbeat_at=clock_timestamp() WHERE id=$1::uuid`, f.agent); err != nil {
		t.Fatal(err)
	}
	digest, err := revocations.RuntimeDigest(failure)
	if err != nil {
		t.Fatal(err)
	}
	var now time.Time
	if err := f.pool.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&now); err != nil {
		t.Fatal(err)
	}
	d := revocations.RemovalRecoveryDecision{
		Audience: dispatch.Audience, AttemptID: "ddeeff00-1122-4334-8556-778899aabbcc",
		OperationID: f.operation, JobID: dispatch.ApplyJobID, AccountID: dispatch.AccountID,
		ServerID: dispatch.ServerID, AgentID: dispatch.AgentID, AgentGeneration: dispatch.AgentGeneration,
		CandidateVersionID: dispatch.CandidateVersionID, CandidateRuntimeHash: dispatch.CandidateRuntimeHash,
		FailureReportDigest: digest, ActorID: user.ID, SessionID: user.SessionID,
		Nonce: strings.Repeat("b", 64), SharedRestartConsent: true,
		IssuedAtUnixMicros: now.UnixMicro(), ExpiresAtUnixMicros: now.Add(90 * time.Second).UnixMicro(),
	}
	sign := func(value revocations.RemovalRecoveryDecision) []byte {
		t.Helper()
		message, err := value.SigningBytes()
		if err != nil {
			t.Fatal(err)
		}
		return ed25519.Sign(signer, message)
	}
	reject := func(ctx context.Context, value revocations.RemovalRecoveryDecision, signature []byte) {
		t.Helper()
		if got, err := f.svc.IssueRemovalRecoveryApproval(ctx, value, signature); !errors.Is(err, revocations.ErrPreflight) || got != (revocations.RemovalRecoveryApproval{}) {
			t.Fatal("invalid recovery approval accepted", got, err)
		}
	}
	reject(f.ctx, d, sign(d))
	originalMessage, _ := dispatch.SigningBytes()
	reject(ctx, d, ed25519.Sign(signer, originalMessage))
	for _, edit := range []func(*revocations.RemovalRecoveryDecision){
		func(d *revocations.RemovalRecoveryDecision) { d.SharedRestartConsent = false },
		func(d *revocations.RemovalRecoveryDecision) { d.Nonce = f.nonce },
		func(d *revocations.RemovalRecoveryDecision) { d.FailureReportDigest = strings.Repeat("0", 64) },
		func(d *revocations.RemovalRecoveryDecision) { d.AgentGeneration++ },
		func(d *revocations.RemovalRecoveryDecision) { d.JobID = d.AccountID },
		func(d *revocations.RemovalRecoveryDecision) {
			d.ExpiresAtUnixMicros = now.Add(-time.Minute).UnixMicro()
		},
		func(d *revocations.RemovalRecoveryDecision) {
			d.ExpiresAtUnixMicros = now.Add(2 * time.Minute).UnixMicro()
		},
		func(d *revocations.RemovalRecoveryDecision) { d.PreviousAttemptID = d.AccountID },
	} {
		bad := d
		edit(&bad)
		reject(ctx, bad, sign(bad))
	}
	signature := sign(d)
	type outcome struct {
		approval revocations.RemovalRecoveryApproval
		err      error
	}
	results := make(chan outcome, 2)
	for range 2 {
		go func() {
			approval, err := f.svc.IssueRemovalRecoveryApproval(ctx, d, signature)
			results <- outcome{approval, err}
		}()
	}
	first, second := <-results, <-results
	if first.err != nil || second.err != nil || first.approval != second.approval ||
		first.approval.AttemptID != d.AttemptID || !first.approval.ExpiresAt.Equal(time.UnixMicro(d.ExpiresAtUnixMicros)) {
		t.Fatal("concurrent approval changed the deadline or appended duplicate authority", first, second)
	}
	replacement := d
	replacement.AttemptID = "11223344-5566-4788-899a-bbccddeeff00"
	replacement.PreviousAttemptID = d.AttemptID
	replacement.Nonce = strings.Repeat("c", 64)
	reject(ctx, replacement, sign(replacement))

	var unchanged bool
	if err := f.pool.QueryRow(ctx, `SELECT r.state='prepared' AND j.status='held_for_revocation'
AND s.active_config_version_id=r.baseline_version_id AND a.expires_at=$2 AND a.nonce=$3
AND (SELECT count(*) FROM routegate_dispatch.removal_recovery_approvals WHERE operation_id=r.id)=1
AND NOT EXISTS(SELECT 1 FROM routegate_dispatch.confirmed_removals WHERE operation_id=r.id)
FROM vpn_credential_revocations r JOIN routegate_dispatch.held_removals h ON h.operation_id=r.id
JOIN config_apply_jobs j ON j.id=h.job_id JOIN servers s ON s.id=r.server_id
JOIN vpn_credential_removal_dispatch_authorizations a ON a.operation_id=r.id
WHERE r.id=$1::uuid`, f.operation, time.UnixMicro(dispatch.ExpiresAtUnixMicros), f.nonce).Scan(&unchanged); err != nil || !unchanged {
		t.Fatal("new consent changed the original operation, nonce, expiry or reservation", err)
	}
	if _, err := f.pool.Exec(ctx, "UPDATE auth_sessions SET revoked_at=clock_timestamp() WHERE id=$1::uuid", user.SessionID); err != nil {
		t.Fatal(err)
	}
	reject(ctx, d, signature)
	down, err := os.ReadFile("../../migrations/000180_removal_recovery_approvals.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, string(down)); err == nil {
		t.Fatal("downgrade erased recovery consent history")
	}
}
