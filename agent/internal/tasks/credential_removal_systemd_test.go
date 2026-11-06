package tasks

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// RunCredentialRemovalSystemdRealityFixture lets the external test drive the
// first removal through the Agent HTTP client without a package import cycle.
// The remaining phases continue to exercise the executor and recovery directly.
func RunCredentialRemovalSystemdRealityFixture(t *testing.T, executeViaHTTP func(*testing.T, context.Context, ConfigTask, CredentialRemovalExecutor) (CredentialRemovalReport, error)) {
	binary := os.Getenv("ROUTEGATE_TEST_SING_BOX")
	if os.Getenv("ROUTEGATE_TEST_REMOVAL_SYSTEMD") != "1" || binary == "" {
		t.Skip("requires disposable systemd runner and pinned sing-box")
	}
	if os.Geteuid() != 0 {
		t.Fatal("isolated systemd test requires root")
	}
	for _, phase := range []string{"terminal_history", "prepared_release_intent", "prepared_release_submission", "cleaned_sources", "cleaned_commit_intent", "admission_release"} {
		t.Run(phase, func(t *testing.T) {
			testCredentialRemovalSystemdRealityTraffic(t, binary, phase != "terminal_history", phase == "prepared_release_submission" || phase == "cleaned_sources" || phase == "cleaned_commit_intent" || phase == "admission_release", phase == "cleaned_sources" || phase == "cleaned_commit_intent" || phase == "admission_release", phase == "cleaned_commit_intent" || phase == "admission_release", phase == "admission_release", executeViaHTTP)
		})
	}
}

