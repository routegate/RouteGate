package db

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ikaevus/routegate/backend/internal/auth"
	"github.com/ikaevus/routegate/backend/internal/recoveryenroll"
)

type authorizedPreparationFixtureV1 struct {
	recoveryReservationFixture
	operator auth.AuthenticatedUser
	prepare *recoveryenroll.Preparer
}

func newAuthorizedPreparationFixtureV1(t *testing.T) authorizedPreparationFixtureV1 {
	t.Helper()
	f := newRecoveryReservationFixture(t)
	f.enableFencing(t)
	repo := auth.NewRepository(f.pool)
	if err := repo.EnsureBuiltIns(f.ctx); err != nil {
		f.done()
		t.Fatal("seed live Manager roles and grants:", err)
	}
	email := "rg140-enrollment-prepare@example.invalid"
	if err := repo.CreateBootstrapSuperAdmin(
		f.ctx, email, "rg140-enrollment-prepare",
		"isolated-test-only-password", "RG-140 Enrollment Test",
	); err != nil {
		f.done()
		t.Fatal(err)
	}
	var id, sessionID string
	if err := f.pool.QueryRow(f.ctx,
		`SELECT id::text FROM users WHERE email=$1`, email,
	).Scan(&id); err != nil {
		f.done()
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(f.ctx, `
INSERT INTO auth_sessions(user_id,token_hash,expires_at)
VALUES($1::uuid,$2,clock_timestamp()+interval '10 minutes') RETURNING id::text
`, id, strings.Repeat("9",64)).Scan(&sessionID); err != nil {
		f.done()
		t.Fatal(err)
	}
	profile, err := repo.ProfileByID(f.ctx, id)
	if err != nil {
		f.done()
		t.Fatal(err)
	}
	principal := auth.AuthenticatedUser{UserProfile: profile, SessionID: sessionID}
	if !auth.CanPrepareRuntimeRecoveryEnrollment(principal) {
		f.done()
		t.Fatal("isolated super_admin does not have dedicated preparation permission")
	}
	return authorizedPreparationFixtureV1{
		recoveryReservationFixture: f, operator: principal,
		prepare: recoveryenroll.NewPreparer(f.pool),
	}
}

func requireAuthorizedPreparationRefusedV1(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, recoveryenroll.ErrEnrollmentPreparationRefused) {
		t.Fatalf("expected safe enrollment preparation refusal, got %v", err)
	}
}

func TestRuntimeRecoveryAuthorizedPreparationV1RecordsOperatorAndAtomicAudit(t *testing.T) {
	f := newAuthorizedPreparationFixtureV1(t)
	defer f.done()
	prepared, err := f.prepare.Prepare(f.ctx,f.agentID,f.generation,f.operator)
	if err != nil { t.Fatal(err) }
	if prepared.ID == "" || prepared.AgentID != f.agentID ||
		prepared.ServerID != f.serverID ||
		prepared.AgentCredentialGeneration != f.generation ||
		prepared.Phase != recoveryenroll.PhasePreparedUnattested ||
		prepared.PreparedAt.IsZero() {
		t.Fatalf("operator-authorized preparation returned wrong untrusted identity: %+v",prepared)
	}
	var appendOnlyCount, attributedAuditCount int
	err = f.pool.QueryRow(f.ctx,`
SELECT
 (SELECT count(*) FROM vpn_runtime_recovery_enrollment_preparation_events
  WHERE preparation_id=$1::uuid AND action='prepared_unattested'),
 (SELECT count(*) FROM audit_events
  WHERE actor_user_id=$2::uuid
    AND action='runtime_recovery.enrollment_prepared_unattested'
    AND resource_type='agent' AND resource_id=$3::uuid
    AND metadata->>'session_id'=$4
    AND metadata->>'preparation_id'=$1::text
    AND metadata->>'phase'='prepared_unattested')
`,prepared.ID,f.operator.ID,f.agentID,f.operator.SessionID).Scan(&appendOnlyCount,&attributedAuditCount)
	if err != nil || appendOnlyCount!=1 || attributedAuditCount!=1 {
		t.Fatalf("preparation and attributed operator audit not committed together: %d/%d %v",
			appendOnlyCount,attributedAuditCount,err)
	}
	// Preparation MUST NOT alter normal legacy Agent polling. No Agent
	// enrollment or protected-mode gate is activated by this method.
	var jobID string
	err=f.pool.QueryRow(f.ctx,`
INSERT INTO config_apply_jobs(server_id,agent_id,config_version_id,action,status)
VALUES($1::uuid,$2::uuid,$3::uuid,'apply','pending') RETURNING id::text
`,f.serverID,f.agentID,f.configVersionID).Scan(&jobID)
	if err!=nil{t.Fatal("preparation unexpectedly blocks normal legacy tasks:",err)}
	if _,err=f.pool.Exec(f.ctx,`
UPDATE config_apply_jobs SET status='in_progress',started_at=now()
WHERE id=$1::uuid`,jobID);err!=nil{t.Fatal("preparation blocked legacy claim:",err)}
}

