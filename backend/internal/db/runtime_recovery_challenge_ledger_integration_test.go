package db

import (
    "context"
    "errors"
    "strings"
    "sync"
    "testing"
    "time"

    "github.com/ikaevus/routegate/backend/internal/auth"
    "github.com/jackc/pgx/v5/pgconn"
    "github.com/jackc/pgx/v5/pgxpool"
)

type recoveryLedgerFixture struct {
    recoveryReservationFixture
    reservationID string
    operatorID string
    sessionID string
}

func newRecoveryLedgerFixture(t *testing.T) recoveryLedgerFixture {
    t.Helper()
    f := newRecoveryReservationFixture(t)
    f.enableFencing(t)
    if err := f.reserve(f.ctx, f.generation, "config_apply"); err != nil {
        f.done()
        t.Fatal("reserve isolated node", err)
    }
    out := recoveryLedgerFixture{recoveryReservationFixture:f}
    if err := f.pool.QueryRow(f.ctx,
        `SELECT id::text FROM vpn_runtime_recovery_reservations WHERE server_id=$1::uuid`,f.serverID).Scan(&out.reservationID); err != nil {
        f.done(); t.Fatal(err)
    }

    repository := auth.NewRepository(f.pool)
    if err := repository.EnsureBuiltIns(f.ctx);err!=nil {f.done();t.Fatal("role seed",err)}
    if err := repository.CreateBootstrapSuperAdmin(f.ctx,
        "rg140-challenge-test@example.invalid", "rg140-challenge-test",
        "isolated-test-password-only", "RG-140 Fixture");err!=nil {f.done();t.Fatal(err)}
    if err := f.pool.QueryRow(f.ctx,
        `SELECT id::text FROM users WHERE email='rg140-challenge-test@example.invalid'`).Scan(&out.operatorID);err!=nil {
        f.done();t.Fatal(err)
    }
    if err := f.pool.QueryRow(f.ctx,`
        INSERT INTO auth_sessions (user_id,token_hash,expires_at)
        VALUES ($1::uuid,$2,clock_timestamp()+interval '10 minutes')
        RETURNING id::text
    `,out.operatorID,strings.Repeat("8",64)).Scan(&out.sessionID);err!=nil {f.done();t.Fatal(err)}
    return out
}

func (f recoveryLedgerFixture) issue(ctx context.Context, userID,sessionID string, issuedAt time.Time) (string,error) {
    var challengeID string
    err:=f.pool.QueryRow(ctx,`
        INSERT INTO vpn_runtime_recovery_challenges
        (reservation_id,operator_user_id,operator_session_id,context_sha256,
         challenge_sha256,issued_at,expires_at)
        VALUES ($1::uuid,$2::uuid,$3::uuid,$4,$5,$6,$6::timestamptz+interval '90 seconds')
        RETURNING id::text
    `,f.reservationID,userID,sessionID,strings.Repeat("a",64),strings.Repeat("b",64),issuedAt).Scan(&challengeID)
    return challengeID,err
}

func (f recoveryLedgerFixture) consume(ctx context.Context, challengeID string) (int64,error) {
    result,err:=f.pool.Exec(ctx,`
        UPDATE vpn_runtime_recovery_challenges
           SET state='consumed',consumed_at=date_trunc('second',clock_timestamp())
         WHERE id=$1::uuid AND state='issued'
           AND expires_at>clock_timestamp()
           AND context_sha256=$2 AND challenge_sha256=$3
    `,challengeID,strings.Repeat("a",64),strings.Repeat("b",64))
    if err!=nil{return 0,err}
    return result.RowsAffected(),nil
}

func requireChallengeGuard(t *testing.T, err error) {
    t.Helper()
    var pg *pgconn.PgError
    if !errors.As(err,&pg) || pg.Code!="P0143" {
        t.Fatalf("expected fail-closed P0143, got %v",err)
    }
}

