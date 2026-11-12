package db

import (
    "context"
    "errors"
    "strings"
    "sync"
    "testing"
    "time"

    "github.com/jackc/pgx/v5/pgconn"
    "github.com/jackc/pgx/v5/pgxpool"
)

type recoveryReservationFixture struct {
    ctx context.Context
    pool *pgxpool.Pool
    serverID string
    otherServerID string
    accountID string
    agentID string
    generation int64
    jobID string
    configVersionID string
    done func()
}

func newRecoveryReservationFixture(t *testing.T) recoveryReservationFixture {
    t.Helper()
    ctx, pool, server, other, account, done := transferFixture(t)
    f := recoveryReservationFixture{ctx: ctx, pool: pool, serverID: server, otherServerID: other, accountID: account, done: done}
    if err := pool.QueryRow(ctx,`SELECT id::text,credential_generation FROM agents WHERE server_id=$1::uuid`,server).
        Scan(&f.agentID,&f.generation); err != nil {
        done()
        t.Fatal(err)
    }
    if err := pool.QueryRow(ctx,`
        SELECT j.id::text,j.config_version_id::text
        FROM config_apply_jobs j
        WHERE j.server_id=$1::uuid AND j.agent_id=$2::uuid AND j.status='succeeded' AND j.action='apply'
        ORDER BY j.completed_at DESC LIMIT 1
    `,server,f.agentID).Scan(&f.jobID,&f.configVersionID);err != nil {
        done()
        t.Fatal("fixture has no positively completed scoped job",err)
    }
    return f
}

func (f recoveryReservationFixture) reserve(ctx context.Context, generation int64, jobKind string) error {
    _, err := f.pool.Exec(ctx,`
        INSERT INTO vpn_runtime_recovery_reservations
        (server_id,agent_id,job_id,job_kind,agent_credential_generation,
         mutation_intent_sha256,result_receipt_sha256,runtime_witness_sha256,requested_by)
        VALUES($1::uuid,$2::uuid,$3::uuid,$4,$5,$6,$7,$8,$9)
    `,f.serverID,f.agentID,f.jobID,jobKind,generation,
      strings.Repeat("a",64),strings.Repeat("b",64),strings.Repeat("c",64),"isolated-operator")
    return err
}

func (f recoveryReservationFixture) enableFencing(t *testing.T) {
    t.Helper()
    if _,err:=f.pool.Exec(f.ctx,`
        UPDATE agents SET capabilities=capabilities || '{"runtimeMutationFencingV1":true}'::jsonb
        WHERE id=$1::uuid
    `,f.agentID);err!=nil { t.Fatal(err) }
}

func requireRecoveryGuard(t *testing.T, err error) {
    t.Helper()
    var pg *pgconn.PgError
    if !errors.As(err,&pg) || pg.Code!="P0142" {
        t.Fatalf("expected P0142 fail-closed recovery guard, got %v",err)
    }
}

func TestRuntimeRecoveryReservationBlocksMutationsAndCannotBeDeleted(t *testing.T) {
    f:=newRecoveryReservationFixture(t)
    defer f.done()
    f.enableFencing(t)
    if err:=f.reserve(f.ctx,f.generation,"config_apply");err!=nil { t.Fatal(err) }

    _,err:=f.pool.Exec(f.ctx,`
        INSERT INTO config_apply_jobs(server_id,agent_id,config_version_id,action,status)
        VALUES($1::uuid,$2::uuid,$3::uuid,'apply','pending')
    `,f.serverID,f.agentID,f.configVersionID)
    requireRecoveryGuard(t,err)
    _,err=f.pool.Exec(f.ctx,`
        INSERT INTO agent_operation_jobs(server_id,agent_id,kind,operation,status)
        VALUES($1::uuid,$2::uuid,'vpn_core_service','restart','pending')
    `,f.serverID,f.agentID)
    requireRecoveryGuard(t,err)

    _,err=f.pool.Exec(f.ctx,`UPDATE vpn_accounts SET status='suspended' WHERE id=$1::uuid`,f.accountID)
    requireRecoveryGuard(t,err)
    _,err=f.pool.Exec(f.ctx,`UPDATE agents SET token_hash='rotated-unsafe' WHERE id=$1::uuid`,f.agentID)
    requireRecoveryGuard(t,err)
    _,err=f.pool.Exec(f.ctx,`UPDATE servers SET vless_port=9711 WHERE id=$1::uuid`,f.serverID)
    requireRecoveryGuard(t,err)

    // A legitimate heartbeat must not be disabled by the reservation.
    if _,err=f.pool.Exec(f.ctx,`UPDATE agents SET last_seen_at=now() WHERE id=$1::uuid`,f.agentID);err!=nil {
        t.Fatal("heartbeat unexpectedly prevented",err)
    }
    // The reservation is node-scoped, not a global shutdown of other VPN nodes.
    if _,err=f.pool.Exec(f.ctx,`
        INSERT INTO agent_operation_jobs(server_id,kind,operation,status)
        VALUES($1::uuid,'vpn_core_service','restart','pending')
    `,f.otherServerID);err!=nil {t.Fatal("other node was blocked",err)}

    _,err=f.pool.Exec(f.ctx,`UPDATE vpn_runtime_recovery_reservations SET requested_by='another' WHERE server_id=$1::uuid`,f.serverID)
    requireRecoveryGuard(t,err)
    _,err=f.pool.Exec(f.ctx,`DELETE FROM vpn_runtime_recovery_reservations WHERE server_id=$1::uuid`,f.serverID)
    requireRecoveryGuard(t,err)
    var held int
    if err=f.pool.QueryRow(f.ctx,`SELECT count(*) FROM vpn_runtime_recovery_reservations WHERE server_id=$1::uuid`,f.serverID).Scan(&held);err!=nil||held!=1 {
        t.Fatal("reservation missing after rejected deletion",held,err)
    }
}

