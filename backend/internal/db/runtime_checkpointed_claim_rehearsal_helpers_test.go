package db

import (
 "context"
 "errors"
 "fmt"
 "testing"

 "github.com/jackc/pgx/v5"
 "github.com/jackc/pgx/v5/pgxpool"
)

// RG-140 transaction REHEARSAL only. Test source is NOT compiled into
// Manager production binaries. No API, dispatcher, Agent or installer caller.
// A prepared_unattested Manager row is NOT real Agent enrollment.
//
// Both claim types must verify the PREVIOUSLY COMMITTED checkpoint, audit,
// credential generation and Manager-owned identity while holding the same
// pending-job lock used to transition to in_progress.
const rehearsalCheckpointedConfigClaimSQL = `
WITH candidate AS (
 SELECT j.id FROM config_apply_jobs j
 JOIN agents a ON a.id=j.agent_id AND a.server_id=j.server_id
 JOIN config_versions v ON v.id=j.config_version_id AND v.server_id=j.server_id
 JOIN vpn_runtime_pre_dispatch_checkpoints c
   ON c.task_id=j.id AND c.task_kind='config_apply'
  AND c.server_id=j.server_id AND c.agent_id=a.id
  AND c.agent_credential_generation=a.credential_generation
  AND c.config_version_id=v.id AND c.expected_config_sha256=v.config_hash
  AND c.state='pre_dispatch_unattested' AND c.dispatch_epoch>0
  AND c.expected_marker_sha256 ~ '^[0-9a-f]{64}$'
 JOIN vpn_runtime_recovery_enrollment_preparations p
   ON p.agent_id=a.id AND p.server_id=a.server_id
  AND p.agent_credential_generation=a.credential_generation
  AND p.phase='prepared_unattested'
 WHERE j.id=$1::uuid AND j.status='pending' AND j.action='apply'
   AND j.started_at IS NULL AND j.completed_at IS NULL
   AND a.token_hash=$2 AND a.status <> 'disabled'
   AND a.capabilities @> '{"runtimeMutationFencingV1":true}'::jsonb
   AND (SELECT count(*) FROM vpn_runtime_pre_dispatch_checkpoint_events e
        WHERE e.checkpoint_id=c.id AND e.action='committed')=1
   AND (SELECT count(*) FROM vpn_runtime_recovery_enrollment_preparation_events e
        WHERE e.preparation_id=p.id AND e.action='prepared_unattested')=1
   AND NOT EXISTS (SELECT 1 FROM vpn_runtime_recovery_reservations r
                   WHERE r.server_id=j.server_id)
   AND NOT EXISTS (SELECT 1 FROM agent_platform_update_jobs u
                   WHERE u.server_id=j.server_id AND u.status IN
                     ('pending','in_progress','mutation_dispatched','outcome_unknown'))
 FOR UPDATE OF j
 FOR SHARE OF a,v,c,p
)
UPDATE config_apply_jobs j
 SET status='in_progress',started_at=now(),updated_at=now()
 FROM candidate
 WHERE j.id=candidate.id AND j.status='pending'
RETURNING j.id::text
`

const rehearsalCheckpointedServiceClaimSQL = `
WITH candidate AS (
 SELECT j.id FROM agent_operation_jobs j
 JOIN agents a ON a.id=j.agent_id AND a.server_id=j.server_id
 JOIN vpn_runtime_pre_dispatch_checkpoints c
   ON c.task_id=j.id AND c.task_kind='vpn_core_service'
  AND c.server_id=j.server_id AND c.agent_id=a.id
  AND c.agent_credential_generation=a.credential_generation
  AND c.config_version_id IS NULL AND c.expected_config_sha256 IS NULL
  AND c.state='pre_dispatch_unattested' AND c.dispatch_epoch>0
  AND c.expected_marker_sha256 ~ '^[0-9a-f]{64}$'
 JOIN vpn_runtime_recovery_enrollment_preparations p
   ON p.agent_id=a.id AND p.server_id=a.server_id
  AND p.agent_credential_generation=a.credential_generation
  AND p.phase='prepared_unattested'
 WHERE j.id=$1::uuid AND j.status='pending'
   AND j.kind='vpn_core_service' AND j.operation='restart'
   AND j.started_at IS NULL AND j.completed_at IS NULL
   AND a.token_hash=$2 AND a.status <> 'disabled'
   AND a.capabilities @> '{"runtimeMutationFencingV1":true}'::jsonb
   AND (SELECT count(*) FROM vpn_runtime_pre_dispatch_checkpoint_events e
        WHERE e.checkpoint_id=c.id AND e.action='committed')=1
   AND (SELECT count(*) FROM vpn_runtime_recovery_enrollment_preparation_events e
        WHERE e.preparation_id=p.id AND e.action='prepared_unattested')=1
   AND NOT EXISTS (SELECT 1 FROM vpn_runtime_recovery_reservations r
                   WHERE r.server_id=j.server_id)
   AND NOT EXISTS (SELECT 1 FROM agent_platform_update_jobs u
                   WHERE u.server_id=j.server_id AND u.status IN
                     ('pending','in_progress','mutation_dispatched','outcome_unknown'))
 FOR UPDATE OF j
 FOR SHARE OF a,c,p
)
UPDATE agent_operation_jobs j
 SET status='in_progress',started_at=now(),updated_at=now()
 FROM candidate
 WHERE j.id=candidate.id AND j.status='pending'
RETURNING j.id::text
`

