package tasks

import (
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
	"sync"
	"testing"
	"time"
)

// Opt-in only: uses a disposable CI VM, one temporary systemd unit and loopback.
// Never point this test at an installed RouteGate service or a production node.
func TestCredentialRemovalSystemdRealityTraffic(t *testing.T) {
	binary := os.Getenv("ROUTEGATE_TEST_SING_BOX")
	if os.Getenv("ROUTEGATE_TEST_REMOVAL_SYSTEMD") != "1" || binary == "" {
		t.Skip("requires disposable systemd runner and pinned sing-box")
	}
	if os.Geteuid() != 0 {
		t.Fatal("isolated systemd test requires root")
	}
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
	unit := "routegate-removal-test-" + strconv.Itoa(os.Getpid()) + ".service"
	unitPath := filepath.Join("/run/systemd/system", unit)
	if _, err = os.Lstat(unitPath); !os.IsNotExist(err) {
		t.Fatal("test unit already exists")
	}
	content := "[Unit]\nDescription=Isolated RouteGate credential removal test\n[Service]\nType=simple\nExecStart=" + strconv.Quote(binary) + " run -c " + strconv.Quote(e.activePath) + "\nKillMode=control-group\nStandardOutput=null\nStandardError=null\n[Install]\nWantedBy=multi-user.target\n"
	if err = os.WriteFile(unitPath, []byte(content), 0600); err != nil {
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
		if err = exec.CommandContext(ctx, "systemctl", args...).Run(); err != nil {
			t.Fatal("isolated systemd setup", err)
		}
	}
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
	streams := []io.ReadCloser{}
	for _, port := range proxies {
		proxy := &url.URL{Scheme: "http", Host: fmt.Sprintf("127.0.0.1:%d", port)}
		transport := &http.Transport{Proxy: http.ProxyURL(proxy)}
		defer transport.CloseIdleConnections()
		request, _ := http.NewRequestWithContext(ctx, http.MethodGet, origin.URL+"/stream", nil)
		response, err := (&http.Client{Transport: transport, Timeout: 12 * time.Second}).Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		first := make([]byte, 4)
		if _, err = io.ReadFull(response.Body, first); err != nil || string(first) != "open" {
			t.Fatal("stream did not open", err)
		}
		streams = append(streams, response.Body)
	}
	inbound["users"] = users[1:]
	bindRemovalCandidate(t, &task, object)
	report, err := e.Execute(ctx, task)
	if err != nil {
		t.Fatal(report, err)
	}
	t.Log("executor measured new systemd invocation, exact active digest and listener")
	replay, err := e.Execute(ctx, task)
	if err != nil || replay != report {
		t.Fatal("lost-ack replay", replay, err)
	}
	for _, stream := range streams {
		done := make(chan error, 1)
		go func(body io.Reader) { _, err := io.ReadAll(body); done <- err }(stream)
		select {
		case <-done:
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
	if _, err := adapter.Restart(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.CheckHealth(ctx, e.activePath); err != nil {
		t.Fatal(err)
	}
	if _, err := ObserveRuntimeRecovery(ctx, e.receiptDir, binding, task.ID, e.activePath, adapter); err == nil {
		t.Fatal("new process accepted old recovery witness")
	}
	if runtimeMarkerAbsent(filepath.Join(e.receiptDir, "mutation-inflight.json")) {
		t.Fatal("observation cleared fence")
	}
	t.Log("recovery observation matches real process, rejects subsequent restart and retains fence")
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
