"""Real data-plane continuation of bootstrap E2E, ONLY on a disposable runner.

Do not print API responses, links, keys, configs, or subprocess diagnostics:
they contain test credentials. The runner destroys private logs afterwards.
"""
import argparse
import base64
from contextlib import ExitStack
import hashlib
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import os
import re
from pathlib import Path
import socket
import subprocess
import threading
import time
from urllib.error import HTTPError, URLError
from urllib.parse import parse_qs, urlsplit
from urllib.request import ProxyHandler, Request, build_opener

TARGET = "reality-target.routegate.test"
BAD_TARGET = "nx.routegate.invalid"
MARKER = b"routegate-real-reality-data-plane\n"


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def isolated(manager):
    require(os.environ.get("GITHUB_ACTIONS") == "true"
            and os.environ.get("RUNNER_ENVIRONMENT") == "github-hosted"
            and os.environ.get("ROUTEGATE_E2E_ISOLATED") == "1",
            "This test requires an explicitly isolated GitHub-hosted runner")
    address = urlsplit(manager)
    require(address.scheme == "http" and address.hostname == "127.0.0.1"
            and address.port and not address.username and not address.password
            and not address.query and not address.fragment and not address.path,
            "Manager must be the runner-local HTTP service")


def client_config(link):
    """Use the actual served link, without overriding its endpoint or keys."""
    uri = urlsplit(link)
    query = parse_qs(uri.query)
    require(uri.scheme == "vless" and uri.hostname == "127.0.0.1"
            and uri.port and uri.username, "Expected a runner-local VLESS link")
    def field(name):
        value = query.get(name, [])
        require(len(value) == 1 and value[0], "Missing or ambiguous client parameter")
        return value[0]
    require(field("type") == "tcp" and field("security") == "reality",
            "Expected TCP Reality client material")
    return {
        "log": {"level": "error"},
        "inbounds": [{"type": "socks", "listen": "127.0.0.1", "listen_port": 10808}],
        "outbounds": [{"type": "vless", "tag": "vpn", "server": uri.hostname,
                       "server_port": uri.port, "uuid": uri.username,
                       "flow": field("flow"),
                       "tls": {"enabled": True, "server_name": field("sni"),
                               "utls": {"enabled": True, "fingerprint": field("fp")},
                               "reality": {"enabled": True, "public_key": field("pbk"),
                                           "short_id": field("sid")}}}],
        "route": {"final": "vpn"},
    }


def verify_client_binding(client, active):
    """Compare private material locally; errors contain only field names."""
    outbound = client["outbounds"][0]
    matches = [i for i in active.get("inbounds", [])
               if i.get("type") == "vless" and i.get("listen_port") == outbound["server_port"]]
    require(len(matches) == 1, "Applied client binding mismatch: listener")
    inbound = matches[0]
    users = [u for u in inbound.get("users", []) if u.get("uuid") == outbound["uuid"]]
    require(len(users) == 1, "Applied client binding mismatch: UUID")
    require(users[0].get("flow", "") == outbound.get("flow", ""),
            "Applied client binding mismatch: flow")
    tls, peer = inbound.get("tls", {}), outbound["tls"]
    require(tls.get("enabled") is True and tls.get("server_name") == peer["server_name"],
            "Applied client binding mismatch: SNI")
    reality = tls.get("reality", {})
    require(reality.get("enabled") is True and peer["reality"]["short_id"] in reality.get("short_id", []),
            "Applied client binding mismatch: short ID")
    try:
        private = base64.urlsafe_b64decode(reality["private_key"] + "===")
        public = base64.urlsafe_b64decode(peer["reality"]["public_key"] + "===")
        require(len(private) == 32 and len(public) == 32, "Invalid Reality key length")
        derived = subprocess.run(["openssl", "pkey", "-inform", "DER", "-pubout", "-outform", "DER"],
                                 input=bytes.fromhex("302e020100300506032b656e04220420") + private,
                                 capture_output=True, timeout=5, check=False)
        expected = bytes.fromhex("302a300506032b656e032100") + public
        require(derived.returncode == 0 and derived.stdout == expected,
                "Applied client binding mismatch: Reality key pair")
    except (KeyError, ValueError, OSError, subprocess.TimeoutExpired):
        raise RuntimeError("Cannot verify applied Reality key pair") from None


