"""Real VLESS/Reality data-plane acceptance AFTER the native Hybrid installer.

Runs only as root on a dedicated GitHub-hosted disposable Ubuntu 24.04
runner. Exercises the Manager/Agent installed by install.sh (not in-process
stubs), installs sing-box through the registered Agent and tests loopback
client traffic plus a rejected configuration apply. Never print credentials,
subscription links, traffic configs or private Agent logs.
"""
from contextlib import ExitStack
import json
import os
from pathlib import Path
import socket
import subprocess
import sys
import time
from http.server import ThreadingHTTPServer
from urllib.parse import urlsplit

import remote_node_vpn_e2e as common

DOMAIN = "routegate-hybrid-ci.invalid"
TARGET = common.TARGET
WORK = Path("/home/runner/work/_temp/routegate-hybrid-clean-host-e2e")
MANAGER = "http://127.0.0.1:8080"


def require(condition: bool, message: str) -> None:
    common.require(condition, message)


def setup_decoy() -> None:
    """Test-only TLS 1.3 vhost shares nginx :443, distinct from Manager SNI."""
    with open("/etc/hosts", "a", encoding="utf-8") as hosts:
        hosts.write(f"\n127.0.0.1 {TARGET}\n")
    fixture_dir = WORK / "reality-tls"
    fixture_dir.mkdir(mode=0o700)
    key = fixture_dir / "key.pem"
    cert = fixture_dir / "cert.pem"
    common.run(
        "openssl", "req", "-new", "-x509", "-nodes", "-newkey", "rsa:2048",
        "-sha256", "-days", "1", "-subj", f"/CN={TARGET}",
        "-addext", f"subjectAltName=DNS:{TARGET}",
        "-keyout", str(key), "-out", str(cert),
    )
    destination = Path("/etc/ssl/private/routegate-hybrid-e2e-target")
    destination.mkdir(mode=0o700)
    key_dest = destination / "key.pem"
    cert_dest = destination / "cert.pem"
    key_dest.write_bytes(key.read_bytes())
    cert_dest.write_bytes(cert.read_bytes())
    key_dest.chmod(0o600)
    cert_dest.chmod(0o644)
    vhost = Path("/etc/nginx/sites-enabled/routegate-hybrid-e2e-target")
    require(not vhost.exists(), "Disposable Reality fixture already exists")
    vhost.write_text(
        f"server {{\n"
        f"    listen 443 ssl;\n"
        f"    listen [::]:443 ssl;\n"
        f"    server_name {TARGET};\n"
        f"    ssl_certificate {cert_dest};\n"
        f"    ssl_certificate_key {key_dest};\n"
        f"    ssl_protocols TLSv1.3;\n"
        f"    location / {{ return 204; }}\n"
        f"}}\n", encoding="utf-8",
    )
    common.run("nginx", "-t")
    common.run("systemctl", "reload", "nginx")
    # Real HTTPS Manager remains served on the SAME port after adding decoy SNI.
    common.run("curl", "--fail", "--silent", "--show-error", "--max-time", "10",
               "--noproxy", "*", f"https://{DOMAIN}/api/admin/health")
    common.run("curl", "--fail", "--silent", "--show-error", "--max-time", "10",
               "--noproxy", "*", "--insecure", f"https://{TARGET}/")


def wait_for_agent(api: common.API, base: str) -> None:
    deadline = time.monotonic() + 80
    while time.monotonic() < deadline:
        response = api.call("GET", base)
        agent = response.get("agent") or {}
        if agent.get("id") and agent.get("lastSeenAt") and agent.get("status") in ("online", "registered"):
            return
        time.sleep(2)
    raise RuntimeError("Hybrid Agent did not report a recent heartbeat")


