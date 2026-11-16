package db

import (
	"errors"
	"testing"

	"github.com/ikaevus/routegate/backend/internal/auth"
	"github.com/ikaevus/routegate/backend/internal/revocations"
)

// Observation cannot turn a lost Manager commit response into permission to
// send. In particular, a NOT_OBSERVED read can race an uncommitted INSERT.
func TestRevocationReadbackNeverAuthorizesReplayOfUncommittedClaim(t *testing.T) {
	ctx, pool, in, _, done := removalPreparationFixture(t)
	defer done()
	user := dispatchReviewUser()
	userCtx := auth.ContextWithUser(ctx, user)
	in.Actor = ""
	p, err := revocations.NewAuthorizedPreparation(revocations.NewService(pool)).Prepare(userCtx, in, true)
	if err != nil { t.Fatal(err) }
	var candidateHash string
	if err = pool.QueryRow(ctx, `SELECT candidate_hash FROM vpn_credential_revocations WHERE id=$1::uuid`, p.ID).Scan(&candidateHash); err != nil { t.Fatal(err) }
	if _, err = revocations.NewAuthorizedConfirmation(pool).Confirm(userCtx, revocations.ConfirmInput{
		OperationID:p.ID, ExpectedCandidateHash:candidateHash, SharedProcessRestartAcknowledged:true,
	}); err != nil { t.Fatal(err) }
	readback := revocations.NewDispatchClaimReadback(pool)

	// Nothing was claimed yet. This response MUST NOT imply it is safe to
	// replay an earlier timed-out request; no CanRetry/ShouldSend field exists.
	before, err := readback.Observe(userCtx, p.ID)
	if err != nil || before.OperationID != p.ID || before.ClaimID != "" ||
		before.State != revocations.DispatchClaimNotObserved || !before.ClaimedAt.IsZero() {
		t.Fatalf("incorrect initial observation: %+v %v",before,err)
	}

	// Stage a valid direct SQL INSERT and leave it uncommitted while another
	// connection reads the ledger. Under READ COMMITTED, the row is not
	// visible yet, and the observation deliberately refuses any implication
	// that another dispatch may proceed.
	tx, err := pool.Begin(userCtx)
	if err != nil { t.Fatal(err) }
	defer tx.Rollback(userCtx)
	var claimID string
	err = tx.QueryRow(userCtx, `
		INSERT INTO vpn_credential_revocation_dispatch_claims
		  (operation_id,claimed_by,server_id,agent_id,agent_credential_generation,
		   baseline_version_id,confirmation_at,candidate_hash,candidate_runtime_hash)
		SELECT p.id,p.requested_by,p.server_id,p.agent_id,p.agent_credential_generation,
		       p.baseline_version_id,c.confirmed_at,p.candidate_hash,p.candidate_runtime_hash
		FROM vpn_credential_revocations p
		JOIN vpn_credential_revocation_confirmations c ON c.operation_id=p.id
		WHERE p.id=$1::uuid
		RETURNING claim_id::text
	`,p.ID).Scan(&claimID)
	if err != nil { t.Fatal(err) }

	pending, err := readback.Observe(userCtx, p.ID)
	if err != nil || pending.State != revocations.DispatchClaimNotObserved || pending.ClaimID != "" {
		t.Fatalf("uncommitted claim should not be falsely observed: %+v %v",pending,err)
	}
	if err = tx.Commit(userCtx); err != nil { t.Fatal(err) }

	after, err := readback.Observe(userCtx, p.ID)
	if err != nil || after.OperationID != p.ID || after.ClaimID != claimID ||
		after.State != revocations.DispatchClaimOutcomeUnknown || after.ClaimedAt.IsZero() {
		t.Fatalf("committed claim not observed as outcome unknown: %+v %v",after,err)
	}

	again, err := readback.Observe(userCtx, p.ID)
	if err != nil || again != after {
		t.Fatalf("readback altered immutable evidence: first=%+v next=%+v error=%v",after,again,err)
	}
	if _,err = revocations.NewAuthorizedDispatchClaim(pool).Claim(userCtx,revocations.DispatchClaimInput{
		OperationID:p.ID,ExpectedCandidateHash:candidateHash,ExplicitExecutionApproval:true,
	});!errors.Is(err,revocations.ErrDispatchClaimAlreadyExists){
		t.Fatalf("readback made possible-send claim repeatable: %v",err)
	}
	var claims, events, jobs int
	err = pool.QueryRow(userCtx, `SELECT
		(SELECT count(*) FROM vpn_credential_revocation_dispatch_claims WHERE operation_id=$1::uuid),
		(SELECT count(*) FROM vpn_credential_revocation_events WHERE operation_id=$1::uuid),
		(SELECT count(*) FROM agent_operation_jobs WHERE server_id=$2::uuid)
	`,p.ID,in.ServerID).Scan(&claims,&events,&jobs)
	if err != nil || claims!=1 || events!=1 || jobs!=0 {
		t.Fatalf("readback changed durable ledger or sent Agent work: claims=%d events=%d jobs=%d err=%v",claims,events,jobs,err)
	}
}

func TestRevocationReadbackRejectsForeignOrUnauthenticatedSessions(t *testing.T) {
	ctx,pool,in,_,done := removalPreparationFixture(t)
	defer done()
	user := dispatchReviewUser()
	userCtx := auth.ContextWithUser(ctx,user)
	in.Actor = ""
	p,err := revocations.NewAuthorizedPreparation(revocations.NewService(pool)).Prepare(userCtx,in,true)
	if err != nil { t.Fatal(err) }
	svc := revocations.NewDispatchClaimReadback(pool)
	other := user
	other.ID = "33333333-3333-4333-8333-333333333333"
	other.SessionID = "44444444-4444-4444-8444-444444444444"
	for name, testCtx := range map[string]func() error {
		"missing session":func()error { _,e:=svc.Observe(ctx,p.ID);return e },
		"foreign super admin":func()error { _,e:=svc.Observe(auth.ContextWithUser(ctx,other),p.ID);return e },
		"invalid operation":func()error { _,e:=svc.Observe(userCtx,"not-an-id");return e },
		"nonexistent operation":func()error { _,e:=svc.Observe(userCtx,"99999999-9999-4999-8999-999999999999");return e },
	} {
		t.Run(name,func(t *testing.T) {
			if err := testCtx(); !errors.Is(err,revocations.ErrPreflight) {
				t.Fatalf("unauthorized lookup succeeded: %v",err)
			}
		})
	}
	// Unmounted readback has no dependency on token mutation, HTTP handlers,
	// Agent runtime, or the ability to turn unknown into success.
}
