package db

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// All fixtures use disposable PostgreSQL schemas. An enrollment PREPARATION
// is untrusted history, not a host enrollment, journal proof or admission gate.
func prepareRecoveryEnrollmentV1(ctx context.Context, f recoveryReservationFixture) (string, error) {
	var id string
	err := f.pool.QueryRow(ctx, `
INSERT INTO vpn_runtime_recovery_enrollment_preparations(agent_id)
VALUES($1::uuid) RETURNING id::text
`, f.agentID).Scan(&id)
	return id, err
}

func requireEnrollmentPreparationRefusal(t *testing.T, err error) {
	t.Helper()
	var pg *pgconn.PgError
	if !errors.As(err, &pg) || pg.Code != "P0147" {
		t.Fatalf("expected P0147 enrollment preparation refusal, got %v", err)
	}
}

func TestRuntimeRecoveryEnrollmentPreparationV1ManagerIdentityAndLegacyUnchanged(t *testing.T) {
	f := newRecoveryReservationFixture(t)
	defer f.done()
	f.enableFencing(t)
	id, err := prepareRecoveryEnrollmentV1(f.ctx, f)
	if err != nil { t.Fatal(err) }
	var server, agent, phase string
	var generation int64
	var events int
	err = f.pool.QueryRow(f.ctx, `
SELECT e.server_id::text, e.agent_id::text, e.agent_credential_generation,
       e.phase, (SELECT count(*) FROM vpn_runtime_recovery_enrollment_preparation_events
                 WHERE preparation_id=e.id AND action='prepared_unattested')
FROM vpn_runtime_recovery_enrollment_preparations e WHERE e.id=$1::uuid
`, id).Scan(&server, &agent, &generation, &phase, &events)
	if err != nil || server != f.serverID || agent != f.agentID ||
		generation != f.generation || phase != "prepared_unattested" || events != 1 {
		t.Fatalf("enrollment prep is not Manager-owned/unattested or audit missing: %s %s %d %s %d %v",
			server,agent,generation,phase,events,err)
	}
	// Preparation MUST NOT make the ordinary Agent locked/enrolled. Current
	// credential rotation, normal legacy job creation and claim still work.
	_, err = f.pool.Exec(f.ctx, `
UPDATE agents SET token_hash=$1 WHERE id=$2::uuid
`, strings.Repeat("f",64), f.agentID)
	if err != nil { t.Fatal("preparation interfered with ordinary Agent rotation:", err) }
	var currentGeneration int64
	if err := f.pool.QueryRow(f.ctx,
		`SELECT credential_generation FROM agents WHERE id=$1::uuid`,f.agentID,
	).Scan(&currentGeneration); err != nil { t.Fatal(err) }
	if currentGeneration <= generation {
		t.Fatalf("credential rotation did not advance generation: %d -> %d",generation,currentGeneration)
	}
	var job string
	err=f.pool.QueryRow(f.ctx, `
INSERT INTO config_apply_jobs(server_id,agent_id,config_version_id,action,status)
VALUES($1::uuid,$2::uuid,$3::uuid,'apply','pending') RETURNING id::text
`, f.serverID,f.agentID,f.configVersionID).Scan(&job)
	if err != nil { t.Fatal("legacy pending job unexpectedly blocked by preparation:",err) }
	if _, err = f.pool.Exec(f.ctx, `
UPDATE config_apply_jobs SET status='in_progress',started_at=now() WHERE id=$1::uuid
`,job); err != nil {t.Fatal("legacy job claim blocked by preparation:",err)}
	var persisted int64
	if err := f.pool.QueryRow(f.ctx,`
SELECT agent_credential_generation FROM vpn_runtime_recovery_enrollment_preparations WHERE id=$1::uuid
`,id).Scan(&persisted); err != nil || persisted != generation {
		t.Fatalf("historical preparation was rewritten or treated as current generation: %d %v",persisted,err)
	}
}

