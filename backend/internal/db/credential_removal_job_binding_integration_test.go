package db

import (
	"errors"
	"os"
	"sync"
	"testing"

	"github.com/ikaevus/routegate/backend/internal/revocations"
	"github.com/jackc/pgx/v5/pgconn"
)

// The proposed UUIDs are durable planning metadata, NOT version/job rows,
// executable authorization, Manager completion or proof of VPN traffic loss.
func TestCredentialRemovalJobIDBinding(t *testing.T) {
	ctx, pool, in, _, done := removalPreparationFixture(t)
	defer done()
	svc := revocations.NewService(pool)
	prepared, err := svc.Prepare(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	inspected, err := svc.InspectPreparedRemovalIntegrity(ctx, prepared.ID)
	if err != nil {
		t.Fatal(err)
	}
	var versionsBefore, jobsBefore, eventsBefore int
	if err = pool.QueryRow(ctx, `SELECT
	 (SELECT count(*) FROM config_versions),
	 (SELECT count(*) FROM config_apply_jobs),
	 (SELECT count(*) FROM vpn_credential_revocation_events)`).Scan(
		&versionsBefore, &jobsBefore, &eventsBefore); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	results := make(chan struct {
		ids revocations.PreparedRemovalJobIDs
		err error
	}, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ids, err := svc.ReservePreparedRemovalJobIDs(ctx, prepared.ID)
			results <- struct {
				ids revocations.PreparedRemovalJobIDs
				err error
			}{ids, err}
		}()
	}
	wg.Wait()
	x, y := <-results, <-results
	if x.err != nil || y.err != nil || x.ids != y.ids ||
		x.ids.OperationID != prepared.ID ||
		x.ids.AgentGeneration != inspected.AgentGeneration ||
		x.ids.BaselineVersionID != inspected.BaselineVersionID ||
		x.ids.CandidateHash != inspected.CandidateHash ||
		x.ids.CandidateRuntimeHash != inspected.CandidateRuntimeHash {
		t.Fatal("concurrent preparation rebound or dropped a job ID", x, y)
	}
	again, err := svc.ReservePreparedRemovalJobIDs(ctx, prepared.ID)
	if err != nil || again != x.ids {
		t.Fatal("idempotent planned job ID changed", err)
	}
	var bound, versionsAfter, jobsAfter, eventsAfter int
	if err = pool.QueryRow(ctx, `SELECT
	 (SELECT count(*) FROM vpn_credential_revocation_dispatch_bindings),
	 (SELECT count(*) FROM config_versions),
	 (SELECT count(*) FROM config_apply_jobs),
	 (SELECT count(*) FROM vpn_credential_revocation_events)`).Scan(
		&bound, &versionsAfter, &jobsAfter, &eventsAfter); err != nil ||
		bound != 1 || versionsAfter != versionsBefore ||
		jobsAfter != jobsBefore || eventsAfter != eventsBefore {
		t.Fatal("binding created an executable job/version or renewed audit authority", err)
	}
	for _, sql := range []string{
		`UPDATE vpn_credential_revocation_dispatch_bindings
		 SET candidate_hash=repeat('f',64)`,
		`DELETE FROM vpn_credential_revocation_dispatch_bindings`,
	} {
		if _, err = pool.Exec(ctx, sql); err == nil {
			t.Fatal("bound job identifiers were mutable", sql)
		}
		var pgerr *pgconn.PgError
		if !errors.As(err, &pgerr) || pgerr.Code != "P0152" {
			t.Fatal("binding mutation did not use immutable SQL guard", err)
		}
	}
	// The reservation still prohibits ordinary Agent job injection.
	if _, err = pool.Exec(ctx, `INSERT INTO config_apply_jobs
	 (server_id,agent_id,config_version_id,action)
	 VALUES($1::uuid,$2::uuid,$3::uuid,'apply')`,
		prepared.ServerID, prepared.AgentID, x.ids.BaselineVersionID); err == nil {
		t.Fatal("binding bypassed ordinary apply reservation")
	}
	var guard *pgconn.PgError
	if !errors.As(err, &guard) || guard.Code != "P0141" {
		t.Fatal("unexpected normal apply guard", err)
	}
	if _, err = svc.Cancel(ctx, prepared.ID, "operator"); err != nil {
		t.Fatal(err)
	}
	if got, e := svc.ReservePreparedRemovalJobIDs(ctx, prepared.ID); !errors.Is(e, revocations.ErrPreflight) ||
		got != (revocations.PreparedRemovalJobIDs{}) {
		t.Fatal("cancelled reservation returned executable-looking IDs", e)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM vpn_credential_revocation_dispatch_bindings`).Scan(&bound); err != nil || bound != 1 {
		t.Fatal("cancellation rewrote immutable ID history", err)
	}
	// Remove empty dependent human-auth schema first, retaining all IDs.
	exchangeDown, err := os.ReadFile("../../migrations/000177_credential_removal_agent_exchange.down.sql")
	if err != nil { t.Fatal(err) }
	if _, err = pool.Exec(ctx, string(exchangeDown)); err != nil { t.Fatal("empty Agent exchange downgrade", err) }
	heldDown, err := os.ReadFile("../../migrations/000176_credential_removal_held_dispatch.down.sql")
	if err != nil { t.Fatal(err) }
	if _, err = pool.Exec(ctx, string(heldDown)); err != nil { t.Fatal("empty held dispatch downgrade", err) }
	stageDown, err := os.ReadFile("../../migrations/000175_credential_removal_dispatch_stages.down.sql")
	if err != nil { t.Fatal(err) }
	if _, err = pool.Exec(ctx, string(stageDown)); err != nil {
		t.Fatal("empty staged dispatch downgrade", err)
	}
	humanDown, err := os.ReadFile("../../migrations/000174_credential_removal_dispatch_authorizations.down.sql")
	if err != nil { t.Fatal(err) }
	if _, err = pool.Exec(ctx, string(humanDown)); err != nil {
		t.Fatal("empty human-approval downgrade", err)
	}
	// Retained proposed IDs make migration 173 downgrade fail closed.
	down, err := os.ReadFile("../../migrations/000173_credential_removal_dispatch_bindings.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, string(down)); err == nil {
		t.Fatal("downgrade discarded reserved job ID history")
	}
}

func TestCredentialRemovalJobIDBindingRejectsDirectTampering(t *testing.T) {
	ctx, pool, in, _, done := removalPreparationFixture(t)
	defer done()
	svc := revocations.NewService(pool)
	prepared, err := svc.Prepare(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	p, err := svc.InspectPreparedRemovalIntegrity(ctx, prepared.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Direct SQL may not rebind to a changed generation or candidate.
	for _, sql := range []string{
		`INSERT INTO vpn_credential_revocation_dispatch_bindings
		 (operation_id,agent_credential_generation,baseline_version_id,candidate_hash,candidate_runtime_hash)
		 VALUES ($1::uuid,$2,$3::uuid,repeat('f',64),$4)`,
		`INSERT INTO vpn_credential_revocation_dispatch_bindings
		 (operation_id,agent_credential_generation,baseline_version_id,candidate_hash,candidate_runtime_hash)
		 VALUES ($1::uuid,$2+1,$3::uuid,$4,$5)`,
	} {
		var e error
		if sql == `INSERT INTO vpn_credential_revocation_dispatch_bindings
		 (operation_id,agent_credential_generation,baseline_version_id,candidate_hash,candidate_runtime_hash)
		 VALUES ($1::uuid,$2,$3::uuid,repeat('f',64),$4)` {
			_, e = pool.Exec(ctx, sql, prepared.ID, p.AgentGeneration, p.BaselineVersionID, p.CandidateRuntimeHash)
		} else {
			_, e = pool.Exec(ctx, sql, prepared.ID, p.AgentGeneration, p.BaselineVersionID, p.CandidateHash, p.CandidateRuntimeHash)
		}
		var pgerr *pgconn.PgError
		if !errors.As(e, &pgerr) || pgerr.Code != "P0152" {
			t.Fatal("direct SQL bypassed pinned identity", e)
		}
	}
	var count int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM vpn_credential_revocation_dispatch_bindings`).Scan(&count); err != nil || count != 0 {
		t.Fatal("invalid direct insert retained an ID binding", err, count)
	}
}