class API:
    def __init__(self, manager, token):
        self.manager, self.token = manager, token
        self.opener = build_opener(ProxyHandler({}))

    def call(self, method, path, body=None, expected=200):
        request = Request(self.manager + path,
                          data=None if body is None else json.dumps(body).encode(),
                          method=method, headers={"Authorization": "Bearer " + self.token,
                                                  "Content-Type": "application/json"})
        try:
            with self.opener.open(request, timeout=15) as response:
                status, raw = response.status, response.read()
        except HTTPError as error:
            status, raw = error.code, error.read()
        except (URLError, TimeoutError):
            raise RuntimeError("Runner-local Manager request failed") from None
        require(status == expected, f"API {method} returned HTTP {status}; expected {expected}")
        return json.loads(raw) if raw else {}

    def wait_job(self, path, expected="succeeded", timeout=240, phase="unspecified"):
        deadline = time.monotonic() + timeout
        last_status = "not_observed"
        while time.monotonic() < deadline:
            job = self.call("GET", path)
            status = job.get("status")
            # Print only a fixed, non-sensitive status label on timeout.
            # Never include the job payload, response, IDs or credentials.
            last_status = status if status in ("pending", "queued", "in_progress", "running",
                                                "succeeded", "failed") else "unrecognized"
            if status in ("succeeded", "failed"):
                require(status == expected, f"Job ended {status}; expected {expected}")
                return job
            time.sleep(1)
        # Only allowlisted phase names are emitted; never print journal content.
        phases = ("witness_before", "witness_unavailable", "witness_ready",
                  "selected", "validated", "applied", "restart", "healthcheck",
                  "persistence", "listener", "report")
        last_phase = "not_observed"
        try:
            journal = subprocess.run(
                ["journalctl", "-u", "routegate-agent.service", "--no-pager",
                 "--output=cat", "--since=-15min"],
                capture_output=True, text=True, timeout=8, check=False)
            if journal.returncode == 0:
                for line in journal.stdout.splitlines():
                    if "config apply phase" in line or "runtime mutation preflight" in line:
                        for phase_name in phases:
                            if f"phase={phase_name}" in line:
                                last_phase = phase_name
        except (OSError, subprocess.TimeoutExpired):
            pass
        marker = Path("/var/lib/routegate-agent/runtime-mutations/mutation-inflight.json")
        admission = "intent_present" if marker.exists() else "no_intent_file"
        # /proc/<pid>/comm contains only a short executable name, never
        # command-line arguments, environment, URLs, credentials or logs.
        # Inspect process names only on the disposable CI runner.
        process_names = set()
        for entry in Path("/proc").iterdir():
            if not entry.name.isdecimal():
                continue
            try:
                process_names.add((entry / "comm").read_text().strip())
            except (OSError, UnicodeError):
                continue
        package_manager = "running" if process_names.intersection(
            {"apt-get", "apt", "dpkg", "dpkg-deb", "gpgv"}) else "not_observed"
        agent = subprocess.run(
            ["systemctl", "is-active", "--quiet", "routegate-agent.service"],
            stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=5,
            check=False)
        agent_service = "active" if agent.returncode == 0 else "not_active"
        binary = "present" if Path("/usr/bin/sing-box").is_file() else "absent"
        source = "present" if Path("/etc/apt/sources.list.d/sagernet.sources").is_file() else "absent"
        raise RuntimeError(
            f"Agent job exceeded its time budget (phase={phase}, last_agent_phase={last_phase}, status={last_status}, "
            f"admission={admission}, package_manager={package_manager}, "
            f"agent_service={agent_service}, sing_box_binary={binary}, "
            f"repository_source={source})")


