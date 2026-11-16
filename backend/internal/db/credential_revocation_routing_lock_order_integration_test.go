package db

import (
	"errors"
	"os"
	"testing"

	"github.com/ikaevus/routegate/backend/internal/revocations"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// A routing writer owns RowExclusive before its BEFORE ROW trigger can request
// advisory lock 140159. A Manager preparer must NOT acquire advisory 140159
// then request a conflicting SHARE table lock: that order can deadlock.
// This test holds RowExclusive *before* preparation and uses PostgreSQL's
// server-side lock_timeout so a deadlocked/blocked preparer fails decisively.
func TestRevocationPrepareDoesNotDeadlockWithRoutingRowExclusive(t *testing.T) {
	ctx, pool, input, _, done := removalPreparationFixture(t)
	defer done()

	routingTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer routingTx.Rollback(ctx)
	if _, err := routingTx.Exec(ctx, "LOCK TABLE routing_profiles IN ROW EXCLUSIVE MODE"); err != nil {
		t.Fatal(err)
	}

	cfg, err := pgxpool.ParseConfig(os.Getenv("ROUTEGATE_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	// A timeout originating inside PostgreSQL is a trustworthy error, unlike
	// assuming that a cancelled Go context rolled its SQL transaction back.
	cfg.ConnConfig.RuntimeParams["lock_timeout"] = "750ms"
	preparer, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer preparer.Close()
	prepared, err := revocations.NewService(preparer).Prepare(ctx, input)
	if err != nil {
		t.Fatalf("preparation blocked on routing RowExclusive lock; likely inverted table SHARE/advisory order: %v", err)
	}
	if prepared.State != "prepared" {
		t.Fatalf("preparation not reserved: %+v", prepared)
	}

	// Holding a routing table lock alone must not bypass the advisory trigger.
	// After the preparation commits, the writer must reject the change,
	// leaving the existing shared routing untouched.
	if _, err := routingTx.Exec(ctx,
		"INSERT INTO routing_profiles(name) VALUES('rg140-route-blocked-after-preparation')"); err == nil {
		t.Fatal("routing writer bypassed committed revocation reservation")
	} else {
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "P0141" {
			t.Fatalf("unexpected SQL routing rejection, want P0141: %v", err)
		}
	}
	if err := routingTx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}

	if _, err := revocations.NewService(preparer).Cancel(ctx, prepared.ID, "review-operator"); err != nil {
		t.Fatalf("cancel dormant reservation: %v", err)
	}
	if _, err := pool.Exec(ctx,
		"INSERT INTO routing_profiles(name) VALUES('rg140-route-allowed-after-cancel')"); err != nil {
		t.Fatalf("normal routing edit should recover after cancellation: %v", err)
	}
}
