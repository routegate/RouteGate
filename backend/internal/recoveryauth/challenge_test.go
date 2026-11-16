package recoveryauth

import (
    "bytes"
    "encoding/json"
    "errors"
    "strings"
    "testing"
    "time"

    "github.com/ikaevus/routegate/backend/internal/auth"
)

const (
    reservationID = "11111111-1111-4111-8111-111111111111"
    serverID = "22222222-2222-4222-8222-222222222222"
    agentID = "33333333-3333-4333-8333-333333333333"
    jobID = "44444444-4444-4444-8444-444444444444"
    operatorID = "55555555-5555-4555-8555-555555555555"
    sessionID = "66666666-6666-4666-8666-666666666666"
)

func fixtureReservation() Reservation {
    return Reservation{
        ID: reservationID, ServerID: serverID, AgentID: agentID, JobID: jobID,
        JobKind: "vpn_core_service", AgentCredentialGeneration: 3,
        MutationIntentSHA256: strings.Repeat("a",64),
        ResultReceiptSHA256: strings.Repeat("b",64),
        RuntimeWitnessSHA256: strings.Repeat("c",64),
    }
}
func fixtureOperator() auth.AuthenticatedUser {
    return auth.AuthenticatedUser{
        UserProfile: auth.UserProfile{
            ID: operatorID, UserType: auth.UserTypeHuman, Status:"active",
            Roles: []string{auth.RoleSuperAdmin},
            Permissions: []string{auth.PermissionRuntimeRecoveryAuthorize},
        }, SessionID: sessionID,
    }
}
func TestRecoveryChallengeBindsReservationAndHumanSession(t *testing.T) {
    now := time.Date(2026,11,12,3,5,0,0,time.UTC)
    subject := fixtureReservation()
    operator := fixtureOperator()
    secret, commitment, err := buildChallengeWithEntropy(operator,subject,now,bytes.NewReader(bytes.Repeat([]byte{0xa4},challengeBytes)))
    if err != nil {t.Fatal(err)}
    if len(secret)!=43 || commitment.ExpiresAt.Sub(commitment.IssuedAt)!=90*time.Second {
        t.Fatalf("wrong entropy length or expiry: token=%d expiry=%s",len(secret),commitment.ExpiresAt.Sub(commitment.IssuedAt))
    }
    serialized,_:=json.Marshal(commitment)
    if bytes.Contains(serialized,[]byte(secret)) || bytes.Contains(serialized,[]byte{0xa4,0xa4,0xa4,0xa4}) {
        t.Fatal("secret leaked into persistable commitment")
    }
    if !MatchesBoundChallenge(secret,commitment,operator,subject,now.Add(89*time.Second)) {
        t.Fatal("valid bound challenge rejected before expiry")
    }
    if MatchesBoundChallenge(secret,commitment,operator,subject,now.Add(90*time.Second)) {
        t.Fatal("expired challenge accepted at boundary")
    }
    if MatchesBoundChallenge(secret,commitment,operator,subject,now.Add(-time.Second)) {
        t.Fatal("future challenge accepted")
    }

    otherToken,otherCommitment,err:=buildChallengeWithEntropy(operator,subject,now,bytes.NewReader(bytes.Repeat([]byte{0xa5},challengeBytes)))
    if err!=nil || otherToken==secret || otherCommitment.ChallengeSHA256==commitment.ChallengeSHA256 {
        t.Fatal("challenge reused randomness")
    }
    if MatchesBoundChallenge(otherToken,commitment,operator,subject,now) {
        t.Fatal("foreign challenge accepted")
    }
    if MatchesBoundChallenge(secret,otherCommitment,operator,subject,now) {
        t.Fatal("old challenge accepted under another commitment")
    }
}

