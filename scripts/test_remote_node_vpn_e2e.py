import base64
import copy
import importlib.util
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import Mock, patch

REPO = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location("vpn_e2e", REPO / "scripts/remote-node-vpn-e2e.py")
e2e = importlib.util.module_from_spec(spec)
spec.loader.exec_module(e2e)
LINK = ("vless://12345678-1234-1234-1234-123456789abc@127.0.0.1:8443"
        "?security=reality&type=tcp&flow=xtls-rprx-vision&sni=reality-target.routegate.test"
        "&fp=chrome&pbk=test-public-key&sid=aabbccdd#test")
ENV = {"GITHUB_ACTIONS": "true", "RUNNER_ENVIRONMENT": "github-hosted",
       "ROUTEGATE_E2E_ISOLATED": "1"}


class VPNContinuationTests(unittest.TestCase):
    def test_binding_uses_derived_public_key_and_rejects_each_stale_field(self):
        # RFC 7748 X25519 test vector, never a deployment credential.
        private = base64.urlsafe_b64encode(bytes.fromhex("77076d0a7318a57d3c16c17251b26645df4c2f87ebc0992ab177fba51db92c2a")).decode().rstrip("=")
        public = base64.urlsafe_b64encode(bytes.fromhex("8520f0098930a754748b7ddcb43ef75a0dbf3a0d26381af4eba4a98eaa9b4e6a")).decode().rstrip("=")
        client = e2e.client_config(LINK.replace("test-public-key", public))
        peer = client["outbounds"][0]
        active = {"inbounds": [{"type": "vless", "listen_port": peer["server_port"],
                  "users": [{"uuid": peer["uuid"], "flow": peer["flow"]}],
                  "tls": {"enabled": True, "server_name": e2e.TARGET,
                          "reality": {"enabled": True, "short_id": ["aabbccdd"], "private_key": private}}}]}
        e2e.verify_client_binding(client, active)
        for field in ("server_port", "uuid", "flow", "server_name", "short_id", "public_key"):
            changed = copy.deepcopy(client)
            out = changed["outbounds"][0]
            if field in out: out[field] = 1 if field == "server_port" else "wrong"
            elif field == "server_name": out["tls"][field] = "wrong"
            else: out["tls"]["reality"][field] = "wrong"
            with self.subTest(field=field), self.assertRaises(RuntimeError) as caught:
                e2e.verify_client_binding(changed, active)
            self.assertNotIn(private, str(caught.exception))
            self.assertNotIn(public, str(caught.exception))

    def test_served_link_is_used_without_overrides(self):
        outbound = e2e.client_config(LINK)["outbounds"][0]
        self.assertEqual((outbound["server"], outbound["server_port"]), ("127.0.0.1", 8443))
        self.assertEqual(outbound["tls"]["server_name"], e2e.TARGET)
        self.assertEqual(outbound["tls"]["reality"]["public_key"], "test-public-key")
        self.assertEqual(outbound["tls"]["reality"]["short_id"], "aabbccdd")
        self.assertNotIn("insecure", outbound["tls"])

    def test_external_endpoint_and_invalid_client_material_are_rejected(self):
        for link in (LINK.replace("127.0.0.1", "77.91.93.150"),
                     LINK.replace("type=tcp", "type=ws"),
                     LINK.replace("security=reality", "security=none"),
                     LINK.replace("&pbk=test-public-key", ""), LINK.replace("#test", "&sid=duplicate#test")):
            with self.subTest(link=link), self.assertRaises(RuntimeError):
                e2e.client_config(link)

    def test_isolation_requires_all_three_markers(self):
        with patch.dict(os.environ, ENV, clear=True):
            e2e.isolated("http://127.0.0.1:18080")
        for key in ENV:
            with self.subTest(key=key), patch.dict(os.environ, {**ENV, key: ""}, clear=True):
                with self.assertRaises(RuntimeError):
                    e2e.isolated("http://127.0.0.1:18080")

    def test_external_or_ambiguous_manager_is_rejected(self):
        with patch.dict(os.environ, ENV, clear=True):
            for url in ("https://routegate.org", "http://192.0.2.1:18080",
                        "http://secret@127.0.0.1:18080", "http://127.0.0.1:18080/path",
                        "http://127.0.0.1:18080?destination=live"):
                with self.subTest(url=url), self.assertRaises(RuntimeError):
                    e2e.isolated(url)

    def test_shell_refuses_local_execution_before_cleanup_or_changes(self):
        result = subprocess.run(["bash", str(REPO / "scripts/remote-node-bootstrap-e2e.sh")],
                                env={**os.environ, "GITHUB_ACTIONS": "false"},
                                capture_output=True, text=True, timeout=5)
        self.assertEqual(result.returncode, 1)
        self.assertIn("explicitly isolated GitHub-hosted runner", result.stderr)
        self.assertNotIn("sudo", result.stderr)

    def test_job_failure_is_bounded_and_does_not_print_payload(self):
        api = e2e.API("http://127.0.0.1:18080", "private-token")
        with patch.object(api, "call", return_value={"status": "failed", "secret": "private-key"}):
            with self.assertRaisesRegex(RuntimeError, "Job ended failed") as caught:
                api.wait_job("/test")
        self.assertNotIn("private", str(caught.exception))
        with patch.object(api, "call", return_value={"status": "failed"}):
            self.assertEqual(api.wait_job("/test", expected="failed")["status"], "failed")

    def test_command_failure_exposes_only_fixed_phase_and_exit_code(self):
        private = "private-token-and-key"
        failure = subprocess.CompletedProcess([], 97, private.encode(), private.encode())
        with patch.object(e2e.subprocess, "run", return_value=failure):
            with self.assertRaisesRegex(RuntimeError, "phase=reality_traffic, exit_code=97") as caught:
                e2e.run("curl", "--fail", private)
        self.assertNotIn(private, str(caught.exception))
        timeout = subprocess.TimeoutExpired([private], 30, output=private, stderr=private)
        with patch.object(e2e.subprocess, "run", side_effect=timeout):
            with self.assertRaisesRegex(RuntimeError, "phase=local_command, budget=30s") as caught:
                e2e.run(private)
        self.assertNotIn(private, str(caught.exception))

    def test_private_log_classification_never_copies_values(self):
        self.assertEqual(e2e.private_error_flags(b"private-key verification failed: connection refused"),
                         "verification,refused")
        self.assertEqual(e2e.private_error_flags(b"private-token-and-address"), "unclassified")

    def test_reference_probe_preserves_config_and_reports_only_outcome(self):
        config = e2e.client_config(LINK)
        original = copy.deepcopy(config)
        for failed in (False, True):
            with tempfile.TemporaryDirectory() as directory, self.subTest(failed=failed):
                with patch.object(e2e.subprocess, "Popen", return_value=Mock()) as launch, \
                     patch.object(e2e, "wait_listener"), patch.object(e2e, "stop") as stopped, \
                     patch.object(e2e, "traffic", side_effect=RuntimeError("private-key") if failed else None) as probe:
                    result = e2e.reference_client_probe(Path(directory), config, 12345, Path("/reference-client"))
                self.assertEqual(result, "failed" if failed else "passed")
                self.assertEqual(config, original)
                self.assertEqual(launch.call_args.args[0][:2], ["/reference-client", "run"])
                self.assertEqual(probe.call_args.args, (12345,))
                self.assertGreater(probe.call_args.kwargs["proxy_port"], 0)
                stopped.assert_called_once()
                self.assertNotIn("private", result)

    def test_reference_pair_is_loopback_only_and_preserves_originals(self):
        config = e2e.client_config(LINK)
        active = {"inbounds": [{"type": "vless", "listen": "::", "listen_port": 8443}]}
        original = copy.deepcopy((config, active))
        with tempfile.TemporaryDirectory() as directory:
            with patch.object(e2e.subprocess, "Popen", return_value=Mock()), \
                 patch.object(e2e, "wait_listener"), patch.object(e2e, "stop") as stopped, \
                 patch.object(e2e, "reference_client_probe", return_value="passed") as probe:
                result = e2e.reference_pair_probe(Path(directory), config, active, 12345, Path("/reference-client"))
            server = e2e.json.loads((Path(directory) / "reference-server.json").read_text())
        self.assertEqual(result, "passed")
        self.assertEqual((config, active), original)
        self.assertEqual(server["inbounds"][0]["listen"], "127.0.0.1")
        self.assertEqual(probe.call_args.args[1]["outbounds"][0]["server_port"], server["inbounds"][0]["listen_port"])
        stopped.assert_called_once()

    def test_traffic_cannot_bypass_the_socks_proxy(self):
        with patch.object(e2e, "run", return_value=e2e.MARKER) as command:
            e2e.traffic(12345)
        args = command.call_args.args
        self.assertEqual(args[args.index("--socks5-hostname") + 1], "127.0.0.1:10808")
        self.assertEqual(args[args.index("--noproxy") + 1], "")
        self.assertEqual(args[-1], "http://127.0.0.1:12345/through-reality")
        with patch.object(e2e, "run", return_value=b"wrong origin"):
            with self.assertRaises(RuntimeError):
                e2e.traffic(12345)


if __name__ == "__main__":
    unittest.main()
