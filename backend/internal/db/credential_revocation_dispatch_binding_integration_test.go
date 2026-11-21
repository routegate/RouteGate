package db

import (
	"errors"
	"os"
	"sync"
	"testing"

	"github.com/ikaevus/routegate/backend/internal/revocations"
	"github.com/jackc/pgx/v5/pgconn"
)

// Adapted from #713 for the existing canonical schema 161–168.
// These identifiers are durable, NON-EXECUTABLE planning metadata; they
// are not a user authorization, a config version or an Agent job.
func TestRevocationPreparedIDBindingIsStableAndNonExecutable(t *testing.T) {
	ctx, pool, in, _, done := removalPreparationFixture(t)
	defer done()
	svc := revocations.NewService(pool)
	prepared, err := svc.Prepare(ctx, in)
	if err != nil { t.Fatal(err) }
	pinned, err := svc.InspectPreparedRemovalIntegrity(ctx, prepared.ID)
	if err != nil { t.Fatal("actual applied baseline was not revalidated", err) }
	var beforeVersion, beforeJob, beforeAudit int
	if err := pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM config_versions),
		(SELECT count(*) FROM config_apply_jobs),
		(SELECT count(*) FROM vpn_credential_revocation_events)`).Scan(&beforeVersion, &beforeJob, &beforeAudit); err != nil {
		t.Fatal(err)
	}

	type result struct {
		ids revocations.PreparedRemovalJobIDs
		err error
	}
	responses := make(chan result, 2)
	var wg sync.WaitGroup
	for i:=0;i<2;i++ {
		wg.Add(1)
		go func(){
			defer wg.Done()
			ids,e := svc.ReservePreparedRemovalJobIDs(ctx,prepared.ID)
			responses <- result{ids,e}
		}()
	}
	wg.Wait()
	a,b:= <-responses, <-responses
	if a.err!=nil || b.err!=nil || a.ids!=b.ids ||
		a.ids.OperationID!=prepared.ID ||
		a.ids.AgentGeneration!=pinned.AgentGeneration ||
		a.ids.BaselineVersionID!=pinned.BaselineVersionID ||
		a.ids.CandidateHash!=pinned.CandidateHash ||
		a.ids.CandidateRuntimeHash!=pinned.CandidateRuntimeHash {
		t.Fatalf("concurrent ID reservation diverged: first=%+v second=%+v",a,b)
	}
	retry,e:=svc.ReservePreparedRemovalJobIDs(ctx,prepared.ID)
	if e!=nil || retry!=a.ids {t.Fatal("retry changed immutable planned task identity",e)}
	var afterVersion, afterJob, afterAudit, bindings int
	if err:=pool.QueryRow(ctx,`SELECT
		(SELECT count(*) FROM config_versions),
		(SELECT count(*) FROM config_apply_jobs),
		(SELECT count(*) FROM vpn_credential_revocation_events),
		(SELECT count(*) FROM vpn_credential_revocation_dispatch_bindings)`).Scan(&afterVersion,&afterJob,&afterAudit,&bindings);err!=nil ||
		bindings!=1 || afterVersion!=beforeVersion || afterJob!=beforeJob || afterAudit!=beforeAudit {
		t.Fatal("ID reservation created executable job/version or altered original audit",err)
	}

	// Same safety guard as legacy #713: the prepared reservation still
	// prevents an ordinary config-apply task from masquerading as removal.
	if _,err=pool.Exec(ctx,`INSERT INTO config_apply_jobs
		(server_id,agent_id,config_version_id,action)
		VALUES ($1::uuid,$2::uuid,$3::uuid,'apply')`,
		prepared.ServerID,prepared.AgentID,a.ids.BaselineVersionID); !pgerrIs(err,"P0141") {
		t.Fatal("normal config apply bypassed the reserved node gate",err)
	}
	for _,sql := range []string{
		`UPDATE vpn_credential_revocation_dispatch_bindings SET candidate_hash=repeat('f',64)`,
		`DELETE FROM vpn_credential_revocation_dispatch_bindings`,
	} {
		if _,err=pool.Exec(ctx,sql);!pgerrIs(err,"P0152"){t.Fatal("immutable planned IDs were rewritten or deleted",sql,err)}
	}
	if _,err=svc.Cancel(ctx,prepared.ID,"operator");err!=nil{t.Fatal(err)}
	if ids,e:=svc.ReservePreparedRemovalJobIDs(ctx,prepared.ID);!errors.Is(e,revocations.ErrPreflight) || ids!=(revocations.PreparedRemovalJobIDs{}){
		t.Fatal("cancelled preparation authorized a new ID plan",e)
	}
	if err:=pool.QueryRow(ctx,`SELECT count(*) FROM vpn_credential_revocation_dispatch_bindings`).Scan(&bindings);err!=nil || bindings!=1 {
		t.Fatal("cancel erased original planned ID history",err)
	}
	downgrade,e:=os.ReadFile("../../migrations/000168_credential_removal_dispatch_bindings.down.sql")
	if e!=nil {t.Fatal(e)}
	if _,e=pool.Exec(ctx,string(downgrade));e==nil {
		t.Fatal("downgrade discarded reserved historical identifiers")
	}
	if err:=pool.QueryRow(ctx,`SELECT count(*) FROM vpn_credential_revocation_dispatch_bindings`).Scan(&bindings);err!=nil || bindings!=1{
		t.Fatal("failed downgrade corrupted retained bindings",err)
	}
}
func pgerrIs(err error,code string) bool {
	var e *pgconn.PgError
	return errors.As(err,&e) && e.Code==code
}

func TestRevocationBindingRefusesDirectIdentityForgery(t *testing.T) {
	ctx,pool,in,_,done:=removalPreparationFixture(t)
	defer done()
	p,err:=revocations.NewService(pool).Prepare(ctx,in)
	if err!=nil {t.Fatal(err)}
	var generation int64
	var baseline,hash,runtime string
	if err=pool.QueryRow(ctx,`SELECT agent_credential_generation,
		baseline_version_id::text,candidate_hash,candidate_runtime_hash
		FROM vpn_credential_revocations WHERE id=$1::uuid`,p.ID).Scan(&generation,&baseline,&hash,&runtime);err!=nil{t.Fatal(err)}
	for _,candidate:=range []struct{name string;g int64;hash string}{
		{"wrong_hash",generation,"ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"},
		{"wrong_generation",generation+1,hash},
	}{
		t.Run(candidate.name,func(t *testing.T){
			_,e:=pool.Exec(ctx,`INSERT INTO vpn_credential_revocation_dispatch_bindings
				(operation_id,agent_credential_generation,baseline_version_id,
				candidate_hash,candidate_runtime_hash)
				VALUES($1::uuid,$2,$3::uuid,$4,$5)`,
				p.ID,candidate.g,baseline,candidate.hash,runtime)
			if !pgerrIs(e,"P0152"){t.Fatal("identity forgery bypassed trigger",e)}
		})
	}
	var n int
	if err=pool.QueryRow(ctx,`SELECT count(*) FROM vpn_credential_revocation_dispatch_bindings`).Scan(&n);err!=nil||n!=0{
		t.Fatal("invalid identity left a binding",err)
	}
}