// A failed/crashed transaction cannot leave an in_progress job committed.
// This is NOT a network-dispatch function: it returns only a synthetic job ID.
func rehearsalClaimCheckpointedMutationV1(
 ctx context.Context,pool *pgxpool.Pool,kind,jobID,tokenHash string,abortAfterUpdate bool,
) (string,error) {
 sql := rehearsalCheckpointedConfigClaimSQL
 switch kind {
 case "config_apply":
 case "vpn_core_service": sql=rehearsalCheckpointedServiceClaimSQL
 default: return "",errors.New("unsupported rehearsal kind")
 }
 tx,err:=pool.BeginTx(ctx,pgx.TxOptions{IsoLevel:pgx.ReadCommitted})
 if err!=nil{return "",err}
 defer tx.Rollback(ctx)
 var claimed string
 if err:=tx.QueryRow(ctx,sql,jobID,tokenHash).Scan(&claimed);err!=nil{return "",err}
 if abortAfterUpdate{return "",errors.New("simulated termination before commit")}
 if err:=tx.Commit(ctx);err!=nil{return "",err}
 return claimed,nil
}

func checkpointedClaimRehearsalFixtureV1(t *testing.T,kind string,withPrep,withCheckpoint bool) (recoveryReservationFixture,string,string){
 t.Helper()
 f:=newRecoveryReservationFixture(t)
 f.enableFencing(t)
 if withPrep{
  if _,err:=prepareRecoveryEnrollmentV1(f.ctx,f);err!=nil{f.done();t.Fatal(err)}
 }
 var jobID string
 var err error
 switch kind {
 case "config_apply":
  err=f.pool.QueryRow(f.ctx,`
INSERT INTO config_apply_jobs(server_id,agent_id,config_version_id,action,status)
VALUES($1::uuid,$2::uuid,$3::uuid,'apply','pending') RETURNING id::text
`,f.serverID,f.agentID,f.configVersionID).Scan(&jobID)
 case "vpn_core_service":
  err=f.pool.QueryRow(f.ctx,`
INSERT INTO agent_operation_jobs(server_id,agent_id,kind,operation,status)
VALUES($1::uuid,$2::uuid,'vpn_core_service','restart','pending') RETURNING id::text
`,f.serverID,f.agentID).Scan(&jobID)
 default: f.done();t.Fatal("unknown fixture kind",kind)
 }
 if err!=nil{f.done();t.Fatal(err)}
 if withCheckpoint{
  if _,err:=checkpointInsert(f.ctx,f,jobID,kind);err!=nil{f.done();t.Fatal(err)}
 }
 var tokenHash string
 if err:=f.pool.QueryRow(f.ctx,`SELECT token_hash FROM agents WHERE id=$1::uuid`,f.agentID).
   Scan(&tokenHash);err!=nil{f.done();t.Fatal(err)}
 return f,jobID,tokenHash
}

func checkpointedClaimRehearsalStateV1(t *testing.T,f recoveryReservationFixture,kind,jobID string)(string,bool){
 t.Helper()
 table:="config_apply_jobs"
 if kind=="vpn_core_service"{table="agent_operation_jobs"}
 var status string
 var started bool
 if err:=f.pool.QueryRow(f.ctx,fmt.Sprintf(
  "SELECT status,started_at IS NOT NULL FROM %s WHERE id=$1::uuid",table,
 ),jobID).Scan(&status,&started);err!=nil{t.Fatal(err)}
 return status,started
}