func TestRuntimeRecoveryAuthorizedPreparationV1LiveAuthorizationCannotBeForged(t *testing.T) {
	for _,mode:=range []string{
		"admin_role_claim","missing_local_grant","missing_local_session",
		"revoked_session","expired_session","role_removed_in_db",
		"permission_removed_in_db","disabled_user_in_db","session_mismatch",
	} {
		t.Run(mode,func(t *testing.T){
			f:=newAuthorizedPreparationFixtureV1(t)
			defer f.done()
			principal:=f.operator
			switch mode {
			case "admin_role_claim":
				principal.Roles=[]string{auth.RoleAdmin}
			case "missing_local_grant":
				principal.Permissions=nil
			case "missing_local_session":
				principal.SessionID=""
			case "revoked_session":
				_,err:=f.pool.Exec(f.ctx,
					`UPDATE auth_sessions SET revoked_at=clock_timestamp() WHERE id=$1::uuid`,principal.SessionID)
				if err!=nil{t.Fatal(err)}
			case "expired_session":
				_,err:=f.pool.Exec(f.ctx,
					`UPDATE auth_sessions SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1::uuid`,principal.SessionID)
				if err!=nil{t.Fatal(err)}
			case "role_removed_in_db":
				_,err:=f.pool.Exec(f.ctx,`
DELETE FROM user_roles
 WHERE user_id=$1::uuid AND role_id=(SELECT id FROM roles WHERE code='super_admin')
`,principal.ID)
				if err!=nil{t.Fatal(err)}
			case "permission_removed_in_db":
				_,err:=f.pool.Exec(f.ctx,`
DELETE FROM role_permissions
 WHERE role_id=(SELECT id FROM roles WHERE code='super_admin')
   AND permission_id=(SELECT id FROM permissions WHERE code='runtime_recovery:enrollment_prepare')
`)
				if err!=nil{t.Fatal(err)}
			case "disabled_user_in_db":
				_,err:=f.pool.Exec(f.ctx,
					`UPDATE users SET status='disabled' WHERE id=$1::uuid`,principal.ID)
				if err!=nil{t.Fatal(err)}
			case "session_mismatch":
				principal.SessionID="aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
			}
			prepared,err:=f.prepare.Prepare(f.ctx,f.agentID,f.generation,principal)
			requireAuthorizedPreparationRefusedV1(t,err)
			if prepared!=(recoveryenroll.Preparation{}) {
				t.Fatal("unauthorized operator received preparation",prepared)
			}
			var n,a int
			if err:=f.pool.QueryRow(f.ctx,`
SELECT (SELECT count(*) FROM vpn_runtime_recovery_enrollment_preparations),
       (SELECT count(*) FROM audit_events
        WHERE action='runtime_recovery.enrollment_prepared_unattested')
`).Scan(&n,&a);err!=nil||n!=0||a!=0{
				t.Fatalf("unauthorized write persisted: %d/%d %v",n,a,err)
			}
		})
	}
}

func TestRuntimeRecoveryAuthorizedPreparationV1RefusesStaleAndBusyNode(t *testing.T) {
	for _,mode:=range []string{"stale_generation","missing_fencing","disabled_agent","active_job","held_reservation","unknown_agent"}{
		t.Run(mode,func(t *testing.T){
			f:=newAuthorizedPreparationFixtureV1(t)
			defer f.done()
			target:=f.agentID
			generation:=f.generation
			switch mode {
			case "stale_generation": generation++
			case "missing_fencing":
				_,err:=f.pool.Exec(f.ctx,
					`UPDATE agents SET capabilities=capabilities-'runtimeMutationFencingV1' WHERE id=$1::uuid`,f.agentID)
				if err!=nil{t.Fatal(err)}
			case "disabled_agent":
				_,err:=f.pool.Exec(f.ctx,
					`UPDATE agents SET status='disabled' WHERE id=$1::uuid`,f.agentID)
				if err!=nil{t.Fatal(err)}
			case "active_job":
				_,err:=f.pool.Exec(f.ctx,`
INSERT INTO agent_operation_jobs(server_id,agent_id,kind,operation,status)
VALUES($1::uuid,$2::uuid,'vpn_core_service','restart','pending')
`,f.serverID,f.agentID)
				if err!=nil{t.Fatal(err)}
			case "held_reservation":
				if err:=f.reserve(f.ctx,f.generation,"config_apply");err!=nil{t.Fatal(err)}
			case "unknown_agent": target="aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
			}
			_,err:=f.prepare.Prepare(f.ctx,target,generation,f.operator)
			requireAuthorizedPreparationRefusedV1(t,err)
			var n int
			if err:=f.pool.QueryRow(f.ctx,
				`SELECT count(*) FROM vpn_runtime_recovery_enrollment_preparations`,
			).Scan(&n);err!=nil||n!=0{t.Fatalf("unsafe preparation persisted: %d %v",n,err)}
		})
	}
}

