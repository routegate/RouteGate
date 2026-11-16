package tasks

import (
	"context"
	"crypto/tls"
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
	"testing"
	"time"
)

// This is the *current Manager's actual applied PostgreSQL candidate*, not a
// handwritten replacement for its JSON. No HTTP task dispatch is enabled.
func TestCredentialRemovalManagerPreparedRealityTraffic(t *testing.T) {
	path := os.Getenv("ROUTEGATE_TEST_REMOVAL_REALITY_CONTRACT_FIXTURE")
	binary := os.Getenv("ROUTEGATE_TEST_SING_BOX")
	if path == "" || binary == "" || os.Getenv("ROUTEGATE_TEST_REMOVAL_SYSTEMD") != "1" {
		t.Skip("requires disposable Manager PostgreSQL + root systemd + pinned sing-box")
	}
	if os.Geteuid() != 0 {
		t.Fatal("disposable Reality E2E must run as root in CI")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("Manager Reality fixture not accessible")
	}
	var fixture struct {
		BaselineRuntime json.RawMessage `json:"baselineRuntime"`
		PublicKey string `json:"publicKey"`
		Task ConfigTask `json:"task"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal("invalid Manager Reality contract")
	}
	task := fixture.Task
	if task.Kind != TaskKindCredentialRemoval || task.CredentialRemoval == nil {
		t.Fatal("Manager candidate has no scoped removal intent")
	}
	var envelope struct {
		SingBox json.RawMessage `json:"singBox"`
	}
	if err := json.Unmarshal(task.RenderedConfig, &envelope); err != nil {
		t.Fatal("invalid applied candidate envelope")
	}
	port, err := verifyRemovalRuntimeDelta(fixture.BaselineRuntime, envelope.SingBox, *task.CredentialRemoval)
	if err != nil {
		t.Fatal("real Manager candidate failed Agent runtime delta gate")
	}
	var runtime struct {
		Inbounds []struct {
			Type string `json:"type"`
			ListenPort int `json:"listen_port"`
			Users []struct{ Name, UUID, Flow string }
			TLS struct {
				ServerName string `json:"server_name"`
				Reality struct { ShortID []string `json:"short_id"` } `json:"reality"`
			} `json:"tls"`
		} `json:"inbounds"`
	}
	if err := json.Unmarshal(fixture.BaselineRuntime, &runtime); err != nil ||
		len(runtime.Inbounds) != 1 || runtime.Inbounds[0].Type != "vless" ||
		runtime.Inbounds[0].ListenPort != port ||
		runtime.Inbounds[0].TLS.ServerName != "localhost" ||
		len(runtime.Inbounds[0].TLS.Reality.ShortID) != 1 ||
		len(runtime.Inbounds[0].Users) != 2 || fixture.PublicKey == "" {
		t.Fatal("expected two real applied VLESS/Reality credentials on disposable localhost")
	}
	inbound := runtime.Inbounds[0]
	ctx, cancel := context.WithTimeout(context.Background(), 55*time.Second)
	defer cancel()

	// Actual Reality handshake target for the Manager-rendered localhost SNI.
	listener, err := net.Listen("tcp4", "127.0.0.1:443")
	if err != nil {
		t.Fatal("isolated Reality handshake port unavailable", err)
	}
	target := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "handshake-only")
	}))
	_ = target.Listener.Close()
	target.Listener = listener
	target.TLS = &tls.Config{MinVersion: tls.VersionTLS13, MaxVersion: tls.VersionTLS13, CurvePreferences: []tls.CurveID{tls.X25519}}
	target.StartTLS()
	defer target.Close()

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/stream" {
			_, _ = io.WriteString(w, "open")
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		}
		_, _ = io.WriteString(w, "origin-manager-candidate")
	}))
	defer origin.Close()
	dir := t.TempDir()
	active := filepath.Join(dir, "config.json")
	if err := os.WriteFile(active, fixture.BaselineRuntime, 0600); err != nil {
		t.Fatal(err)
	}
	unit := startManagerCandidateRealityUnit(t, ctx, binary, active)
	adapter := NewSingBoxVLESSAdapter(dir, binary, unit)
	if _, err := adapter.CheckHealth(ctx, active); err != nil {
		t.Fatal("real Manager baseline not healthy under systemd", err)
	}
	executor := NewCredentialRemovalExecutor(active, filepath.Join(dir, "receipts"),
		task.ServerID, task.AgentID, adapter, true)

	proxies := map[string]int{}
	for _, user := range inbound.Users {
		port := removalFreePort(t)
		clientConfig := map[string]any{
			"log": map[string]any{"level":"error"},
			"inbounds": []any{map[string]any{"type":"mixed","listen":"127.0.0.1","listen_port":port}},
			"outbounds": []any{map[string]any{
				"type":"vless","tag":"vpn","server":"127.0.0.1","server_port":inbound.ListenPort,
				"uuid":user.UUID,"flow":user.Flow,
				"tls":map[string]any{
					"enabled":true,"server_name":inbound.TLS.ServerName,
					"utls":map[string]any{"enabled":true,"fingerprint":"chrome"},
					"reality":map[string]any{"enabled":true,"public_key":fixture.PublicKey,"short_id":inbound.TLS.Reality.ShortID[0]},
				},
			}},
			"route":map[string]any{"final":"vpn"},
		}
		removalStartSingBox(t,ctx,binary,clientConfig,port)
		removalWaitTraffic(t,ctx,port,origin.URL)
		proxies[user.Name] = port
	}
	revoked := proxies[task.CredentialRemoval.AccountID]
	if revoked == 0 || len(proxies) != 2 {
		t.Fatal("target account not identifiable among Manager's two applied credentials")
	}
	var survivor int
	for id,port := range proxies {if id != task.CredentialRemoval.AccountID {survivor=port}}
	if survivor == 0 {t.Fatal("missing independent survivor credential")}

	streams := []io.ReadCloser{}
	for _, proxyPort := range []int{revoked,survivor} {
		proxy := &url.URL{Scheme:"http",Host:fmt.Sprintf("127.0.0.1:%d",proxyPort)}
		transport := &http.Transport{Proxy:http.ProxyURL(proxy)}
		defer transport.CloseIdleConnections()
		request,_ := http.NewRequestWithContext(ctx,http.MethodGet,origin.URL+"/stream",nil)
		response,err := (&http.Client{Transport:transport,Timeout:12*time.Second}).Do(request)
		if err != nil {t.Fatal("existing Manager credential stream did not start",err)}
		defer response.Body.Close()
		first := make([]byte,4)
		if _,err = io.ReadFull(response.Body,first);err != nil || string(first)!="open" {
			t.Fatal("Manager credential stream did not carry data",err)
		}
		streams=append(streams,response.Body)
	}

	report,err := executor.Execute(ctx,task)
	if err != nil || report.State!="succeeded" || report.OperationID!=task.CredentialRemoval.OperationID ||
		report.ActiveRuntimeHash!=task.CredentialRemoval.CandidateRuntimeHash ||
		report.SessionEffect!="shared_process_restart_observed" {
		t.Fatal("Manager's prepared candidate was not applied by actual Agent/sing-box",err,report.State,report.Code)
	}
	replay,replayErr := executor.Execute(ctx,task)
	if replayErr != nil || replay!=report {t.Fatal("durable Agent result replay mismatch",replayErr)}

	for _, stream := range streams {
		done := make(chan error,1)
		go func(body io.Reader){_,readErr:=io.Copy(io.Discard,body);done<-readErr}(stream)
		select {
		case <-done:
		case <-time.After(3*time.Second):
			t.Fatal("already-open VLESS/Reality stream survived sing-box restart")
		}
	}
	removalWaitTraffic(t,ctx,survivor,origin.URL)
	if removalTraffic(ctx,revoked,origin.URL) {
		t.Fatal("Manager revoked account's cached UUID still authenticates")
	}
	removalWaitTraffic(t,ctx,survivor,origin.URL)
	t.Log("Manager PostgreSQL candidate revoked actual Reality credential, severed both old streams and preserved unaffected account")
}

// Separate disposable systemd unit; never select or restart an installed
// RouteGate service. Requires an empty /run/systemd/system unit name.
func startManagerCandidateRealityUnit(t *testing.T,ctx context.Context,binary,active string) string {
	t.Helper()
	unit:="routegate-manager-removal-test-"+strconv.Itoa(os.Getpid())+".service"
	unitPath:=filepath.Join("/run/systemd/system",unit)
	if _,err:=os.Lstat(unitPath);!os.IsNotExist(err){t.Fatal("disposable unit already exists",err)}
	content:="[Unit]\nDescription=Isolated Manager-prepared VLESS removal\n[Service]\nType=simple\nExecStart="+strconv.Quote(binary)+" run -c "+strconv.Quote(active)+"\nKillMode=control-group\nStandardOutput=null\nStandardError=null\n[Install]\nWantedBy=multi-user.target\n"
	if err:=os.WriteFile(unitPath,[]byte(content),0600);err!=nil{t.Fatal(err)}
	t.Cleanup(func(){
		cleanup,cancel:=context.WithTimeout(context.Background(),15*time.Second)
		defer cancel()
		_ = exec.CommandContext(cleanup,"systemctl","disable","--now",unit).Run()
		_ = os.Remove(unitPath)
		_ = exec.CommandContext(cleanup,"systemctl","daemon-reload").Run()
	})
	for _,args:=range [][]string{{"daemon-reload"},{"enable","--now",unit}}{
		if err:=exec.CommandContext(ctx,"systemctl",args...).Run();err!=nil{
			t.Fatal("isolated Manager-prepared systemd setup",err)
		}
	}
	return unit
}
