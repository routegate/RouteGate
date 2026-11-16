package db

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ikaevus/routegate/backend/internal/recoveryenroll"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ONLY runs against ROUTEGATE_TEST_DATABASE_URL in disposable isolated CI.
// These tests kill a single PostgreSQL *session* using pg_terminate_backend;
// they do not restart PostgreSQL, kill the Manager, touch a VPS or operate
// on a live Agent. A lost COMMIT acknowledgement is not proof of rollback.

func enrollmentTransportLossFixtureDigestV1(t *testing.T,
	f enrollmentOperatorGrantFixtureV1,
) string {
	t.Helper()
	digest,err:=recoveryenroll.OfflineGenesisDigestUnattestedV1(
		recoveryenroll.Preparation{
			ID:f.preparationID,AgentID:f.agentID,ServerID:f.serverID,
			AgentCredentialGeneration:f.generation,
			Phase:recoveryenroll.PhasePreparedUnattested,
		},
	)
	if err!=nil{t.Fatal(err)}
	return digest
}

func insertEnrollmentTransportLossAttemptV1(
	ctx context.Context,t *testing.T,conn *pgxpool.Conn,
	f enrollmentOperatorGrantFixtureV1,grant enrollmentOperatorGrantV1,
	nonceHash string,
) string {
	t.Helper()
	var id string
	err:=conn.QueryRow(ctx,`
INSERT INTO rg140_test_enrollment_attempts(
 preparation_id,agent_id,server_id,credential_generation,
 expected_genesis_sha256,nonce_sha256,issued_at,expires_at,grant_id
) SELECT $1::uuid,$2::uuid,$3::uuid,$4,$5,$6,
 stamp.issued_at,stamp.issued_at+interval '30 seconds',$7::uuid
FROM (SELECT clock_timestamp() AS issued_at) stamp
RETURNING id::text
`,f.preparationID,f.agentID,f.serverID,f.generation,
		enrollmentTransportLossFixtureDigestV1(t,f),nonceHash,grant.ID).Scan(&id)
	if err!=nil||id==""{t.Fatalf("could not stage session-local enrollment attempt: %q %v",id,err)}
	return id
}

func terminateEnrollmentTestBackendV1(
	t *testing.T,f enrollmentOperatorGrantFixtureV1,pid uint32,
) {
	t.Helper()
	var terminated bool
	err:=f.pool.QueryRow(f.ctx,
		`SELECT pg_terminate_backend($1::int)`,int32(pid)).Scan(&terminated)
	if err!=nil||!terminated{
		t.Fatalf("disposable PostgreSQL session did not terminate pid=%d: %v %t",
			pid,err,terminated)
	}
}

func TestRuntimeRecoveryEnrollmentTransportLossV1UncommittedSessionTerminationRollsBack(t *testing.T){
	f:=newEnrollmentOperatorGrantFixtureV1(t)
	defer f.done()
	grant:=grantForFixtureV1(t,f)
	held,err:=f.pool.Acquire(f.ctx)
	if err!=nil{t.Fatal(err)}
	defer held.Release()
	pid:=held.Conn().PgConn().PID()
	if _,err:=held.Exec(f.ctx,`BEGIN`);err!=nil{t.Fatal(err)}
	id:=insertEnrollmentTransportLossAttemptV1(f.ctx,t,held,f,grant,strings.Repeat("7",64))
	// Both attributed audit records are emitted inside the uncommitted
	// session, but must disappear when that backend connection is killed.
	var internalEvents int64
	if err:=held.QueryRow(f.ctx,`
SELECT count(*) FROM rg140_test_enrollment_attempt_events
WHERE attempt_id=$1::uuid
`,id).Scan(&internalEvents);err!=nil||internalEvents!=1{
		t.Fatalf("pre-crash test transaction missing attempt event: %d %v",internalEvents,err)
	}
	terminateEnrollmentTestBackendV1(t,f,pid)
	// The dead session has no chance to send an application-level rollback.
	// PostgreSQL must abandon its uncommitted attempt and both audit events.
	got:=expectManualEnrollmentHistoryV1(t,f,enrollmentManualGrantOnlyStateV1)
	if got.GrantID!=grant.ID||got.AttemptID!=""{
		t.Fatalf("session loss committed an unacknowledged attempt: %+v",got)
	}
	g,e,a,au:=operatorGrantCountsV1(t,f)
	if g!=1||e!=1||a!=0||au!=1{
		t.Fatalf("killed connection left partial historical state: %d/%d/%d/%d",g,e,a,au)
	}
}