def run(*args):
    # Fixed labels only: argv, stdout and stderr can contain credentials.
    phase = {("openssl", "req"): "tls_fixture",
             ("systemctl", "show"): "runtime_state",
             ("sing-box", "check"): "client_config_check",
             ("curl", "--fail"): "reality_traffic"}.get(tuple(args[:2]), "local_command")
    try:
        result = subprocess.run(args, capture_output=True, timeout=30, check=False)
    except subprocess.TimeoutExpired:
        raise RuntimeError(f"Local command timed out (phase={phase}, budget=30s)") from None
    require(result.returncode == 0,
            f"Local command failed (phase={phase}, exit_code={result.returncode}; private diagnostics suppressed)")
    return result.stdout



def private_error_flags(data):
    """Return only fixed categories; never copy text from private logs."""
    text = data.decode("utf-8", errors="replace").lower()
    patterns = {"verification": "verif", "certificate": "certificate",
                "handshake": "handshake", "refused": "connection refused",
                "reset": "connection reset", "timeout": "timeout",
                "eof": "eof", "permission": "permission denied",
                "unreachable": "unreachable", "unsupported": "unsupported",
                "invalid": "invalid", "missing": "not found",
                "invalid_reality": "processed invalid connection",
                "target_dial": "failed to dial dest", "tls_record": "first record",
                "key_share": "key share", "cipher": "shared cipher",
                "user": "unknown user", "flow": "flow", "blocked": "reject"}
    return ",".join(label for label, needle in patterns.items() if needle in text) or "unclassified"


def traffic_diagnostics(work, client):
    try:
        version = subprocess.run(["sing-box", "version"], capture_output=True, timeout=5, check=False)
        match = re.search(rb"sing-box version ([0-9]+\.[0-9]+\.[0-9]+)", version.stdout)
        runtime_version = match.group(1).decode("ascii") if match else "unknown"
    except (OSError, subprocess.TimeoutExpired):
        runtime_version = "unknown"
    flags = {}
    for name, path in {"vpn-client": work / "vpn-client.log",
                       "reality-target": work / "reality-target.log",
                       "server": Path("/var/lib/sing-box/routegate-presence.log")}.items():
        try:
            with path.open("rb") as log:
                log.seek(0, 2)
                log.seek(max(0, log.tell() - 65536))
                flags[name] = private_error_flags(log.read(65536))
        except OSError:
            flags[name] = "unavailable"
    alive = "yes" if client.poll() is None else "no"
    return (f"sing_box={runtime_version}, client_alive={alive}, "
            f"client_flags={flags['vpn-client']}, target_flags={flags['reality-target']}, "
            f"server_flags={flags['server']}")


def stop(process):
    if process.poll() is None:
        process.terminate()
        try:
            process.wait(timeout=5)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait(timeout=5)


def wait_listener(port, process):
    deadline = time.monotonic() + 15
    while time.monotonic() < deadline:
        require(process.poll() is None, "Local test process exited before becoming ready")
        try:
            with socket.create_connection(("127.0.0.1", port), timeout=1):
                return
        except OSError:
            time.sleep(0.2)
    raise RuntimeError("Local test listener did not become ready")


def service_state():
    return run("systemctl", "show", "sing-box.service", "--property=MainPID",
               "--property=ActiveEnterTimestampMonotonic", "--property=NRestarts")


def active_hash():
    return hashlib.sha256(Path("/etc/sing-box/config.json").read_bytes()).digest()