func TestRuntimeRecoveryChallengeSingleConsumptionAndImmutableAudit(t *testing.T) {
    f:=newRecoveryLedgerFixture(t);defer f.done()
    id,err:=f.issue(f.ctx,f.operatorID,f.sessionID,time.Now().UTC().Truncate(time.Second))
    if err!=nil {t.Fatal("issue",err)}
    rows,err:=f.consume(f.ctx,id)
    if err!=nil||rows!=1 {t.Fatalf("first consume rows=%d err=%v",rows,err)}
    rows,err=f.consume(f.ctx,id)
    if err!=nil||rows!=0 {t.Fatalf("replayed consume rows=%d err=%v",rows,err)}

    var state string
    var auditCount,issuedCount,consumedCount int
    if err:=f.pool.QueryRow(f.ctx,`
        SELECT state,
               (SELECT count(*) FROM vpn_runtime_recovery_challenge_events WHERE challenge_id=$1::uuid),
               (SELECT count(*) FROM vpn_runtime_recovery_challenge_events WHERE challenge_id=$1::uuid AND action='issued'),
               (SELECT count(*) FROM vpn_runtime_recovery_challenge_events WHERE challenge_id=$1::uuid AND action='consumed')
        FROM vpn_runtime_recovery_challenges WHERE id=$1::uuid
    `,id).Scan(&state,&auditCount,&issuedCount,&consumedCount);err!=nil {t.Fatal(err)}
    if state!="consumed"||auditCount!=2||issuedCount!=1||consumedCount!=1{
        t.Fatalf("history lost/duplicated: state=%s all=%d issued=%d consumed=%d",state,auditCount,issuedCount,consumedCount)
    }

    // A replay cannot mutate an immutable final result or remove evidence.
    _,err=f.pool.Exec(f.ctx,`UPDATE vpn_runtime_recovery_challenges SET state='issued',consumed_at=NULL WHERE id=$1::uuid`,id)
    requireChallengeGuard(t,err)
    _,err=f.pool.Exec(f.ctx,`UPDATE vpn_runtime_recovery_challenges SET operator_user_id=$2::uuid WHERE id=$1::uuid`,id,f.operatorID)
    requireChallengeGuard(t,err)
    _,err=f.pool.Exec(f.ctx,`DELETE FROM vpn_runtime_recovery_challenges WHERE id=$1::uuid`,id)
    requireChallengeGuard(t,err)
    _,err=f.pool.Exec(f.ctx,`DELETE FROM vpn_runtime_recovery_challenge_events WHERE challenge_id=$1::uuid`,id)
    requireChallengeGuard(t,err)
    _,err=f.pool.Exec(f.ctx,`TRUNCATE vpn_runtime_recovery_challenges CASCADE`)
    requireChallengeGuard(t,err)
    _,err=f.pool.Exec(f.ctx,`TRUNCATE vpn_runtime_recovery_challenge_events`)
    requireChallengeGuard(t,err)

    // One ever per reservation: no new challenge after success or expiry.
    _,err=f.issue(f.ctx,f.operatorID,f.sessionID,time.Now().UTC().Truncate(time.Second))
    var pg *pgconn.PgError
    if !errors.As(err,&pg)||pg.Code!="23505" {t.Fatalf("duplicate reservation admitted: %v",err)}
}

func TestRuntimeRecoveryChallengeParallelConsumptionExactlyOnce(t *testing.T){
    f:=newRecoveryLedgerFixture(t);defer f.done()
    id,err:=f.issue(f.ctx,f.operatorID,f.sessionID,time.Now().UTC().Truncate(time.Second))
    if err!=nil {t.Fatal(err)}
    ctx,cancel:=context.WithTimeout(f.ctx,12*time.Second);defer cancel()
    start:=make(chan struct{})
    type reply struct{count int64;err error}
    results:=make(chan reply,2)
    var wg sync.WaitGroup
    for i:=0;i<2;i++{wg.Add(1);go func(){defer wg.Done();<-start;n,e:=f.consume(ctx,id);results<-reply{n,e}}()}
    close(start);wg.Wait();close(results)
    var consumed,duplicate int
    for x:=range results{
        if x.err!=nil {t.Fatal(x.err)}
        switch x.count{case 1: consumed++;case 0:duplicate++;default:t.Fatalf("unexpected affected rows %d",x.count)}
    }
    if consumed!=1||duplicate!=1 {t.Fatalf("single-use violated: consumed=%d duplicate=%d",consumed,duplicate)}
    var auditCount int
    if err=f.pool.QueryRow(f.ctx,`
        SELECT count(*) FROM vpn_runtime_recovery_challenge_events
         WHERE challenge_id=$1::uuid AND action='consumed'`,id).Scan(&auditCount);err!=nil||auditCount!=1{
        t.Fatalf("duplicate consumption history=%d err=%v",auditCount,err)
    }
}