func TestRuntimeRecoveryAuthorizedPreparationV1AuditFailureRollsBackAll(t *testing.T){
	f:=newAuthorizedPreparationFixtureV1(t)
	defer f.done()
	_,err:=f.pool.Exec(f.ctx,`
CREATE FUNCTION rg140_fail_operator_prep_audit()
RETURNS trigger LANGUAGE plpgsql AS
'BEGIN IF NEW.action = ''runtime_recovery.enrollment_prepared_unattested''
 THEN RAISE EXCEPTION ''injected attributed audit failure'';
 END IF; RETURN NEW; END'
`)
	if err!=nil{t.Fatal(err)}
	_,err=f.pool.Exec(f.ctx,`
CREATE TRIGGER rg140_fail_operator_prep_audit
BEFORE INSERT ON audit_events
FOR EACH ROW EXECUTE FUNCTION rg140_fail_operator_prep_audit()
`)
	if err!=nil{t.Fatal(err)}
	if prepared,err:=f.prepare.Prepare(f.ctx,f.agentID,f.generation,f.operator);
		!errors.Is(err,recoveryenroll.ErrEnrollmentPreparationRefused) ||
		prepared!=(recoveryenroll.Preparation{}) {
		t.Fatalf("audit failure disclosed success: %+v %v",prepared,err)
	}
	var n,events,audit int
	if err:=f.pool.QueryRow(f.ctx,`
SELECT (SELECT count(*) FROM vpn_runtime_recovery_enrollment_preparations),
       (SELECT count(*) FROM vpn_runtime_recovery_enrollment_preparation_events),
       (SELECT count(*) FROM audit_events
        WHERE action='runtime_recovery.enrollment_prepared_unattested')
`).Scan(&n,&events,&audit);err!=nil||n!=0||events!=0||audit!=0{
		t.Fatalf("partial preparation survived audit failure: %d/%d/%d %v",n,events,audit,err)
	}
}

func TestRuntimeRecoveryAuthorizedPreparationV1ConcurrentOneWinner(t *testing.T) {
	f:=newAuthorizedPreparationFixtureV1(t)
	defer f.done()
	ctx,cancel:=context.WithTimeout(f.ctx,12*time.Second)
	defer cancel()
	start:=make(chan struct{})
	out:=make(chan error,2)
	var wg sync.WaitGroup
	for i:=0;i<2;i++ {
		wg.Add(1)
		go func(){
			defer wg.Done()
			<-start
			_,err:=f.prepare.Prepare(ctx,f.agentID,f.generation,f.operator)
			out<-err
		}()
	}
	close(start);wg.Wait();close(out)
	success,refused:=0,0
	for err:=range out {
		if err==nil{success++}else if errors.Is(err,recoveryenroll.ErrEnrollmentPreparationRefused){refused++}else{
			t.Fatalf("unexpected competing prepare: %v",err)
		}
	}
	if success!=1||refused!=1{t.Fatalf("enrollment history inserted twice: success=%d refused=%d",success,refused)}
	var n,events,audit int
	if err:=f.pool.QueryRow(f.ctx,`
SELECT (SELECT count(*) FROM vpn_runtime_recovery_enrollment_preparations),
       (SELECT count(*) FROM vpn_runtime_recovery_enrollment_preparation_events),
       (SELECT count(*) FROM audit_events WHERE action='runtime_recovery.enrollment_prepared_unattested')
`).Scan(&n,&events,&audit);err!=nil||n!=1||events!=1||audit!=1{
		t.Fatalf("concurrent preparation/audit inconsistent: %d/%d/%d %v",n,events,audit,err)
	}
}

func TestRuntimeRecoveryAuthorizedPreparationV1MalformedAndCancelledRefused(t *testing.T) {
	f:=newAuthorizedPreparationFixtureV1(t)
	defer f.done()
	for _,tc:=range []struct{
		name string; ctx context.Context; agentID string; generation int64
	}{
		{"nil_context",nil,f.agentID,f.generation},
		{"cancelled_context",func() context.Context{ctx,cancel:=context.WithCancel(f.ctx);cancel();return ctx}(),f.agentID,f.generation},
		{"invalid_uuid",f.ctx,"not-an-agent",f.generation},
		{"zero_generation",f.ctx,f.agentID,0},
	} {
		t.Run(tc.name,func(t *testing.T){
			prepared,err:=f.prepare.Prepare(tc.ctx,tc.agentID,tc.generation,f.operator)
			if prepared!=(recoveryenroll.Preparation{}){t.Fatal("invalid input got receipt",prepared)}
			requireAuthorizedPreparationRefusedV1(t,err)
		})
	}
}