class Origin(BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(200)
        self.send_header("Content-Length", str(len(MARKER)))
        self.end_headers()
        self.wfile.write(MARKER)

    def log_message(self, *_args):
        pass


def traffic(port, proxy_port=10808):
    body = run("curl", "--fail", "--silent", "--show-error", "--max-time", "15",
               "--noproxy", "", "--socks5-hostname", f"127.0.0.1:{proxy_port}",
               f"http://127.0.0.1:{port}/through-reality")
    require(body == MARKER, "VPN traffic did not reach the isolated origin")


def reference_client_probe(work, config, origin_port, binary):
    """Diagnostic only: success never converts the original failure to a pass."""
    if binary is None:
        return "not_configured"
    probe = json.loads(json.dumps(config))
    with socket.socket() as listener:
        listener.bind(("127.0.0.1", 0))
        proxy_port = listener.getsockname()[1]
    probe["inbounds"][0]["listen_port"] = proxy_port
    path = work / "reference-client.json"
    path.write_text(json.dumps(probe))
    process = None
    try:
        with (work / "reference-client.log").open("wb") as log:
            process = subprocess.Popen([str(binary), "run", "-c", str(path)],
                                       stdout=log, stderr=log)
            wait_listener(proxy_port, process)
            traffic(origin_port, proxy_port=proxy_port)
        return "passed"
    except (RuntimeError, OSError, subprocess.TimeoutExpired):
        return "failed"
    finally:
        if process is not None:
            stop(process)


def exercise(work, manager, reference_client=None):
    isolated(manager)
    require(os.geteuid() == 0, "The isolated continuation requires root")
    os.umask(0o077)
    api = API(manager, (work / "admin.token").read_text().strip())
    server = (work / "server.id").read_text().strip()
    base = "/api/v1/servers/" + server
    with ExitStack() as stack:
        # Hosts entry exists only on the disposable runner, never on a node.
        with open("/etc/hosts", "a", encoding="utf-8") as hosts:
            hosts.write(f"\n127.0.0.1 {TARGET}\n")
        run("openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "1",
            "-subj", f"/CN={TARGET}", "-addext", f"subjectAltName=DNS:{TARGET}",
            "-keyout", str(work / "reality.key"), "-out", str(work / "reality.crt"))
        tls_log = stack.enter_context(open(work / "reality-target.log", "wb"))
        tls = subprocess.Popen(["openssl", "s_server", "-accept", "127.0.0.1:443",
                                "-cert", str(work / "reality.crt"),
                                "-key", str(work / "reality.key"), "-tls1_3", "-www"],
                               stdout=tls_log, stderr=tls_log)
        stack.callback(stop, tls)
        wait_listener(443, tls)
        origin = ThreadingHTTPServer(("127.0.0.1", 0), Origin)
        stack.callback(origin.server_close)
        thread = threading.Thread(target=origin.serve_forever, daemon=True)
        thread.start()
        stack.callback(origin.shutdown)

        # Capture only a systemd monotonic timestamp; no logs or unit metadata.
        # A changed timestamp proves the Agent service restarted during install.
        def agent_activation():
            result = subprocess.run(
                ["systemctl", "show", "--value", "--property=ActiveEnterTimestampMonotonic",
                 "routegate-agent.service"],
                capture_output=True, text=True, timeout=5, check=False)
            value = result.stdout.strip()
            return value if result.returncode == 0 and value.isdecimal() else None

        agent_started_before = agent_activation()
        print("[remote-vpn-e2e] Installing sing-box through the registered Agent.", flush=True)
        install = api.call("POST", base + "/vpn-core/installations",
                           {"operation": "install_sing_box"}, expected=202)
        try:
            api.wait_job(base + "/vpn-core/installations/" + install["job"]["id"], timeout=660, phase="install")
        except RuntimeError as error:
            agent_started_after = agent_activation()
            if agent_started_before is None or agent_started_after is None:
                restart = "unknown"
            elif agent_started_before != agent_started_after:
                restart = "yes"
            else:
                restart = "no"
            raise RuntimeError(f"{error} (agent_restarted={restart})") from None
        # Successful installation must release durable admission before config apply.
        install_intent = Path("/var/lib/routegate-agent/runtime-mutations/mutation-inflight.json")
        require(not install_intent.exists(),
                "Successful VPN Core installation retained runtime mutation intent")
        print("[remote-vpn-e2e] sing-box installation completed; admission released.", flush=True)
        api.call("POST", base + "/protocol-settings/recommended", {"serverName": TARGET})
        account = api.call("POST", "/api/v1/vpn-accounts",
                           {"displayName": "Reality E2E", "serverId": server}, expected=201)
        account_base = "/api/v1/vpn-accounts/" + account["id"]
        api.call("POST", account_base + "/activate", {})
        api.call("GET", account_base + "/client-connection", expected=409)

        def render_apply(expected):
            rendered = api.call("POST", base + "/config/render", {}, expected=201)
            version = rendered["configVersion"]["id"]
            checked = api.call("POST", base + "/config/versions/" + version + "/validate", {})
            require(checked["validationResult"]["valid"], "Manager validation rejected test config")
            submitted = api.call("POST", base + "/config/versions/" + version + "/apply", {}, expected=202)
            print("[remote-vpn-e2e] Awaiting config apply result.", flush=True)
            job = api.wait_job(base + "/config/apply-jobs/" + submitted["job"]["id"], expected, phase="config_apply")
            return job

        render_apply("succeeded")
        link = api.call("GET", account_base + "/client-connection")["vlessLink"]
        config = client_config(link)
        require(config["outbounds"][0]["tls"]["server_name"] == TARGET,
                "Served Reality name differs from the applied fixture")
        verify_client_binding(config, json.loads(Path("/etc/sing-box/config.json").read_text()))
        print("[remote-vpn-e2e] Served client matches applied UUID, flow, SNI, short ID, port and derived Reality public key.", flush=True)
        client_path = work / "vpn-client.json"
        client_path.write_text(json.dumps(config))
        run("sing-box", "check", "-c", str(client_path))
        client_log = stack.enter_context(open(work / "vpn-client.log", "wb"))
        client = subprocess.Popen(["sing-box", "run", "-c", str(client_path)],
                                  stdout=client_log, stderr=client_log)
        stack.callback(stop, client)
        wait_listener(10808, client)
        try:
            traffic(origin.server_port)
        except RuntimeError as error:
            diagnostic = traffic_diagnostics(work, client)
            reference = reference_client_probe(work, config, origin.server_port, reference_client)
            raise RuntimeError(f"{error} ({diagnostic}, reference_1_12_8={reference})") from None
        before_hash, before_state = active_hash(), service_state()
        print("[remote-vpn-e2e] Real SOCKS -> VLESS/Reality -> origin traffic passed.", flush=True)

        api.call("PATCH", base + "/protocol-settings", {"realityServerName": BAD_TARGET})
        require(api.call("GET", account_base + "/client-connection")["vlessLink"] == link,
                "Saving unapplied settings changed client material")
        failure = render_apply("failed")
        require(failure["resultPayload"].get("failedStage") == "validate",
                "Bad Reality target did not fail before config replacement")
        require("DNS" in failure.get("errorMessage", "") or "no such host" in failure.get("errorMessage", ""),
                "Bad Reality target did not report DNS failure")
        require(active_hash() == before_hash and service_state() == before_state,
                "Rejected config changed the active config or restarted sing-box")
        require(api.call("GET", account_base + "/client-connection")["vlessLink"] == link,
                "Failed apply changed the served client material")
        traffic(origin.server_port)
        api.call("PATCH", base + "/protocol-settings", {"realityServerName": TARGET})
        print("[remote-vpn-e2e] NXDOMAIN rejected at validate; config, process, client link and traffic preserved.", flush=True)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--work-dir", type=Path, required=True)
    parser.add_argument("--manager", required=True)
    parser.add_argument("--reference-client", type=Path)
    args = parser.parse_args()
    try:
        exercise(args.work_dir, args.manager, args.reference_client)
    except Exception as error:
        # Never interpolate arbitrary API/subprocess errors that may hold keys.
        print("[remote-vpn-e2e] FAILED: " + (str(error) if isinstance(error, RuntimeError)
                                             else type(error).__name__), flush=True)
        raise SystemExit(1)