func TestRuntimeRecoveryChallengeRequiresLiveSuperAdminAtIssueAndConsume(t *testing.T){
    t.Run("revoked_before_issue",func(t *testing.T){
        f:=newRecoveryLedgerFixture(t);defer f.done()
        if _,err:=f.pool.Exec(f.ctx,`UPDATE auth_sessions SET revoked_at=clock_timestamp() WHERE id=$1::uuid`,f.sessionID);err!=nil{t.Fatal(err)}
        _,err:=f.issue(f.ctx,f.operatorID,f.sessionID,time.Now().UTC().Truncate(time.Second))
        requireChallengeGuard(t,err)
    })
    t.Run("revoked_before_consume",func(t *testing.T){
        f:=newRecoveryLedgerFixture(t);defer f.done()
        id,err:=f.issue(f.ctx,f.operatorID,f.sessionID,time.Now().UTC().Truncate(time.Second))
        if err!=nil{t.Fatal(err)}
        if _,err=f.pool.Exec(f.ctx,`UPDATE auth_sessions SET revoked_at=clock_timestamp() WHERE id=$1::uuid`,f.sessionID);err!=nil{t.Fatal(err)}
        _,err=f.consume(f.ctx,id)
        requireChallengeGuard(t,err)
        var status string
        if err=f.pool.QueryRow(f.ctx,`SELECT state FROM vpn_runtime_recovery_challenges WHERE id=$1::uuid`,id).Scan(&status);err!=nil||status!="issued"{
            t.Fatalf("revoked operator consumed ledger: %s %v",status,err)
        }
    })
    t.Run("role_removed_before_consume",func(t *testing.T){
        f:=newRecoveryLedgerFixture(t);defer f.done()
        id,err:=f.issue(f.ctx,f.operatorID,f.sessionID,time.Now().UTC().Truncate(time.Second))
        if err!=nil{t.Fatal(err)}
        if _,err=f.pool.Exec(f.ctx,`
            DELETE FROM user_roles WHERE user_id=$1::uuid AND role_id=(SELECT id FROM roles WHERE code='super_admin')
        `,f.operatorID);err!=nil{t.Fatal(err)}
        _,err=f.consume(f.ctx,id)
        requireChallengeGuard(t,err)
    })
    t.Run("expired_at_issue",func(t *testing.T){
        f:=newRecoveryLedgerFixture(t);defer f.done()
        _,err:=f.issue(f.ctx,f.operatorID,f.sessionID,time.Now().UTC().Add(-2*time.Minute))
        requireChallengeGuard(t,err)
    })
}

func TestRuntimeRecoveryChallengeCannotUseDifferentHumanOrMissingReservation(t *testing.T) {
    f:=newRecoveryLedgerFixture(t);defer f.done()
    var foreignUserID string
    if err:=f.pool.QueryRow(f.ctx,`
        INSERT INTO users (email,user_type,status)
        VALUES('rg140-unprivileged@example.invalid','human','active') RETURNING id::text
    `).Scan(&foreignUserID);err!=nil{t.Fatal(err)}
    var foreignSession string
    if err:=f.pool.QueryRow(f.ctx,`
        INSERT INTO auth_sessions(user_id,token_hash,expires_at)
        VALUES($1::uuid,$2,clock_timestamp()+interval '10 minutes') RETURNING id::text
    `,foreignUserID,strings.Repeat("9",64)).Scan(&foreignSession);err!=nil{t.Fatal(err)}
    _,err:=f.issue(f.ctx,foreignUserID,foreignSession,time.Now().UTC().Truncate(time.Second))
    requireChallengeGuard(t,err)
    _,err=f.issue(f.ctx,foreignUserID,f.sessionID,time.Now().UTC().Truncate(time.Second))
    requireChallengeGuard(t,err)
    var held int
    if err=f.pool.QueryRow(f.ctx,`SELECT count(*) FROM vpn_runtime_recovery_challenges`).Scan(&held);err!=nil||held!=0{
        t.Fatalf("unauthorized challenge issued: %d %v",held,err)
    }
}

// Never expose production API: the tests exercise only isolated SQL and
// intentionally do not create a route, runtime unlock or Agent task.
var _ *pgxpool.Pool
