package tasks

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func recoveryJournalExpectedFixtureV1(t *testing.T, events []recoveryJournalEventV1) recoveryJournalManagerExpectationV1 {
	t.Helper()
	event := events[0]
	return recoveryJournalManagerExpectationV1{
		AttemptEpoch: event.AttemptEpoch,
		TaskID: event.TaskID,
		AgentID: event.AgentID,
		ServerID: event.ServerID,
		AgentCredentialGeneration: event.AgentCredentialGeneration,
		MarkerSHA256: event.MarkerSHA256,
	}
}

func TestRuntimeRecoveryJournalOfflinePreflightV1EveryPrefixStaysUnattested(t *testing.T) {
	events := syntheticRecoveryJournalV1(t)
	dir := recoveryJournalDiskFixtureV1(t)
	expected := recoveryJournalExpectedFixtureV1(t, events)
	if observed, err := inspectRecoveryJournalAgainstManagerExpectationV1(
		context.Background(), dir, expected,
	); !errors.Is(err, errRecoveryJournalUntrustedV1) ||
		observed != (recoveryJournalOfflinePreflightV1{}) {
		t.Fatalf("empty journal was accepted: %+v %v", observed, err)
	}
	for i, event := range events {
		if err := appendRecoveryJournalDiskV1(dir, event, nil); err != nil {
			t.Fatal(err)
		}
		before, err := os.ReadFile(filepath.Join(dir, recoveryJournalDiskNameV1(i+1)))
		if err != nil { t.Fatal(err) }
		result, err := inspectRecoveryJournalAgainstManagerExpectationV1(
			context.Background(), dir, expected,
		)
		if err != nil || !result.Unattested || result.EventsSeen != i+1 ||
			result.LastPhase != event.Phase || !validRemovalHash(result.ObservedHeadSHA256) {
			t.Fatalf("valid journal prefix was promoted or rejected: %+v %v", result, err)
		}
		after, err := os.ReadFile(filepath.Join(dir, recoveryJournalDiskNameV1(i+1)))
		if err != nil || !bytes.Equal(before, after) {
			t.Fatal("read-only preflight modified persisted journal", err)
		}
		// No lock survives the historical returned value.
		held, err := openRecoveryJournalDiskV1(dir)
		if err != nil { t.Fatal("read-only preflight leaked directory flock", err) }
		if err := held.Close(); err != nil { t.Fatal(err) }
	}
}

func TestRuntimeRecoveryJournalOfflinePreflightV1RejectsExpectedIdentityDrift(t *testing.T) {
	events := syntheticRecoveryJournalV1(t)
	dir := recoveryJournalDiskFixtureV1(t)
	for _, event := range events {
		if err := appendRecoveryJournalDiskV1(dir, event, nil); err != nil { t.Fatal(err) }
	}
	initial := recoveryJournalExpectedFixtureV1(t, events)
	mutations := map[string]func(*recoveryJournalManagerExpectationV1){
		"missing_epoch": func(x *recoveryJournalManagerExpectationV1){x.AttemptEpoch=0},
		"wrong_epoch": func(x *recoveryJournalManagerExpectationV1){x.AttemptEpoch++},
		"missing_generation": func(x *recoveryJournalManagerExpectationV1){x.AgentCredentialGeneration=0},
		"stale_generation": func(x *recoveryJournalManagerExpectationV1){x.AgentCredentialGeneration++},
		"wrong_task": func(x *recoveryJournalManagerExpectationV1){x.TaskID=x.AgentID},
		"wrong_agent": func(x *recoveryJournalManagerExpectationV1){x.AgentID=x.ServerID},
		"wrong_server": func(x *recoveryJournalManagerExpectationV1){x.ServerID=x.AgentID},
		"wrong_marker": func(x *recoveryJournalManagerExpectationV1){x.MarkerSHA256=strings.Repeat("f",64)},
		"malformed_marker": func(x *recoveryJournalManagerExpectationV1){x.MarkerSHA256="not-a-digest"},
	}
	for name, mutate := range mutations {
		t.Run(name,func(t *testing.T){
			expectation := initial
			mutate(&expectation)
			observed, err := inspectRecoveryJournalAgainstManagerExpectationV1(
				context.Background(), dir, expectation,
			)
			if !errors.Is(err, errRecoveryJournalUntrustedV1) ||
				observed != (recoveryJournalOfflinePreflightV1{}) {
				t.Fatalf("stale Manager tuple was accepted: %+v %v",observed,err)
			}
			if held, err := openRecoveryJournalDiskV1(dir); err != nil {
				t.Fatal("refused preflight leaked directory flock",err)
			} else {_ = held.Close()}
		})
	}
}