func TestRuntimeRecoveryEnrollmentTransportLossV1CommittedBeforeLostSocketRequiresReview(t *testing.T){
	f:=newEnrollmentOperatorGrantFixtureV1(t)
	defer f.done()
	grant:=grantForFixtureV1(t,f)
	held,err:=f.pool.Acquire(f.ctx)
	if err!=nil{t.Fatal(err)}
	defer held.Release()
	pid:=held.Conn().PgConn().PID()
	if _,err:=held.Exec(f.ctx,`BEGIN`);err!=nil{t.Fatal(err)}
	id:=insertEnrollmentTransportLossAttemptV1(f.ctx,t,held,f,grant,strings.Repeat("6",64))

	// Intentionally issue COMMIT + a delayed read in the SAME simple-query
	// round trip. A separate session must observe the committed attempt
	// BEFORE killing this socket, while the original caller has not yet
	// received the complete round-trip reply.
	networkCtx,cancel:=context.WithTimeout(f.ctx,15*time.Second)
	defer cancel()
	done:=make(chan error,1)
	go func(){
		_,err:=held.Conn().PgConn().Exec(networkCtx,
			`COMMIT; SELECT pg_sleep(20)`).ReadAll()
		done<-err
	}()
	deadline:=time.NewTimer(7*time.Second)
	defer deadline.Stop()
	observed:=false
	for !observed {
		var count int
		err=f.pool.QueryRow(f.ctx,`
SELECT count(*) FROM rg140_test_enrollment_attempts
 WHERE id=$1::uuid
`,id).Scan(&count)
		if err!=nil{t.Fatal("independent Manager reconciliation read:",err)}
		if count==1{
			observed=true
			break
		}
		select{
		case result:=<-done:
			t.Fatalf("COMMIT round-trip ended before externally visible commit: %v",result)
		case <-deadline.C:
			t.Fatal("did not observe committed attempt before test deadline")
		case <-time.After(15*time.Millisecond):
		}
	}
	terminateEnrollmentTestBackendV1(t,f,pid)
	select {
	case err=<-done:
		if err==nil {
			t.Fatal("lost socket falsely returned a successful complete request")
		}
	case <-time.After(6*time.Second):
		t.Fatal("server socket termination failed to unblock the client")
	}
	// The durable issue and its audits must survive even though the
	// original caller received only an ambiguous network error.
	got:=expectManualEnrollmentHistoryV1(t,f,"issued_unattested")
	if got.AttemptID!=id||got.GrantID!=grant.ID||!got.ManualReviewOnly{
		t.Fatalf("lost COMMIT receipt did not require manual reconciliation: %+v",got)
	}
	_,err=issueGrantedEnrollmentChallengeRehearsalV1(f.ctx,f.pool,grant.ID,f.operator)
	if !errors.Is(err,errEnrollmentOperatorGrantRefusedV1){
		t.Fatalf("re-issued challenge after ambiguous network result: %v",err)
	}
	g,e,a,au:=operatorGrantCountsV1(t,f)
	if g!=1||e!=1||a!=1||au!=2{
		t.Fatalf("ambiguous COMMIT created duplicate or partial history: %d/%d/%d/%d",g,e,a,au)
	}
}
