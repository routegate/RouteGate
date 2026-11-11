package configs

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
	"sync"
	"testing"
	"time"
)

func TestVLESSRemovalSingBoxCheck(t *testing.T) {
	binary := os.Getenv("ROUTEGATE_TEST_SING_BOX")
	if binary == "" {
		t.Skip("set ROUTEGATE_TEST_SING_BOX for real configuration checks")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	baseline, target := removalFixture(t)
	var config RenderedConfig
	if err := json.Unmarshal(baseline.RenderedConfig, &config); err != nil {
		t.Fatal(err)
	}
	candidate, err := PrepareVLESSRemoval(baseline, target)
	if err != nil {
		t.Fatal(err)
	}
	remaining := pinRemovalConfig(t, baseline, candidate.RenderedConfig)
	last := VLESSRemovalTarget{ServerID: target.ServerID, AccountID: "keep", VersionID: remaining.ID, ConfigHash: remaining.ConfigHash, VLESSUUID: config.VPNAccounts[1].VLESSUUID}
	empty, err := PrepareVLESSRemoval(remaining, last)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		config SingBoxConfig
	}{{"baseline", config.SingBox}, {"one_removed", candidate.RenderedConfig.SingBox}, {"none_allowed", empty.RenderedConfig.SingBox}} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(path, mustMarshalRaw(t, tc.config), 0600); err != nil {
				t.Fatal(err)
			}
			if err := exec.CommandContext(ctx, binary, "check", "-c", path).Run(); err != nil {
				t.Fatalf("sing-box rejected candidate: %v", err)
			}
		})
	}
}

// This is a disposable runtime test of the prepared JSON, not an Agent apply
// or a durable revocation operation. No systemd, Manager, or real node is used.
func TestVLESSRemovalSingBoxRealityTraffic(t *testing.T) {
	binary := os.Getenv("ROUTEGATE_TEST_SING_BOX")
	if binary == "" {
		t.Skip("set ROUTEGATE_TEST_SING_BOX for isolated real Reality traffic")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "isolated-removal-origin") }))
	defer origin.Close()
	tlsTarget := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "handshake-only") }))
	tlsTarget.TLS = &tls.Config{MinVersion: tls.VersionTLS13, MaxVersion: tls.VersionTLS13, CurvePreferences: []tls.CurveID{tls.X25519}}
	tlsTarget.StartTLS()
	defer tlsTarget.Close()
	_, handshakePort, err := net.SplitHostPort(tlsTarget.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	handshakeAddr, err := net.ResolveTCPAddr("tcp", net.JoinHostPort("127.0.0.1", handshakePort))
	if err != nil {
		t.Fatal(err)
	}
	private, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	publicKey := base64.RawURLEncoding.EncodeToString(private.PublicKey().Bytes())
	baseline, target := removalFixture(t)
	var rendered RenderedConfig
	if err = json.Unmarshal(baseline.RenderedConfig, &rendered); err != nil {
		t.Fatal(err)
	}
	serverPort := removalFreePort(t)
	inbound := rendered.SingBox.Inbounds[0]
	inbound["listen"] = "127.0.0.1"
	inbound["listen_port"] = serverPort
	tlsConfig := inbound["tls"].(map[string]any)
	reality := tlsConfig["reality"].(map[string]any)
	reality["private_key"] = base64.RawURLEncoding.EncodeToString(private.Bytes())
	reality["handshake"] = map[string]any{"server": "127.0.0.1", "server_port": handshakeAddr.Port}
	baseline = pinRemovalConfig(t, baseline, rendered)
	target.ConfigHash = baseline.ConfigHash
	candidate, err := PrepareVLESSRemoval(baseline, target)
	if err != nil {
		t.Fatal(err)
	}
	if err = ValidateVLESSRemovalDelta(baseline, target, mustMarshalRaw(t, candidate.RenderedConfig)); err != nil {
		t.Fatal(err)
	}
	stopBaseline := removalStartSingBox(t, ctx, binary, rendered.SingBox, serverPort)
	proxyPorts := []int{removalFreePort(t), removalFreePort(t)}
	for index, account := range rendered.VPNAccounts {
		clientConfig := map[string]any{
			"log":      map[string]any{"level": "error"},
			"inbounds": []any{map[string]any{"type": "mixed", "listen": "127.0.0.1", "listen_port": proxyPorts[index]}},
			"outbounds": []any{map[string]any{"type": "vless", "tag": "vpn", "server": "127.0.0.1", "server_port": serverPort, "uuid": account.VLESSUUID, "flow": "xtls-rprx-vision",
				"tls": map[string]any{"enabled": true, "server_name": "example.com", "utls": map[string]any{"enabled": true, "fingerprint": "chrome"}, "reality": map[string]any{"enabled": true, "public_key": publicKey, "short_id": "0123456789abcdef"}}}},
			"route": map[string]any{"final": "vpn"},
		}
		removalStartSingBox(t, ctx, binary, clientConfig, proxyPorts[index])
		removalWaitTraffic(t, ctx, proxyPorts[index], origin.URL)
	}
	// Model the current adapter's service restart explicitly. It may interrupt
	// every session on this service; this test does not claim selective teardown.
	stopBaseline()
	stopCandidate := removalStartSingBox(t, ctx, binary, candidate.RenderedConfig.SingBox, serverPort)
	removalWaitTraffic(t, ctx, proxyPorts[1], origin.URL)
	if removalTraffic(ctx, proxyPorts[0], origin.URL) {
		t.Fatal("removed cached UUID still authenticated")
	}
	// Last-user JSON retains the listener and permits nobody. It still requires
	// separate operation authority before generic apply can support this state.
	stopCandidate()
	remaining := pinRemovalConfig(t, baseline, candidate.RenderedConfig)
	lastTarget := VLESSRemovalTarget{ServerID: "node", AccountID: "keep", VersionID: remaining.ID, ConfigHash: remaining.ConfigHash, VLESSUUID: rendered.VPNAccounts[1].VLESSUUID}
	empty, err := PrepareVLESSRemoval(remaining, lastTarget)
	if err != nil {
		t.Fatal(err)
	}
	removalStartSingBox(t, ctx, binary, empty.RenderedConfig.SingBox, serverPort)
	if removalTraffic(ctx, proxyPorts[0], origin.URL) || removalTraffic(ctx, proxyPorts[1], origin.URL) {
		t.Fatal("zero-user listener accepted a cached UUID")
	}
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
	if err := os.WriteFile(path, mustMarshalRaw(t, config), 0600); err != nil {
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
