package tasks

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const resultTaskID = "11111111-1111-4111-8111-111111111111"

var resultBinding = RuntimeResultBinding{ManagerURL: "https://isolated.invalid", AgentID: "22222222-2222-4222-8222-222222222222", ServerID: "33333333-3333-4333-8333-333333333333"}
var resultEnvelope = []byte(`{"status":"succeeded","resultPayload":{"counter":9007199254740993,"output":"historical"}}`)

func resultFixture(t *testing.T) (string, *RuntimeMutation) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "state")
	lease, err := BeginRuntimeMutation(dir, resultTaskID, TaskKindVPNCoreService)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { lease.Close() })
	if err := lease.SaveResult(resultBinding, resultTaskID, resultEnvelope); err != nil {
		t.Fatal(err)
	}
	return dir, lease
}

func TestRuntimeMutationResultReplay(t *testing.T) {
	dir, lease := resultFixture(t)
	calls := 0
	deliver := func(id string, data []byte) error {
		calls++
		if id != resultTaskID || !bytes.Equal(data, resultEnvelope) {
			t.Fatal("result changed")
		}
		if other, err := BeginRuntimeMutation(dir, resultTaskID, TaskKindVPNCoreService); err == nil {
			other.Close()
			t.Fatal("delivery did not hold exclusive lock")
		}
		return nil
	}
	if ReplayRuntimeResult(dir, resultBinding, deliver) == nil || calls != 0 {
		t.Fatal("replayed while live owner held lock")
	}
	lease.Close()
	if err := ReplayRuntimeResult(dir, resultBinding, func(string, []byte) error { return errors.New("lost response") }); err == nil {
		t.Fatal("lost acknowledgement accepted")
	}
	for range 2 {
		if err := ReplayRuntimeResult(dir, resultBinding, deliver); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatal("acknowledged receipt delivered again")
	}
	if runtimeMarkerAbsent(filepath.Join(dir, "mutation-inflight.json")) {
		t.Fatal("replay cleared runtime uncertainty")
	}
	if other, err := BeginRuntimeMutation(dir, resultTaskID, TaskKindVPNCoreService); err == nil {
		other.Close()
		t.Fatal("replay admitted runtime mutation")
	}
}

func TestRuntimeMutationResultUnsafeState(t *testing.T) {
	for _, mode := range []string{"manager", "agent", "server", "corrupt", "marker", "missing marker", "permissions", "symlink", "removal"} {
		t.Run(mode, func(t *testing.T) {
			dir, lease := resultFixture(t)
			lease.Close()
			binding := resultBinding
			path := filepath.Join(dir, runtimeResultFile)
			switch mode {
			case "manager":
				binding.ManagerURL += "/other"
			case "agent":
				binding.AgentID = resultTaskID
			case "server":
				binding.ServerID = resultTaskID
			case "corrupt":
				os.WriteFile(path, []byte("{"), 0600)
			case "marker":
				os.WriteFile(filepath.Join(dir, "mutation-inflight.json"), []byte("{}"), 0600)
			case "missing marker":
				os.Remove(filepath.Join(dir, "mutation-inflight.json"))
			case "permissions":
				os.Chmod(path, 0644)
			case "symlink":
				os.Rename(path, path+".original")
				os.Symlink(path+".original", path)
			case "removal":
				os.WriteFile(filepath.Join(dir, "inflight.json"), []byte("unknown"), 0600)
			}
			if ReplayRuntimeResult(dir, binding, func(string, []byte) error { t.Fatal("unsafe receipt delivered"); return nil }) == nil {
				t.Fatal("unsafe receipt accepted")
			}
		})
	}
}

func TestRuntimeMutationResultLiveCompletion(t *testing.T) {
	dir, lease := resultFixture(t)
	if lease.SaveResult(resultBinding, resultTaskID, []byte(`{"status":"succeeded"}`)) == nil {
		t.Fatal("overwrote receipt")
	}
	if err := lease.Complete(); err != nil {
		t.Fatal(err)
	}
	if !runtimeMarkerAbsent(filepath.Join(dir, runtimeResultFile)) || !runtimeMarkerAbsent(filepath.Join(dir, "mutation-inflight.json")) {
		t.Fatal("live completion retained state")
	}
	lease.Close()
	other, err := BeginRuntimeMutation(dir, resultTaskID, TaskKindVPNCoreService)
	if err != nil {
		t.Fatal(err)
	}
	other.Close()
}

func TestRuntimeMutationResultInvalidEnvelope(t *testing.T) {
	for _, data := range [][]byte{[]byte(`{"status":"failed"}`), []byte(`{"status":"succeeded","errorMessage":"uncertain"}`), []byte(strings.Repeat("x", runtimeResultLimit)), []byte("null")} {
		dir := filepath.Join(t.TempDir(), "state")
		lease, err := BeginRuntimeMutation(dir, resultTaskID, TaskKindVPNCoreService)
		if err != nil {
			t.Fatal(err)
		}
		if lease.SaveResult(resultBinding, resultTaskID, data) == nil {
			t.Fatal("invalid envelope persisted")
		}
		lease.Close()
		if !runtimeMarkerAbsent(filepath.Join(dir, runtimeResultFile)) {
			t.Fatal("invalid receipt exists")
		}
	}
}
