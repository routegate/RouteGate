package db

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/ikaevus/routegate/backend/internal/auth"
	"github.com/ikaevus/routegate/backend/internal/revocations"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Isolated connections are necessary to expose a real PostgreSQL row-lock cycle.
func dispatchRacePool(t *testing.T, ctx context.Context, app string) *pgxpool.Pool {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(os.Getenv("ROUTEGATE_TEST_DATABASE_URL"))
	if err != nil { t.Fatal(err) }
	cfg.ConnConfig.RuntimeParams["application_name"] = app
	cfg.ConnConfig.RuntimeParams["lock_timeout"] = "4s"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil { t.Fatal(err) }
	return pool
}

// Assert that the contender reached an actual server-side lock wait.
func waitForDispatchRaceLock(t *testing.T, ctx context.Context, pool *pgxpool.Pool, app string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var count int
		err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity
			WHERE application_name=$1 AND wait_event_type='Lock'`, app).Scan(&count)
		if err != nil { t.Fatal(err) }
		if count > 0 { return }
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s never reached a PostgreSQL lock wait", app)
}

func confirmedRevocationForLockRace(t *testing.T) (
	context.Context, *pgxpool.Pool, revocations.DispatchClaimInput, string, string, func(),
) {
	t.Helper()
	ctx, pool, in, _, done := removalPreparationFixture(t)
	user := dispatchReviewUser()
	authCtx := auth.ContextWithUser(ctx, user)
	in.Actor = ""
	p, err := revocations.NewAuthorizedPreparation(revocations.NewService(pool)).Prepare(authCtx, in, true)
	if err != nil { done(); t.Fatal(err) }
	var hash string
	err = pool.QueryRow(ctx, `SELECT candidate_hash FROM vpn_credential_revocations WHERE id=$1::uuid`, p.ID).Scan(&hash)
	if err != nil { done(); t.Fatal(err) }
	_, err = revocations.NewAuthorizedConfirmation(pool).Confirm(authCtx, revocations.ConfirmInput{
		OperationID: p.ID, ExpectedCandidateHash: hash, SharedProcessRestartAcknowledged: true,
	})
	if err != nil { done(); t.Fatal(err) }
	return authCtx, pool, revocations.DispatchClaimInput{
		OperationID: p.ID, ExpectedCandidateHash: hash, ExplicitExecutionApproval: true,
	}, in.ServerID, user.ID, done
}

// Cancel holds server FOR UPDATE then operation FOR UPDATE. An INSERT's FK
// implicitly acquires server KEY SHARE, and an operation-first claim could
// deadlock even without explicit FOR UPDATE on servers in Claim().
func TestRevocationClaimWaitsForServerFirstWhenCancellationWins(t *testing.T) {
	userCtx, pool, req, serverID, actor, done := confirmedRevocationForLockRace(t)
	defer done()
	ctx, cancel := context.WithTimeout(userCtx, 12*time.Second)
	defer cancel()

	tx, err := pool.Begin(ctx)
	if err != nil { t.Fatal(err) }
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT id FROM servers WHERE id=$1::uuid FOR UPDATE`, serverID); err != nil { t.Fatal(err) }

	const app = "rg140-claim-blocked-by-cancel"
	other := dispatchRacePool(t, ctx, app)
	defer other.Close()
	result := make(chan error, 1)
	go func() {
		_, e := revocations.NewAuthorizedDispatchClaim(other).Claim(ctx, req)
		result <- e
	}()
	waitForDispatchRaceLock(t, ctx, pool, app)

	// If Claim locked the operation before attempting server KEY SHARE,
	// this lock would deadlock while the claimant waits for the server FK.
	if _, err = tx.Exec(ctx, `SELECT id FROM vpn_credential_revocations WHERE id=$1::uuid FOR UPDATE`, req.OperationID); err != nil {
		t.Fatalf("operation-first FK deadlock: %v", err)
	}
	if _, err = tx.Exec(ctx, `UPDATE vpn_credential_revocations
		SET state='cancelled', cancelled_at=now(), last_action_by=$2
		WHERE id=$1::uuid`, req.OperationID, actor); err != nil { t.Fatal(err) }
	if err = tx.Commit(ctx); err != nil { t.Fatal(err) }
	select {
	case e := <-result:
		if !errors.Is(e, revocations.ErrPreflight) { t.Fatalf("claimed cancelled operation: %v", e) }
	case <-ctx.Done(): t.Fatal("claim did not resume after cancellation")
	}
	var claims int
	err = pool.QueryRow(ctx, `SELECT count(*) FROM vpn_credential_revocation_dispatch_claims WHERE operation_id=$1::uuid`, req.OperationID).Scan(&claims)
	if err != nil || claims != 0 { t.Fatalf("cancel-first race left claim: %d %v", claims, err) }
}

// A concurrent KEY SHARE must still pass while a canceller waits for server
// FOR UPDATE. The claim wins and the following cancellation is refused.
func TestRevocationClaimCommitsBeforeWaitingCancellation(t *testing.T) {
	userCtx, pool, req, serverID, _, done := confirmedRevocationForLockRace(t)
	defer done()
	ctx, cancel := context.WithTimeout(userCtx, 12*time.Second)
	defer cancel()

	tx, err := pool.Begin(ctx)
	if err != nil { t.Fatal(err) }
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT id FROM servers WHERE id=$1::uuid FOR KEY SHARE`, serverID); err != nil { t.Fatal(err) }
	const app = "rg140-cancel-blocked-by-claim"
	other := dispatchRacePool(t, ctx, app)
	defer other.Close()
	result := make(chan error, 1)
	go func() {
		_, e := revocations.NewService(other).Cancel(ctx, req.OperationID, "review-operator")
		result <- e
	}()
	waitForDispatchRaceLock(t, ctx, pool, app)

	claim, err := revocations.NewAuthorizedDispatchClaim(pool).Claim(ctx, req)
	if err != nil || claim.State != "outcome_unknown" {
		t.Fatalf("claim blocked by competing server lock: %+v %v", claim, err)
	}
	if err = tx.Commit(ctx); err != nil { t.Fatal(err) }
	select {
	case e := <-result:
		if !errors.Is(e, revocations.ErrPreflight) { t.Fatalf("cancelled possible dispatch: %v", e) }
	case <-ctx.Done(): t.Fatal("cancellation did not resume after claim")
	}
	var state string
	var claims, jobs int
	err = pool.QueryRow(ctx, `SELECT p.state,
		(SELECT count(*) FROM vpn_credential_revocation_dispatch_claims WHERE operation_id=p.id),
		(SELECT count(*) FROM agent_operation_jobs WHERE server_id=p.server_id)
		FROM vpn_credential_revocations p WHERE p.id=$1::uuid`, req.OperationID).Scan(&state, &claims, &jobs)
	if err != nil || state != "prepared" || claims != 1 || jobs != 0 {
		t.Fatalf("unexpected claim-first state: %s claims=%d jobs=%d err=%v", state, claims, jobs, err)
	}
}