func TestRecoveryChallengeRejectsIdentityOrEvidenceDrift(t *testing.T) {
    now := time.Date(2026,11,12,3,5,0,0,time.UTC)
    r:=fixtureReservation()
    actor:=fixtureOperator()
    token,c,err:=buildChallengeWithEntropy(actor,r,now,bytes.NewReader(bytes.Repeat([]byte{0xcc},challengeBytes)))
    if err!=nil {t.Fatal(err)}
    cases:=map[string]func(*Reservation,*auth.AuthenticatedUser,*Commitment){
        "different_reservation":func(r *Reservation,_ *auth.AuthenticatedUser,_ *Commitment){r.ID=jobID},
        "different_node":func(r *Reservation,_ *auth.AuthenticatedUser,_ *Commitment){r.ServerID=reservationID},
        "different_agent":func(r *Reservation,_ *auth.AuthenticatedUser,_ *Commitment){r.AgentID=serverID},
        "different_job":func(r *Reservation,_ *auth.AuthenticatedUser,_ *Commitment){r.JobID=agentID},
        "different_kind":func(r *Reservation,_ *auth.AuthenticatedUser,_ *Commitment){r.JobKind="config_apply"},
        "rotated_credentials":func(r *Reservation,_ *auth.AuthenticatedUser,_ *Commitment){r.AgentCredentialGeneration++},
        "changed_intent":func(r *Reservation,_ *auth.AuthenticatedUser,_ *Commitment){r.MutationIntentSHA256=strings.Repeat("e",64)},
        "changed_receipt":func(r *Reservation,_ *auth.AuthenticatedUser,_ *Commitment){r.ResultReceiptSHA256=strings.Repeat("d",64)},
        "changed_witness":func(r *Reservation,_ *auth.AuthenticatedUser,_ *Commitment){r.RuntimeWitnessSHA256=strings.Repeat("f",64)},
        "different_operator":func(_ *Reservation,u *auth.AuthenticatedUser,_ *Commitment){u.ID=agentID},
        "different_session":func(_ *Reservation,u *auth.AuthenticatedUser,_ *Commitment){u.SessionID=agentID},
        "disabled_operator":func(_ *Reservation,u *auth.AuthenticatedUser,_ *Commitment){u.Status="disabled"},
        "operator_role_lost":func(_ *Reservation,u *auth.AuthenticatedUser,_ *Commitment){u.Roles=[]string{auth.RoleAdmin}},
        "forged_agent_operator":func(_ *Reservation,u *auth.AuthenticatedUser,_ *Commitment){u.UserType="agent"},
        "permission_removed":func(_ *Reservation,u *auth.AuthenticatedUser,_ *Commitment){u.Permissions=nil},
        "tampered_digest":func(_ *Reservation,_ *auth.AuthenticatedUser,c *Commitment){c.ContextSHA256=strings.Repeat("0",64)},
        "tampered_expiry":func(_ *Reservation,_ *auth.AuthenticatedUser,c *Commitment){c.ExpiresAt=c.ExpiresAt.Add(3*time.Minute)},
        "tampered_session_binding":func(_ *Reservation,_ *auth.AuthenticatedUser,c *Commitment){c.SessionID=agentID},
    }
    for name,change:=range cases {
        t.Run(name,func(t *testing.T) {
            candidate:=r
            user:=actor
            bound:=c
            change(&candidate,&user,&bound)
            if MatchesBoundChallenge(token,bound,user,candidate,now.Add(time.Second)) {
                t.Fatal("tampered challenge was accepted")
            }
        })
    }
    if MatchesBoundChallenge("invalid-encoding!",c,actor,r,now) || MatchesBoundChallenge("",c,actor,r,now) {
        t.Fatal("invalid token accepted")
    }
}

func TestRecoveryChallengeRefusesBadInputAndEntropyFailures(t *testing.T) {
    now := time.Date(2026,11,12,3,5,0,0,time.UTC)
    r:=fixtureReservation()
    actor:=fixtureOperator()
    for name,mutate:=range map[string]func(*Reservation,*auth.AuthenticatedUser){
        "missing_reservation":func(r *Reservation,_ *auth.AuthenticatedUser){r.ID=""},
        "uppercase_hash":func(r *Reservation,_ *auth.AuthenticatedUser){r.RuntimeWitnessSHA256=strings.Repeat("A",64)},
        "non_positive_generation":func(r *Reservation,_ *auth.AuthenticatedUser){r.AgentCredentialGeneration=0},
        "invalid_job_kind":func(r *Reservation,_ *auth.AuthenticatedUser){r.JobKind="credential_removal"},
        "missing_operator_session":func(_ *Reservation,a *auth.AuthenticatedUser){a.SessionID=""},
        "non_uuid_session":func(_ *Reservation,a *auth.AuthenticatedUser){a.SessionID="session"},
        "admin_with_permission":func(_ *Reservation,a *auth.AuthenticatedUser){a.Roles=[]string{auth.RoleAdmin}},
    } {
        t.Run(name,func(t *testing.T){
            candidate:=r
            user:=actor
            mutate(&candidate,&user)
            token,commitment,err:=buildChallengeWithEntropy(user,candidate,now,bytes.NewReader(bytes.Repeat([]byte{2},challengeBytes)))
            if !errors.Is(err,ErrAuthorizationRefused)||token!=""||commitment.SchemaVersion!=0 {
                t.Fatalf("bad input produced challenge: %+v, %v",commitment,err)
            }
        })
    }
    _,_,err:=buildChallengeWithEntropy(actor,r,now,bytes.NewReader([]byte{1}))
    if !errors.Is(err,ErrAuthorizationRefused) {
        t.Fatal("short entropy read accepted")
    }
}