func TestRuntimeRecoveryEnrollmentPreparationV1RefusesSpoofAndIneligibleNode(t *testing.T) {
	for _, mode := range []string{
		"caller_server","caller_generation","caller_attestation",
		"legacy_unfenced","disabled_agent","manager_pending_job",
		"held_post_event_recovery","already_checkpointed",
	} {
		t.Run(mode,func(t *testing.T){
			f:=newRecoveryReservationFixture(t)
			defer f.done()
			if mode!="legacy_unfenced" {f.enableFencing(t)}
			if mode=="disabled_agent" {
				if _,err:=f.pool.Exec(f.ctx,`UPDATE agents SET status='disabled' WHERE id=$1::uuid`,f.agentID);err!=nil{t.Fatal(err)}
			}
			if mode=="manager_pending_job" || mode=="already_checkpointed"{
				var jobID string
				err:=f.pool.QueryRow(f.ctx,`
INSERT INTO config_apply_jobs(server_id,agent_id,config_version_id,action,status)
VALUES($1::uuid,$2::uuid,$3::uuid,'apply','pending') RETURNING id::text
`,f.serverID,f.agentID,f.configVersionID).Scan(&jobID)
				if err!=nil{t.Fatal(err)}
				if mode=="already_checkpointed"{
					if _,err:=f.pool.Exec(f.ctx,`
INSERT INTO vpn_runtime_pre_dispatch_checkpoints(task_id,task_kind)
VALUES($1::uuid,'config_apply')
`,jobID);err!=nil{t.Fatal("checkpoint fixture:",err)}
					if _,err:=f.pool.Exec(f.ctx,`
UPDATE config_apply_jobs SET status='in_progress',started_at=now() WHERE id=$1::uuid
`,jobID);err!=nil{t.Fatal(err)}
				}
			}
			if mode=="held_post_event_recovery" {
				if err:=f.reserve(f.ctx,f.generation,"config_apply");err!=nil{t.Fatal(err)}
			}
			var err error
			switch mode {
			case "caller_server":
				_,err=f.pool.Exec(f.ctx,`
INSERT INTO vpn_runtime_recovery_enrollment_preparations(agent_id,server_id)
VALUES($1::uuid,$2::uuid)
`,f.agentID,f.serverID)
			case "caller_generation":
				_,err=f.pool.Exec(f.ctx,`
INSERT INTO vpn_runtime_recovery_enrollment_preparations(agent_id,agent_credential_generation)
VALUES($1::uuid,$2)
`,f.agentID,f.generation)
			case "caller_attestation":
				_,err=f.pool.Exec(f.ctx,`
INSERT INTO vpn_runtime_recovery_enrollment_preparations(agent_id,phase)
VALUES($1::uuid,'enrolled')
`,f.agentID)
			default:
				_,err=prepareRecoveryEnrollmentV1(f.ctx,f)
			}
			requireEnrollmentPreparationRefusal(t,err)
			var count int
			if err:=f.pool.QueryRow(f.ctx,`
SELECT count(*) FROM vpn_runtime_recovery_enrollment_preparations
`).Scan(&count);err!=nil||count!=0 {
				t.Fatalf("invalid enrollment preparation committed: %d %v",count,err)
			}
		})
	}
}

func TestRuntimeRecoveryEnrollmentPreparationV1ImmutableAndNonDestructive(t *testing.T) {
	f:=newRecoveryReservationFixture(t)
	defer f.done()
	f.enableFencing(t)
	id,err:=prepareRecoveryEnrollmentV1(f.ctx,f)
	if err!=nil{t.Fatal(err)}
	for _,query:=range []string{
		`UPDATE vpn_runtime_recovery_enrollment_preparations SET phase='prepared_unattested' WHERE id=$1::uuid`,
		`DELETE FROM vpn_runtime_recovery_enrollment_preparations WHERE id=$1::uuid`,
	}{
		_,err:=f.pool.Exec(f.ctx,query,id)
		requireEnrollmentPreparationRefusal(t,err)
	}
	for _,query:=range []string{
		`UPDATE vpn_runtime_recovery_enrollment_preparation_events SET action='prepared_unattested'`,
		`DELETE FROM vpn_runtime_recovery_enrollment_preparation_events`,
		`TRUNCATE vpn_runtime_recovery_enrollment_preparations CASCADE`,
		`TRUNCATE vpn_runtime_recovery_enrollment_preparation_events`,
	}{
		_,err:=f.pool.Exec(f.ctx,query)
		requireEnrollmentPreparationRefusal(t,err)
	}
	down,err:=os.ReadFile("../../migrations/000167_runtime_recovery_enrollment_preparation.down.sql")
	if err!=nil{t.Fatal(err)}
	if _,err:=f.pool.Exec(f.ctx,string(down));err==nil||
		!strings.Contains(err.Error(),"enrollment preparation history"){
		t.Fatal("downgrade erased dormant enrollment history:",err)
	}
	var count,audit int
	if err:=f.pool.QueryRow(f.ctx,`
SELECT (SELECT count(*) FROM vpn_runtime_recovery_enrollment_preparations),
       (SELECT count(*) FROM vpn_runtime_recovery_enrollment_preparation_events)
`).Scan(&count,&audit);err!=nil||count!=1||audit!=1{
		t.Fatalf("retained enrollment/audit count=%d/%d err=%v",count,audit,err)
	}
}