func testCredentialRemovalSystemdRealityTraffic(t *testing.T, binary string, prepared, submitted, cleanup, commitIntent, release bool, executeViaHTTP func(*testing.T, context.Context, ConfigTask, CredentialRemovalExecutor) (CredentialRemovalReport, error)) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/stream" {
			io.WriteString(w, "open")
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		}
		io.WriteString(w, "isolated-removal-origin")
	}))
	defer origin.Close()
	target := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "handshake-only") }))
	target.TLS = &tls.Config{MinVersion: tls.VersionTLS13, MaxVersion: tls.VersionTLS13, CurvePreferences: []tls.CurveID{tls.X25519}}
	target.StartTLS()
	defer target.Close()
	private, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serverPort := removalFreePort(t)
	e, task, _, baseline := removalExecutorFixture(t)
	object, _, _, _ := canonicalRemovalJSON(baseline)
	inbound := object["inbounds"].([]any)[0].(map[string]any)
	object["inbounds"] = []any{inbound}
	inbound["listen_port"] = serverPort
	users := inbound["users"].([]any)
	for _, user := range users {
		user.(map[string]any)["flow"] = "xtls-rprx-vision"
	}
	inbound["tls"] = map[string]any{"enabled": true, "server_name": "example.com", "reality": map[string]any{"enabled": true, "private_key": base64.RawURLEncoding.EncodeToString(private.Bytes()), "short_id": []string{"0123456789abcdef"}, "handshake": map[string]any{"server": "127.0.0.1", "server_port": target.Listener.Addr().(*net.TCPAddr).Port}}}
	baseline = removalMarshal(t, object)
	_, _, task.CredentialRemoval.BaselineRuntimeHash, _ = canonicalRemovalJSON(baseline)
	if err = os.WriteFile(e.activePath, baseline, 0600); err != nil {
		t.Fatal(err)
	}
	unit := startRemovalTestUnit(t, ctx, binary, e.activePath)
	adapter := NewSingBoxVLESSAdapter(filepath.Dir(e.activePath), binary, unit)
	if _, err = adapter.CheckHealth(ctx, e.activePath); err != nil {
		t.Fatal(err)
	}
	e.adapter = adapter
	proxies := []int{removalFreePort(t), removalFreePort(t)}
	for i, user := range users {
		client := map[string]any{"log": map[string]any{"level": "error"}, "inbounds": []any{map[string]any{"type": "mixed", "listen": "127.0.0.1", "listen_port": proxies[i]}}, "outbounds": []any{map[string]any{"type": "vless", "tag": "vpn", "server": "127.0.0.1", "server_port": serverPort, "uuid": user.(map[string]any)["uuid"], "flow": "xtls-rprx-vision", "tls": map[string]any{"enabled": true, "server_name": "example.com", "utls": map[string]any{"enabled": true, "fingerprint": "chrome"}, "reality": map[string]any{"enabled": true, "public_key": base64.RawURLEncoding.EncodeToString(private.PublicKey().Bytes()), "short_id": "0123456789abcdef"}}}}, "route": map[string]any{"final": "vpn"}}
		removalStartSingBox(t, ctx, binary, client, proxies[i])
		removalWaitTraffic(t, ctx, proxies[i], origin.URL)
	}
	streams := []removalObservedStream{}
	for _, port := range proxies {
		proxy := &url.URL{Scheme: "http", Host: fmt.Sprintf("127.0.0.1:%d", port)}
		transport := &http.Transport{Proxy: http.ProxyURL(proxy)}
		defer transport.CloseIdleConnections()
		request, _ := http.NewRequestWithContext(ctx, http.MethodGet, origin.URL+"/stream", nil)
		started := time.Now()
		response, err := (&http.Client{Transport: transport, Timeout: removalStreamClientTimeout}).Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		first := make([]byte, 4)
		if _, err = io.ReadFull(response.Body, first); err != nil || string(first) != "open" {
			t.Fatal("stream did not open", err)
		}
		done := make(chan removalStreamTermination, 1)
		go func(body io.Reader) {
			_, readErr := io.ReadAll(body)
			done <- removalStreamTermination{err: readErr, contextErr: ctx.Err(), finished: time.Now()}
		}(response.Body)
		streams = append(streams, removalObservedStream{started: started, done: done})
	}
	inbound["users"] = users[1:]
	bindRemovalCandidate(t, &task, object)
	for _, stream := range streams {
		select {
		case <-stream.done:
			t.Fatal("old session closed before credential removal began")
		default:
		}
	}
	mutationStarted := time.Now()
	var report CredentialRemovalReport
	if prepared {
		report, err = e.Execute(ctx, task)
	} else {
		report, err = executeViaHTTP(t, ctx, task, e)
	}
	if err != nil {
		t.Fatal(report, err)
	}
	t.Log("executor measured new systemd invocation, exact active digest and listener")
	replay, err := e.Execute(ctx, task)
	if err != nil || replay != report {
		t.Fatal("lost-ack replay", replay, err)
	}
	for _, stream := range streams {
		select {
		case result := <-stream.done:
			if err := result.validate(stream.started, mutationStarted); err != nil {
				t.Fatal(err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("shared-process old session remained open")
		}
	}
	t.Log("already-open streams for both accounts closed on shared process restart")
	removalWaitTraffic(t, ctx, proxies[1], origin.URL)
	if removalTraffic(ctx, proxies[0], origin.URL) {
		t.Fatal("removed cached UUID authenticated")
	}
	t.Log("removed UUID rejected; unrelated account passes real Reality traffic")
	task.CredentialRemoval.BaselineRuntimeHash = report.ActiveRuntimeHash
	task.CredentialRemoval.BaselineVersionID = task.ConfigVersionID
	task.ConfigVersionID = "99999999-9999-4999-8999-999999999999"
	task.ID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	task.CredentialRemoval.OperationID = task.ID
	task.CredentialRemoval.AccountID = users[1].(map[string]any)["name"].(string)
	task.CredentialRemoval.VLESSUUID = users[1].(map[string]any)["uuid"].(string)
	inbound["users"] = []any{}
	bindRemovalCandidate(t, &task, object)
	report, err = e.Execute(ctx, task)
	if err != nil {
		t.Fatal(report, err)
	}
	if removalTraffic(ctx, proxies[0], origin.URL) || removalTraffic(ctx, proxies[1], origin.URL) {
		t.Fatal("zero-user runtime accepted UUID")
	}
	t.Log("last-account removal retains listener and rejects both cached UUIDs")
	// A historical result must not become unlock permission after a restart.
	// Reuse only this disposable unit to exercise the production observer.
	// Keep completed scoped-removal receipts intact in their original directory.
	// The recovery readiness regression uses a separate private fence root;
	// unrelated receipts in that root must fail closed, never be discarded.
	removalReceipts := e.receiptDir
	beforeRemovalReceipts := terminalHistoryTree(t, removalReceipts)
	e.receiptDir = filepath.Join(t.TempDir(), "runtime-mutations")
	lease, err := BeginRuntimeMutation(e.receiptDir, task.ID, TaskKindVPNCoreService)
	if err != nil {
		t.Fatal(err)
	}
	previous, err := CaptureRuntimeRecoveryWitness(ctx, e.activePath, adapter)
	if err != nil {
		lease.Close()
		t.Fatal(err)
	}
	if _, err := adapter.Restart(ctx); err != nil {
		lease.Close()
		t.Fatal(err)
	}
	witness, err := CaptureRuntimeRecoveryWitness(ctx, e.activePath, adapter)
	if err != nil {
		lease.Close()
		t.Fatal(err)
	}
	witness.PreviousGeneration = previous.Generation
	binding := RuntimeResultBinding{ManagerURL: "https://isolated.invalid", AgentID: task.AgentID, ServerID: task.ServerID}
	if err := lease.SaveResultWithRuntimeWitness(binding, task.ID, []byte(`{"status":"succeeded","resultPayload":{"kind":"vpn_core_service","operation":"restart"}}`), &witness); err != nil {
		lease.Close()
		t.Fatal(err)
	}
	lease.Close()
	if err := ReplayRuntimeResult(e.receiptDir, binding, func(string, []byte) error { return nil }); err != nil {
		t.Fatal(err)
	}
	observation, err := ObserveRuntimeRecovery(ctx, e.receiptDir, binding, task.ID, e.activePath, adapter)
	if err != nil || observation.Witness != witness {
		t.Fatal("production runtime witness did not match", err)
	}
	now := time.Now().UTC()
	grant := RuntimeRecoveryMutationGrant{1, runtimeRecoveryGrantPurpose, task.ID, strings.Repeat("d", 64), 7, strings.Repeat("e", 64), strings.Repeat("f", 64), now.Add(-time.Second), now.Add(time.Minute), observation}
	grantData, _ := json.Marshal(grant)
	commit, err := BeginRuntimeRecoveryCommit(ctx, e.receiptDir, RuntimeRecoveryGrantBinding{RuntimeResultBinding: binding, AgentCredentialGeneration: 7}, grantData, e.activePath, adapter)
	if err != nil {
		t.Fatal("real process commit observation failed", err)
	}
	defer commit.Close()
	proof, err := commit.Proof(ctx)
	if err != nil || proof.Observation != observation {
		t.Fatal("real locked commit proof mismatch", err)
	}
	if lock, err := lockRuntimeMutation(e.receiptDir); err == nil {
		lock.Close()
		t.Fatal("real commit observation dropped lock")
	}
	if err = commit.WithSubmission(ctx, RuntimeRecoveryGrantBinding{RuntimeResultBinding: binding, AgentCredentialGeneration: 7}, markerDigest(grantData), func(g *RuntimeRecoveryCommitGuard) error {
		p, e := g.PinProof(ctx)
		if e != nil {
			return e
		}
		p2, e := g.PinProof(ctx)
		if e != nil || p != p2 {
			return ErrRuntimeMutationBlocked
		}
		return g.RecordReceipt(ctx, receiptForRecoveryProof(p))
	}); err != nil {
		t.Fatal("real process submission pin failed", err)
	}
	commit.Close()
	bound := RuntimeRecoveryGrantBinding{RuntimeResultBinding: binding, AgentCredentialGeneration: 7}
	assertHistoricalRecoveryAfterExit(t, e.receiptDir, bound)
	pinnedData, err := os.ReadFile(filepath.Join(e.receiptDir, runtimeRecoverySubmissionFile))
	var pinned RuntimeRecoveryCommitProof
	if err != nil || json.Unmarshal(pinnedData, &pinned) != nil {
		t.Fatal("real pinned proof", err)
	}
	authorization := finalAuthorizationForProof(pinned, 7)
	authorizationData, _ := json.Marshal(authorization)
	final, err := BeginRuntimeRecoveryFinalization(ctx, e.receiptDir, bound, authorizationData, e.activePath, adapter)
	if err != nil {
		t.Fatal("real final consumption failed", err)
	}
	defer final.Close()
	if proof, err := final.Proof(ctx); err != nil || proof.Observation != observation || proof.FinalConsumptionHash == "" {
		t.Fatal("real fresh final proof", err)
	}
	// Synthetic historical Manager receipt; this is a real-runtime regression,
	// not Manager+Agent HTTP finalization E2E.
	if err := final.WithSubmission(ctx, bound, markerDigest(authorizationData), func(g *RuntimeRecoveryFinalizationGuard) error {
		p, e := g.PinProof(ctx)
		if e != nil {
			return e
		}
		if e = g.RecordReceipt(ctx, receiptForFinalizationProof(p)); e != nil {
			return e
		}
		_, e = g.PrepareRetirement(ctx)
		return e
	}); err != nil {
		t.Fatal("real final confirmation persistence", err)
	}
	if hash, err := final.RetirePrepared(ctx); err != nil || hash == "" {
		t.Fatal("real terminal archive", err)
	}
	if err := final.WithTerminalSubmission(ctx, bound, markerDigest(authorizationData), func(g *RuntimeRecoveryTerminalGuard) error {
		p, e := g.PinProof(ctx)
		if e != nil {
			return e
		}
		return g.RecordReceipt(ctx, receiptForTerminalProof(p))
	}); err != nil {
		t.Fatal("real terminal confirmation persistence", err)
	}
	beforeReleasePlan := terminalHistoryTree(t, e.receiptDir)
	if plan, err := final.PlanFenceRelease(ctx, bound, markerDigest(authorizationData)); err != nil || plan.OperationID != authorization.OperationID {
		t.Fatal("real runtime release readiness", err)
	}
	assertTerminalHistoryTree(t, e.receiptDir, beforeReleasePlan)
	assertTerminalHistoryTree(t, removalReceipts, beforeRemovalReceipts)
	if prepared {
		if hash, err := final.PrepareFenceRelease(ctx, bound, markerDigest(authorizationData)); err != nil || hash == "" {
			t.Fatal("real prepared release intent", err)
		}
		current := terminalHistoryTree(t, e.receiptDir)
		delete(current, runtimeRecoveryReleaseIntentFile)
		if len(current) != len(beforeReleasePlan) {
			t.Fatal("preparation changed root set")
		}
		for path, data := range beforeReleasePlan {
			if !bytes.Equal(current[path], data) {
				t.Fatal("preparation changed history", path)
			}
		}
	}
	if submitted {
		if err := final.WithPreparedReleaseSubmission(ctx, bound, markerDigest(authorizationData), func(g *RuntimeRecoveryPreparedReleaseGuard) error {
			p, e := g.PinProof(ctx)
			if e != nil {
				return e
			}
			return g.RecordReceipt(ctx, receiptForPreparedProof(p))
		}); err != nil {
			t.Fatal("real prepared evidence persistence", err)
		}
	}

	restartRuntime := func() error {
		if _, err := adapter.Restart(ctx); err != nil {
			return err
		}
		_, err := adapter.CheckHealth(ctx, e.activePath)
		return err
	}
	if submitted {
		before := terminalHistoryTree(t, e.receiptDir)
		var cleanupBackup map[string][]byte
		var cleanedIntent RuntimeRecoveryCleanedCommitIntent
		var cleanedProof RuntimeRecoveryCleanedCommitProof
		var cleanedReceipt RuntimeRecoveryCleanedCommitReceipt
		var cleanedArchiveHash string
		var released RuntimeRecoveryAdmissionReleaseResult
		var admissionProof RuntimeRecoveryAdmissionReleaseProof
		copyPath, _ := recoveryHistoryArchivePath(e.receiptDir, authorization.OperationID)
		if err := final.WithPreparedReleaseSubmission(ctx, bound, markerDigest(authorizationData), func(g *RuntimeRecoveryPreparedReleaseGuard) error {
			if _, err := g.AcknowledgedProof(ctx); err != nil {
				t.Fatal("real acknowledged readiness", err)
			}
			if hash, err := g.RetainHistory(ctx); err != nil || hash == "" {
				t.Fatal("real full history retention", err)
			}
			p, err := g.PinRetentionProof(ctx)
			if err != nil || g.RecordRetentionReceipt(ctx, receiptForRetentionProof(p)) != nil {
				t.Fatal("real retention evidence durability", err)
			}
			if hash, err := g.ArchiveRetainedHistory(ctx); err != nil || len(hash) != 64 {
				t.Fatal("real independent history durability", err)
			}
			archiveEntries, err := os.ReadDir(final.archive)
			if err != nil {
				t.Fatal(err)
			}
			if plan, err := g.PlanArchivedSourceCleanup(ctx); err != nil || len(plan.Candidates) != 9+len(archiveEntries) || plan.AdmissionLockName != "runtime.lock" {
				t.Fatal("real archived source plan", err)
			}
			if cleanup {
				cleanupBackup = terminalHistoryTree(t, copyPath)
				if s, err := g.CleanupArchivedSources(ctx); err != nil || !s.SourcesRemoved {
					t.Fatal("real duplicate cleanup", err)
				}
				if commitIntent {
					cleanedIntent, err = g.PrepareCleanedCommit(ctx)
					if err != nil || cleanedIntent.Observation != authorization.Expected {
						t.Fatal("real cleaned commit intent", err)
					}
					cleanedProof, err = g.PinCleanedCommitProof(ctx)
					if err != nil {
						t.Fatal("real cleaned proof", err)
					}
					wire, _ := json.Marshal(cleanedProof)
					h := markerDigest(wire)
					cleanedReceipt = RuntimeRecoveryCleanedCommitReceipt{1, true, cleanedProof.OperationID, cleanedProof.CleanedIntentHash, h, h}
					if err = g.RecordCleanedCommitReceipt(ctx, cleanedReceipt); err != nil {
						t.Fatal("real cleaned storage acknowledgement", err)
					}
					beforePlan := terminalHistoryTree(t, e.receiptDir)
					plan, err := g.PlanCleanedCommit(ctx)
					if err != nil || plan.ProofHash != cleanedReceipt.ProofHash || plan.ManifestHash != cleanedProof.ManifestHash || plan.AdmissionLockName != "runtime.lock" || plan.ObservedAt.Before(cleanedProof.ObservedAt) {
						t.Fatal("real cleaned commit plan", err)
					}
					for _, file := range plan.Files {
						if file.Hash != markerDigest(beforePlan[file.Name]) || file.Name == "runtime.lock" {
							t.Fatal("real plan inventory")
						}
					}
					assertTerminalHistoryTree(t, e.receiptDir, beforePlan)
					cleanedArchiveHash, err = g.ArchiveCleanedCommitHistory(ctx)
					if err != nil || len(cleanedArchiveHash) != 64 {
						t.Fatal("real cleaned history archive", err)
					}
					assertTerminalHistoryTree(t, e.receiptDir, beforePlan)
				}
				if release {
					var err error
					released, err = g.ReleaseAdmission(ctx)
					if err == nil {
						admissionProof, err = g.PinAdmissionReleaseProof(ctx)
					}
					return err
				}
				if err := restartRuntime(); err != nil {
					t.Fatal(err)
				}
				if commitIntent {
					if plan, err := g.PlanCleanedCommit(ctx); err != ErrRuntimeMutationBlocked || plan != (RuntimeRecoveryCleanedCommitPlan{}) {
						t.Fatal("cleaned plan accepted restarted runtime", err)
					}
				}
				if p, err := g.CleanupPreparedProof(ctx); err != ErrRuntimeMutationBlocked || p != (RuntimeRecoveryPreparedReleaseProof{}) {
					t.Fatal("cleaned owner accepted changed runtime", err)
				}
				if commitIntent {
					if p, err := g.CleanedCommitProof(ctx); err != ErrRuntimeMutationBlocked || p != (RuntimeRecoveryCleanedCommitProof{}) {
						t.Fatal("cleaned proof accepted restarted runtime", err)
					}
					if p, err := g.PrepareCleanedCommit(ctx); err != ErrRuntimeMutationBlocked || p != (RuntimeRecoveryCleanedCommitIntent{}) {
						t.Fatal("intent accepted restarted runtime", err)
					}
				}
				return ErrRuntimeMutationBlocked
			}
			if err := restartRuntime(); err != nil {
				t.Fatal(err)
			}
			if plan, err := g.PlanArchivedSourceCleanup(ctx); err != ErrRuntimeMutationBlocked || plan.ManifestHash != "" || len(plan.Candidates) != 0 {
				t.Fatal("source plan accepted restarted process", err)
			}
			if p, err := g.RetentionProof(ctx); err != ErrRuntimeMutationBlocked || p != (RuntimeRecoveryRetentionProof{}) {
				t.Fatal("retention evidence accepted restarted process", err)
			}
			if p, err := g.AcknowledgedProof(ctx); err != ErrRuntimeMutationBlocked || p != (RuntimeRecoveryPreparedReleaseProof{}) {
				t.Fatal("acknowledged readiness accepted restarted process", err)
			}
			return ErrRuntimeMutationBlocked
		}); release && err != nil || !release && err != ErrRuntimeMutationBlocked {
			t.Fatal("runtime finalization result", err)
		}
		if release {
			if released.Outcome != RuntimeRecoveryAdmissionDurable || released.RemovedFiles != 5 || released.ArchiveHash != cleanedArchiveHash {
				t.Fatal("real admission release", released)
			}
			if l, err := BeginRuntimeMutation(e.receiptDir, resultTaskID, TaskKindVPNCoreService); err != ErrRuntimeMutationBlocked {
				if l != nil {
					l.Close()
				}
				t.Fatal("release dropped original flock")
			}
			final.Close()
			if err := WithArchivedRecoveryCleanedCommit(ctx, e.receiptDir, authorization.OperationID, bound, func(h RuntimeRecoveryCleanedCommitArchivedHistory) error {
				if h.ManifestHash != cleanedArchiveHash || h.Proof != cleanedProof || h.Receipt != cleanedReceipt {
					t.Error("released real archive chain")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if err := WithRecoveryAdmissionReleaseHistory(ctx, e.receiptDir, authorization.OperationID, bound, func(p RuntimeRecoveryAdmissionReleaseProof) error {
				if p != admissionProof || p.CleanedArchiveHash != cleanedArchiveHash || p.Observation != authorization.Expected {
					t.Error("real post-release proof")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			l, err := BeginRuntimeMutation(e.receiptDir, resultTaskID, TaskKindVPNCoreService)
			if err != nil {
				t.Fatal("real admission remained fenced", err)
			}
			if err = l.Complete(); err != nil {
				t.Fatal(err)
			}
			l.Close()
			if removalTraffic(ctx, proxies[0], origin.URL) || removalTraffic(ctx, proxies[1], origin.URL) {
				t.Fatal("release regranted removed credentials")
			}
			assertTerminalHistoryTree(t, copyPath, cleanupBackup)
			assertTerminalHistoryTree(t, removalReceipts, beforeRemovalReceipts)
			return
		}
		if cleanup {
			final.Close()
			if err := WithRecoverySourceCleanupHistory(ctx, e.receiptDir, authorization.OperationID, bound, func(s RuntimeRecoverySourceCleanupStatus, h RuntimeRecoveryRetentionHistory) error {
				if !s.SourcesRemoved || !h.LocalReceipt.Recorded {
					t.Error("real cleanup history")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			remaining := terminalHistoryTree(t, e.receiptDir)
			wantFiles := 4
			if commitIntent {
				wantFiles += 3
				if err := WithArchivedRecoveryCleanedCommit(ctx, e.receiptDir, authorization.OperationID, bound, func(h RuntimeRecoveryCleanedCommitArchivedHistory) error {
					if h.ManifestHash != cleanedArchiveHash || h.Proof != cleanedProof || h.Receipt != cleanedReceipt {
						t.Error("real archived cleaned chain")
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				if err := WithRecoveryCleanedCommitEvidenceHistory(ctx, e.receiptDir, authorization.OperationID, bound, func(p RuntimeRecoveryCleanedCommitProof, r RuntimeRecoveryCleanedCommitReceipt) error {
					if p != cleanedProof || r != cleanedReceipt {
						t.Error("real cleaned evidence history")
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				if err := WithRecoveryCleanedCommitHistory(ctx, e.receiptDir, authorization.OperationID, bound, func(p RuntimeRecoveryCleanedCommitIntent, h RuntimeRecoveryRetentionHistory) error {
					if p != cleanedIntent || !h.LocalReceipt.Recorded {
						t.Error("real cleaned intent history")
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			if len(remaining) != wantFiles {
				t.Fatal("real cleanup remaining tree")
			}
			for _, name := range []string{"runtime.lock", "mutation-inflight.json", runtimeRecoveryReleaseIntentFile} {
				if !bytes.Equal(before[name], remaining[name]) {
					t.Fatal("real cleanup changed barrier")
				}
			}
			if l, err := BeginRuntimeMutation(e.receiptDir, resultTaskID, TaskKindVPNCoreService); err != ErrRuntimeMutationBlocked {
				if l != nil {
					l.Close()
				}
				t.Fatal("real cleanup released fence")
			}
			assertTerminalHistoryTree(t, copyPath, cleanupBackup)
			assertTerminalHistoryTree(t, removalReceipts, beforeRemovalReceipts)
			return
		}
		retained, err := os.ReadFile(filepath.Join(e.receiptDir, runtimeRecoveryReleaseRetentionFile))
		if err != nil {
			t.Fatal(err)
		}
		before[runtimeRecoveryReleaseRetentionFile] = retained
		for _, name := range []string{runtimeRecoveryRetentionSubmissionFile, runtimeRecoveryRetentionAcknowledgementFile} {
			data, err := os.ReadFile(filepath.Join(e.receiptDir, name))
			if err != nil {
				t.Fatal(err)
			}
			before[name] = data
		}
		assertTerminalHistoryTree(t, e.receiptDir, before)
	} else if err := restartRuntime(); err != nil {
		t.Fatal(err)
	}
	if submitted {
		if err := final.WithPreparedReleaseSubmission(ctx, bound, markerDigest(authorizationData), func(*RuntimeRecoveryPreparedReleaseGuard) error {
			t.Error("restarted runtime reached submission")
			return nil
		}); err != ErrRuntimeMutationBlocked {
			t.Fatal("new process accepted prepared proof", err)
		}
	}

	if prepared {
		if hash, err := final.PrepareFenceRelease(ctx, bound, markerDigest(authorizationData)); err != ErrRuntimeMutationBlocked || hash != "" {
			t.Fatal("new process accepted prepared intent", err)
		}
	}
	if _, err := final.PlanFenceRelease(ctx, bound, markerDigest(authorizationData)); err != ErrRuntimeMutationBlocked {
		t.Fatal("new process accepted old release readiness", err)
	}
	if _, err := final.RetirePrepared(ctx); err == nil {
		t.Fatal("new process accepted old archived terminal witness")
	}
	if err := final.WithTerminalSubmission(ctx, bound, markerDigest(authorizationData), func(*RuntimeRecoveryTerminalGuard) error { return nil }); err == nil {
		t.Fatal("new process accepted old terminal submission owner")
	}
	if _, err := observeRuntimeRecoveryLocked(ctx, final.archive, binding, task.ID, e.activePath, adapter, final.lock); err == nil {
		t.Fatal("new process accepted archived witness")
	}
	final.Close()
	if submitted {
		before := terminalHistoryTree(t, e.receiptDir)
		pin, e2 := readTerminalConfirmationFile(filepath.Join(e.receiptDir, runtimeRecoveryPreparedSubmissionFile), 8192)
		var p RuntimeRecoveryPreparedReleaseProof
		ack, e3 := readTerminalConfirmationFile(filepath.Join(e.receiptDir, runtimeRecoveryPreparedAcknowledgementFile), 4096)
		var receipt RuntimeRecoveryPreparedReleaseReceipt
		if e2 != nil || e3 != nil || !exactHistoricalJSON(pin, &p) || !exactHistoricalJSON(ack, &receipt) || !preparedReleaseReceiptMatches(receipt, p, pin) {
			t.Fatal("real restart lost confirmation")
		}
		if err := WithHistoricalRecoveryRetention(ctx, e.receiptDir, bound, func(h RuntimeRecoveryRetentionHistory) error {
			if h.Proof.Observation != observation || !h.LocalReceipt.Recorded {
				t.Error("real retention history drift")
			}
			return nil
		}); err != nil {
			t.Fatal("real retention history unavailable after restart", err)
		}
		if err := WithArchivedRecoveryRetention(ctx, e.receiptDir, p.OperationID, bound, func(h RuntimeRecoveryArchivedHistory) error {
			if h.History.Proof.Observation != observation || !h.History.LocalReceipt.Recorded || len(h.ManifestHash) != 64 {
				t.Error("real independent history drift")
			}
			return nil
		}); err != nil {
			t.Fatal("real independent history unavailable after restart", err)
		}
		if l, err := BeginRuntimeMutation(e.receiptDir, resultTaskID, TaskKindVPNCoreService); err != ErrRuntimeMutationBlocked {
			if l != nil {
				l.Close()
			}
			t.Fatal("restart released prepared fence")
		}
		if err := WithHistoricalRecoveryPreparedRelease(ctx, e.receiptDir, bound, func(RuntimeRecoveryPreparedReleaseHistory) error {
			t.Error("historical owner adopted submission")
			return nil
		}); err != ErrRuntimeMutationBlocked {
			t.Fatal(err)
		}
		assertTerminalHistoryTree(t, e.receiptDir, before)
		assertTerminalHistoryTree(t, removalReceipts, beforeRemovalReceipts)
		return
	}

	if prepared {
		assertReleaseIntentFenced(t, e.receiptDir, bound)
		assertTerminalHistoryTree(t, removalReceipts, beforeRemovalReceipts)
		return
	}
	beforeTerminalRead := terminalHistoryTree(t, e.receiptDir)
	if err := WithHistoricalRecoveryTerminal(ctx, e.receiptDir, bound, func(p RuntimeRecoveryTerminalProof) error {
		data, e := os.ReadFile(filepath.Join(e.receiptDir, runtimeRecoveryTerminalSubmissionFile))
		wire, _ := json.Marshal(p)
		if e != nil || !bytes.Equal(wire, data) {
			t.Error("real historical terminal proof drift", e)
		}
		return nil
	}); err != nil {
		t.Fatal("real terminal read after process drift", err)
	}
	assertTerminalHistoryTree(t, e.receiptDir, beforeTerminalRead)
	// Historical storage read remains valid after injected runtime drift, without
	// claiming a fresh runtime witness or reacquiring a mutation owner.
	assertHistoricalFinalizationAfterExit(t, e.receiptDir, bound)
	if runtimeMarkerAbsent(filepath.Join(final.archive, runtimeRecoveryFinalConsumptionFile)) {
		t.Fatal("real process drift discarded final consumption")
	}
	for _, name := range []string{runtimeRecoveryFinalSubmissionFile, runtimeRecoveryFinalAcknowledgementFile, runtimeRecoveryRetirementFile} {
		if runtimeMarkerAbsent(filepath.Join(final.archive, name)) {
			t.Fatal("real drift discarded final confirmation", name)
		}
	}
	if _, err := ObserveRuntimeRecovery(ctx, e.receiptDir, binding, task.ID, e.activePath, adapter); err == nil {
		t.Fatal("new process accepted old recovery witness")
	}
	if runtimeMarkerAbsent(filepath.Join(final.archive, runtimeRecoveryConsumptionFile)) {
		t.Fatal("real process drift discarded consumption")
	}
	if runtimeMarkerAbsent(filepath.Join(final.archive, runtimeRecoverySubmissionFile)) {
		t.Fatal("real process drift discarded submission")
	}
	if runtimeMarkerAbsent(filepath.Join(final.archive, runtimeRecoveryAcknowledgementFile)) {
		t.Fatal("real process drift discarded acknowledgement")
	}
	if runtimeMarkerAbsent(filepath.Join(final.archive, "mutation-inflight.json")) {
		t.Fatal("observation cleared fence")
	}
	t.Log("recovery observation matches real process, rejects subsequent restart and retains fence")
	t.Log("durable grant consumption holds admission lock, rejects external restart and retains consumption/fence")
}
func bindRemovalCandidate(t *testing.T, task *ConfigTask, object map[string]any) {
	runtime := removalMarshal(t, object)
	_, _, task.CredentialRemoval.CandidateRuntimeHash, _ = canonicalRemovalJSON(runtime)
	task.RenderedConfig = removalMarshal(t, map[string]any{"schemaVersion": "routegate.config.v1", "server": map[string]string{"id": task.ServerID}, "agent": map[string]string{"id": task.AgentID}, "singBox": object})
}
func removalMarshal(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func startRemovalTestUnit(t *testing.T, ctx context.Context, binary, activePath string) string {
	t.Helper()
	unit := "routegate-removal-test-" + strconv.Itoa(os.Getpid()) + ".service"
	unitPath := filepath.Join("/run/systemd/system", unit)
	if _, err := os.Lstat(unitPath); !os.IsNotExist(err) {
		t.Fatal("test unit already exists")
	}
	content := "[Unit]\nDescription=Isolated RouteGate credential removal test\n[Service]\nType=simple\nExecStart=" + strconv.Quote(binary) + " run -c " + strconv.Quote(activePath) + "\nKillMode=control-group\nStandardOutput=null\nStandardError=null\n[Install]\nWantedBy=multi-user.target\n"
	if err := os.WriteFile(unitPath, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = exec.CommandContext(cleanup, "systemctl", "disable", "--now", unit).Run()
		_ = os.Remove(unitPath)
		_ = exec.CommandContext(cleanup, "systemctl", "daemon-reload").Run()
	})
	for _, args := range [][]string{{"daemon-reload"}, {"enable", "--now", unit}} {
		if err := exec.CommandContext(ctx, "systemctl", args...).Run(); err != nil {
			t.Fatal("isolated systemd setup", err)
		}
	}
	return unit
}

func removalFreePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err = listener.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}

func removalStartSingBox(t *testing.T, ctx context.Context, binary string, config any, port int) func() {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, removalMarshal(t, config), 0600); err != nil {
		t.Fatal(err)
	}
	// Suppress runtime output, which may contain test credentials/configuration.
	if err := exec.CommandContext(ctx, binary, "check", "-c", path).Run(); err != nil {
		t.Fatalf("sing-box candidate validation failed: %v", err)
	}
	process := exec.CommandContext(ctx, binary, "run", "-c", path)
	if err := process.Start(); err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	stop := func() { once.Do(func() { _ = process.Process.Kill(); _ = process.Wait() }) }
	t.Cleanup(stop)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && ctx.Err() == nil {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 100*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return stop
		}
		time.Sleep(20 * time.Millisecond)
	}
	stop()
	t.Fatal("isolated sing-box listener did not start")
	return stop
}

func removalWaitTraffic(t *testing.T, ctx context.Context, port int, origin string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && ctx.Err() == nil {
		if removalTraffic(ctx, port, origin) {
			return
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatal("isolated account failed real Reality traffic")
}

func removalTraffic(ctx context.Context, port int, origin string) bool {
	proxy := &url.URL{Scheme: "http", Host: fmt.Sprintf("127.0.0.1:%d", port)}
	transport := &http.Transport{Proxy: http.ProxyURL(proxy), DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 2 * time.Second}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, origin, nil)
	if err != nil {
		return false
	}
	response, err := client.Do(request)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 100))
	return err == nil && response.StatusCode == 200 && string(body) == "isolated-removal-origin"
}