func TestRuntimeRecoveryJournalOfflinePreflightV1MissingCorruptBusyAndCancelled(t *testing.T) {
	events := syntheticRecoveryJournalV1(t)
	expected := recoveryJournalExpectedFixtureV1(t,events)
	cases := []string{"missing","partial","symlink","busy","cancelled","nil_context"}
	for _, mode := range cases {
		t.Run(mode,func(t *testing.T){
			dir := recoveryJournalDiskFixtureV1(t)
			if mode=="missing" {
				dir=filepath.Join(dir,"not_enrolled")
			} else if mode=="partial" {
				if err:=os.WriteFile(filepath.Join(dir,"event-000001.json"),[]byte("{"),0600);err!=nil{t.Fatal(err)}
			} else if mode=="symlink" {
				if err:=os.Symlink(filepath.Join(dir,"elsewhere"),filepath.Join(dir,"event-000001.json"));err!=nil{t.Fatal(err)}
			} else {
				if err:=appendRecoveryJournalDiskV1(dir,events[0],nil);err!=nil{t.Fatal(err)}
			}

			ctx:=context.Background()
			if mode=="cancelled" {
				c,cancel:=context.WithCancel(ctx);cancel();ctx=c
			}
			if mode=="nil_context" {ctx=nil}
			var held *os.File
			if mode=="busy"{
				var err error
				held,err=openRecoveryJournalDiskV1(dir)
				if err!=nil{t.Fatal(err)}
				defer held.Close()
			}
			observed,err:=inspectRecoveryJournalAgainstManagerExpectationV1(ctx,dir,expected)
			if !errors.Is(err,errRecoveryJournalUntrustedV1)||
				observed!=(recoveryJournalOfflinePreflightV1{}){
				t.Fatalf("unsafe local journal accepted: %+v %v",observed,err)
			}
		})
	}
}

// An attacker who can replace the journal and recompute its hash chain can
// also fake the "expected" tuple supplied to a private helper. A match must
// ALWAYS remain explicitly unattested: byte consistency is not host truth.
func TestRuntimeRecoveryJournalOfflinePreflightV1ForgedConsistentStillUnattested(t *testing.T){
	dir:=recoveryJournalDiskFixtureV1(t)
	events:=syntheticRecoveryJournalV1(t)
	forged:=recoveryJournalExpectedFixtureV1(t,events)
	forged.MarkerSHA256=strings.Repeat("e",64)
	previous:=strings.Repeat("0",64)
	for i:=range events{
		events[i].MarkerSHA256=forged.MarkerSHA256
		events[i].PreviousSHA256=previous
		digest,err:=recoveryJournalEventDigestV1(events[i])
		if err!=nil{t.Fatal(err)}
		previous=digest
		if err:=appendRecoveryJournalDiskV1(dir,events[i],nil);err!=nil{t.Fatal(err)}
	}
	obs,err:=inspectRecoveryJournalAgainstManagerExpectationV1(context.Background(),dir,forged)
	if err!=nil||!obs.Unattested||obs.EventsSeen!=len(events){
		t.Fatalf("forged but internally consistent chain got promoted: %+v %v",obs,err)
	}
}