func TestRuntimeRecoveryEnrollmentPreparationV1AuditFailureRollsBack(t *testing.T){
	f:=newRecoveryReservationFixture(t)
	defer f.done()
	f.enableFencing(t)
	if _,err:=f.pool.Exec(f.ctx,`
CREATE FUNCTION rg140_refuse_enrollment_prep_audit() RETURNS trigger LANGUAGE plpgsql AS
'BEGIN RAISE EXCEPTION ''injected preparation audit failure''; END'
`);err!=nil{t.Fatal(err)}
	if _,err:=f.pool.Exec(f.ctx,`
CREATE TRIGGER rg140_enrollment_prep_audit_failure
BEFORE INSERT ON vpn_runtime_recovery_enrollment_preparation_events
FOR EACH ROW EXECUTE FUNCTION rg140_refuse_enrollment_prep_audit()
`);err!=nil{t.Fatal(err)}
	if id,err:=prepareRecoveryEnrollmentV1(f.ctx,f);err==nil||id!=""{
		t.Fatalf("enrollment preparation disclosed success without audit: %q %v",id,err)
	}
	var n,a int
	if err:=f.pool.QueryRow(f.ctx,`
SELECT (SELECT count(*) FROM vpn_runtime_recovery_enrollment_preparations),
       (SELECT count(*) FROM vpn_runtime_recovery_enrollment_preparation_events)
`).Scan(&n,&a);err!=nil||n!=0||a!=0{
		t.Fatalf("audit rollback did not remove incomplete transaction: %d/%d %v",n,a,err)
	}
}

func TestRuntimeRecoveryEnrollmentPreparationV1ConcurrentSingleUse(t *testing.T) {
	f:=newRecoveryReservationFixture(t)
	defer f.done()
	f.enableFencing(t)
	ctx,cancel:=context.WithTimeout(f.ctx,12*time.Second)
	defer cancel()
	begin:=make(chan struct{})
	results:=make(chan error,2)
	var wg sync.WaitGroup
	for i:=0;i<2;i++{
		wg.Add(1)
		go func(){
			defer wg.Done()
			<-begin
			_,err:=prepareRecoveryEnrollmentV1(ctx,f)
			results<-err
		}()
	}
	close(begin)
	wg.Wait()
	close(results)
	success,replay:=0,0
	for err:=range results{
		if err==nil{success++;continue}
		var pg *pgconn.PgError
		if errors.As(err,&pg)&&pg.Code=="23505"{replay++;continue}
		t.Fatalf("unexpected concurrent prepare result: %v",err)
	}
	if success!=1||replay!=1{t.Fatalf("replayed enrollment prep success=%d refused=%d",success,replay)}
	var n,a int
	if err:=f.pool.QueryRow(f.ctx,`
SELECT (SELECT count(*) FROM vpn_runtime_recovery_enrollment_preparations),
       (SELECT count(*) FROM vpn_runtime_recovery_enrollment_preparation_events)
`).Scan(&n,&a);err!=nil||n!=1||a!=1{
		t.Fatalf("duplicate or lost audit count=%d/%d err=%v",n,a,err)
	}
}

func TestRuntimeRecoveryEnrollmentPreparationV1EmptyDowngradeOnly(t *testing.T){
	f:=newRecoveryReservationFixture(t)
	defer f.done()
	down,err:=os.ReadFile("../../migrations/000167_runtime_recovery_enrollment_preparation.down.sql")
	if err!=nil{t.Fatal(err)}
	if _,err:=f.pool.Exec(f.ctx,string(down));err!=nil{t.Fatal(err)}
	up,err:=os.ReadFile("../../migrations/000167_runtime_recovery_enrollment_preparation.up.sql")
	if err!=nil{t.Fatal(err)}
	if _,err:=f.pool.Exec(f.ctx,string(up));err!=nil{t.Fatal("empty schema reapply failed:",err)}
}