def run_install_and_traffic() -> None:
    # Double guard: never allow a workstation, self-hosted GitHub runner, or VPS.
    common.isolated(MANAGER)
    require(os.geteuid() == 0, "Root required on disposable runner")
    require(os.environ.get("RUNNER_TEMP") == "/home/runner/work/_temp",
            "Not an allowed disposable runner temp root")
    require(WORK.is_dir(), "Dedicated transient test directory is missing")
    require(Path("/etc/routegate/install-state.env").is_file(),
            "Run native Hybrid install before runtime acceptance")
    require(f"VERSION=ci-hybrid-e2e\n" in Path("/etc/routegate/install-state.env").read_text(),
            "Installed bundle version does not match disposable test")
    require(Path("/etc/sing-box/config.json").exists() is False,
            "VPN runtime is already configured before the Agent test")
    assert Path("/etc/nginx/sites-available/routegate").is_file()
    # No secrets printed or copied into test assertions.
    token = (WORK / "admin.token").read_text().strip()
    server_id = (WORK / "server.id").read_text().strip()
    require(bool(token) and bool(server_id), "Administrator test session is missing")
    api = common.API(MANAGER, token)
    base = f"/api/v1/servers/{server_id}"

    info = api.call("GET", base)
    require(info["server"]["deploymentRole"] == "hybrid",
            "Installed server is not a Hybrid deployment")
    wait_for_agent(api, base)

    with ExitStack() as stack:
        origin = ThreadingHTTPServer(("127.0.0.1", 0), common.Origin)
        stack.callback(origin.server_close)
        import threading
        thread = threading.Thread(target=origin.serve_forever, daemon=True)
        thread.start()
        stack.callback(origin.shutdown)

        setup_decoy()
        print("[hybrid-vpn-e2e] nginx HTTPS Manager and TLS decoy share TCP 443; VLESS uses 8443.", flush=True)
        installation = api.call("POST", base + "/vpn-core/installations",
                                {"operation": "install_sing_box"}, expected=202)
        api.wait_job(base + "/vpn-core/installations/" + installation["job"]["id"],
                     timeout=660)
        common.run("systemctl", "is-enabled", "routegate-manager")
        require(Path("/usr/bin/sing-box").exists(), "Agent did not install sing-box")

        # Only an isolated test endpoint is changed. The public link must
        # contain the Manager's configured endpoint, without client-side hacks.
        api.call("PATCH", base, {"publicIp": "127.0.0.1"})
        recommended = api.call("POST", base + "/protocol-settings/recommended",
                               {"serverName": TARGET})
        require(recommended.get("vlessPort") == 8443,
                "Hybrid recommended VLESS listener must use TCP 8443, not HTTPS 443")
        account = api.call("POST", "/api/v1/vpn-accounts",
                           {"displayName": "Hybrid CI VLESS", "serverId": server_id},
                           expected=201)
        account_base = "/api/v1/vpn-accounts/" + account["id"]
        api.call("POST", account_base + "/activate", {})
        api.call("GET", account_base + "/client-connection", expected=409)

        def render_apply(expected: str) -> dict:
            rendered = api.call("POST", base + "/config/render", {}, expected=201)
            version = rendered["configVersion"]["id"]
            checked = api.call("POST", base + "/config/versions/" + version + "/validate", {})
            require(checked["validationResult"]["valid"],
                    "Manager rejected rendered Hybrid config")
            submitted = api.call("POST", base + "/config/versions/" + version + "/apply",
                                 {}, expected=202)
            return api.wait_job(base + "/config/apply-jobs/" + submitted["job"]["id"],
                                expected, timeout=300)

        render_apply("succeeded")
        common.run("systemctl", "is-active", "sing-box")
        link = api.call("GET", account_base + "/client-connection")["vlessLink"]
        uri = urlsplit(link)
        require(uri.hostname == "127.0.0.1" and uri.port == 8443,
                "Applied client link does not target loopback VLESS on 8443")
        config = common.client_config(link)
        require(config["outbounds"][0]["tls"]["server_name"] == TARGET,
                "Client Reality name disagrees with applied Hybrid config")

        client_config_path = WORK / "hybrid-client.json"
        client_config_path.write_text(json.dumps(config))
        client_config_path.chmod(0o600)
        common.run("sing-box", "check", "-c", str(client_config_path))
        with (WORK / "hybrid-client.log").open("wb") as client_log:
            client = subprocess.Popen(
                ["sing-box", "run", "-c", str(client_config_path)],
                stdout=client_log, stderr=client_log,
            )
            stack.callback(common.stop, client)
            common.wait_listener(10808, client)
            common.traffic(origin.server_port)
            old_hash, old_state = common.active_hash(), common.service_state()
            print("[hybrid-vpn-e2e] VLESS/Reality real loopback client traffic succeeded.", flush=True)

            # Last-known-applied safety: a bad draft must not restart VPN or
            # replace active credentials while the Manager stays available.
            api.call("PATCH", base + "/protocol-settings",
                     {"realityServerName": common.BAD_TARGET})
            require(api.call("GET", account_base + "/client-connection")["vlessLink"] == link,
                    "Draft settings changed published client connection")
            failed = render_apply("failed")
            require(failed.get("resultPayload", {}).get("failedStage") == "validate",
                    "Bad Reality target was not rejected before active apply")
            require(common.active_hash() == old_hash and common.service_state() == old_state,
                    "Failed apply modified active VPN or restarted the runtime")
            require(api.call("GET", account_base + "/client-connection")["vlessLink"] == link,
                    "Failed apply changed current client link")
            common.traffic(origin.server_port)
            common.run("curl", "--fail", "--silent", "--show-error", "--max-time", "10",
                       "--noproxy", "*", f"https://{DOMAIN}/api/admin/health")
            api.call("PATCH", base + "/protocol-settings", {"realityServerName": TARGET})
            print("[hybrid-vpn-e2e] Failed apply preserved VPN runtime, client link and Admin HTTPS.", flush=True)


if __name__ == "__main__":
    try:
        run_install_and_traffic()
    except Exception as exc:
        # RuntimeError messages are hard-coded test assertions or API status
        # codes. Never show raw API payloads, links or subprocess stderr.
        if isinstance(exc, RuntimeError):
            print("[hybrid-vpn-e2e] FAILED: " + str(exc), flush=True)
        else:
            print("[hybrid-vpn-e2e] FAILED: " + type(exc).__name__, flush=True)
        sys.exit(1)
