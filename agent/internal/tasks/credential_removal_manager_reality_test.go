package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/url"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

type ManagerRemovalRealityFixture struct {
	ManagerURL, Token, AccountID, PublicKey, JobID string
	Baseline                                       json.RawMessage
}

// RunCredentialRemovalManagerRealityFixture consumes the real Manager's
// baseline and task. All endpoints and client probes are disposable.
func RunCredentialRemovalManagerRealityFixture(t *testing.T, input ManagerRemovalRealityFixture, binary string, execute func(context.Context, CredentialRemovalExecutor) (*CredentialRemovalReport, error)) {
	t.Helper()
	var envelope struct {
		Server  struct{ ID string }
		Agent   struct{ ID string }
		SingBox json.RawMessage
	}
	if err := json.Unmarshal(input.Baseline, &envelope); err != nil || envelope.Server.ID == "" || envelope.Agent.ID == "" {
		t.Fatal("invalid Manager baseline")
	}
	var runtime struct {
		Inbounds []struct {
			Type       string
			ListenPort int `json:"listen_port"`
			Users      []struct{ Name, UUID, Flow string }
			TLS        struct {
				ServerName string `json:"server_name"`
				Reality    struct {
					ShortID []string `json:"short_id"`
				}
			}
		}
	}
	if err := json.Unmarshal(envelope.SingBox, &runtime); err != nil || len(runtime.Inbounds) != 1 {
		t.Fatal("unexpected disposable sing-box topology")
	}
	inbound := runtime.Inbounds[0]
	if inbound.Type != "vless" || inbound.TLS.ServerName != "localhost" || len(inbound.TLS.Reality.ShortID) != 1 || len(inbound.Users) != 2 {
		t.Fatal("expected two clients on a loopback Reality fixture")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	var revokedProbeHits atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/stream":
			_, _ = io.WriteString(w, "open")
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		case "/revoked-probe":
			revokedProbeHits.Add(1)
		}
		_, _ = io.WriteString(w, "isolated-removal-origin")
	}))
	defer origin.Close()

	dir := t.TempDir()
	active := filepath.Join(dir, "config.json")
	if err := os.WriteFile(active, envelope.SingBox, 0600); err != nil {
		t.Fatal(err)
	}
	unit := startRemovalTestUnit(t, ctx, binary, active)
	adapter := NewSingBoxVLESSAdapter(dir, binary, unit)
	executor := NewCredentialRemovalExecutor(active, filepath.Join(dir, "receipts"), envelope.Server.ID, envelope.Agent.ID, adapter, true)
	var revoked, control int
	for _, user := range inbound.Users {
		port := removalFreePort(t)
		client := map[string]any{
			"log":      map[string]any{"level": "error"},
			"inbounds": []any{map[string]any{"type": "mixed", "listen": "127.0.0.1", "listen_port": port}},
			"outbounds": []any{map[string]any{
				"type": "vless", "tag": "vpn", "server": "127.0.0.1", "server_port": inbound.ListenPort,
				"uuid": user.UUID, "flow": user.Flow,
				"tls": map[string]any{"enabled": true, "server_name": inbound.TLS.ServerName,
					"utls":    map[string]any{"enabled": true, "fingerprint": "chrome"},
					"reality": map[string]any{"enabled": true, "public_key": input.PublicKey, "short_id": inbound.TLS.Reality.ShortID[0]}},
			}},
			"route": map[string]any{"final": "vpn"},
		}
		removalStartSingBox(t, ctx, binary, client, port)
		removalWaitTraffic(t, ctx, port, origin.URL)
		if user.Name == input.AccountID {
			revoked = port
		} else {
			control = port
		}
	}
	if revoked == 0 || control == 0 {
		t.Fatal("target account did not match the baseline")
	}
	t.Log("both baseline credentials carried real Reality traffic")
	proxy := &url.URL{Scheme: "http", Host: fmt.Sprintf("127.0.0.1:%d", revoked)}
	transport := &http.Transport{Proxy: http.ProxyURL(proxy), DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: removalStreamClientTimeout}
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, origin.URL+"/stream", nil)
	started := time.Now()
	response, err := client.Do(request)
	if err != nil {
		t.Fatal("old stream did not open", err)
	}
	defer response.Body.Close()
	first := make([]byte, 4)
	if _, err := io.ReadFull(response.Body, first); err != nil || string(first) != "open" {
		t.Fatal("old stream did not carry data")
	}
	done := make(chan removalStreamTermination, 1)
	go func() {
		_, readErr := io.Copy(io.Discard, response.Body)
		done <- removalStreamTermination{err: readErr, contextErr: ctx.Err(), finished: time.Now()}
	}()
	mutationStarted := time.Now()
	report, err := execute(ctx, executor)
	if err != nil || report == nil || report.JobID != input.JobID || report.AccountID != input.AccountID {
		t.Fatal("Agent did not acknowledge the issued job", err)
	}
	select {
	case result := <-done:
		if err := result.validate(started, mutationStarted); err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("existing revoked stream remained open")
	}
	removalWaitTraffic(t, ctx, control, origin.URL)
	var connected atomic.Bool
	probeCtx := httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{GotConn: func(httptrace.GotConnInfo) { connected.Store(true) }})
	probe, _ := http.NewRequestWithContext(probeCtx, http.MethodGet, origin.URL+"/revoked-probe", nil)
	probeResponse, err := (&http.Client{Transport: transport, Timeout: 3 * time.Second}).Do(probe)
	if probeResponse != nil {
		_ = probeResponse.Body.Close()
	}
	var networkErr net.Error
	if !connected.Load() || ctx.Err() != nil || (errors.As(err, &networkErr) && networkErr.Timeout()) ||
		(err == nil && (probeResponse == nil || probeResponse.StatusCode < 500)) || revokedProbeHits.Load() != 0 {
		t.Fatal("revoked credential probe was inconclusive or reached the origin")
	}
	removalWaitTraffic(t, ctx, control, origin.URL)
	t.Log("old stream terminated, old credential could not reach the origin, and the other account reconnected")
}