func TestRuntimeRecoveryReservationRejectsMissingFencingAndUncertainWork(t *testing.T) {
    for _,mode:=range []string{"legacy_agent","wrong_generation","wrong_job_kind","pending_job"} {
        t.Run(mode,func(t *testing.T){
            f:=newRecoveryReservationFixture(t)
            defer f.done()
            if mode!="legacy_agent" {f.enableFencing(t)}
            generation:=f.generation
            jobKind:="config_apply"
            switch mode {
            case "wrong_generation":
                generation++
            case "wrong_job_kind":
                jobKind="vpn_core_service"
            case "pending_job":
                if _,err:=f.pool.Exec(f.ctx,`
                    INSERT INTO agent_operation_jobs(server_id,agent_id,kind,operation,status)
                    VALUES($1::uuid,$2::uuid,'vpn_core_service','restart','pending')
                `,f.serverID,f.agentID);err!=nil {t.Fatal(err)}
            }
            requireRecoveryGuard(t,f.reserve(f.ctx,generation,jobKind))
            var held int
            if err:=f.pool.QueryRow(f.ctx,`SELECT count(*) FROM vpn_runtime_recovery_reservations`).Scan(&held);err!=nil||held!=0 {
                t.Fatal("unsafe reservation persisted",held,err)
            }
        })
    }
}

// The existing job-insert trigger and the new reservation insert must
// serialize on the same server row. Exactly one side may commit.
func TestRuntimeRecoveryReservationCannotRaceNewJob(t *testing.T) {
    f:=newRecoveryReservationFixture(t)
    defer f.done()
    f.enableFencing(t)
    ctx,cancel:=context.WithTimeout(f.ctx,10*time.Second)
    defer cancel()
    start:=make(chan struct{})
    errorsOut:=make(chan error,2)
    var wg sync.WaitGroup
    wg.Add(2)
    go func(){
        defer wg.Done()
        <-start
        errorsOut<-f.reserve(ctx,f.generation,"config_apply")
    }()
    go func(){
        defer wg.Done()
        <-start
        _,err:=f.pool.Exec(ctx,`
            INSERT INTO agent_operation_jobs(server_id,agent_id,kind,operation,status)
            VALUES($1::uuid,$2::uuid,'vpn_core_service','restart','pending')
        `,f.serverID,f.agentID)
        errorsOut<-err
    }()
    close(start)
    wg.Wait()
    close(errorsOut)
    accepted,rejected:=0,0
    for err:=range errorsOut{
        if err==nil {accepted++;continue}
        requireRecoveryGuard(t,err)
        rejected++
    }
    if accepted!=1||rejected!=1 {
        t.Fatalf("concurrent reservation and job committed together: accepted=%d rejected=%d",accepted,rejected)
    }
    var holds,jobs int
    if err:=f.pool.QueryRow(f.ctx,`
        SELECT (SELECT count(*) FROM vpn_runtime_recovery_reservations WHERE server_id=$1::uuid),
               (SELECT count(*) FROM agent_operation_jobs WHERE server_id=$1::uuid AND status='pending')
    `,f.serverID).Scan(&holds,&jobs);err!=nil {t.Fatal(err)}
    if holds+jobs!=1 {t.Fatalf("unsafe concurrent state holds=%d jobs=%d",holds,jobs)}
}
