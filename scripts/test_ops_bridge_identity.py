"""Regression guards for the production-like issue-control trust boundary.

This is a static workflow contract test. It deliberately does NOT dispatch any
GitHub workflow, use secrets, or connect to the production host.
"""
from pathlib import Path
import re
import subprocess
import unittest

WORKFLOW = Path(__file__).resolve().parents[1] / ".github/workflows/production-like-ops.yml"


def issue_job_gate():
    workflow = WORKFLOW.read_text(encoding="utf-8")
    match = re.search(
        r"(?m)^jobs:\n  operate:\n(?:(?!^    if:)[\s\S])*?^    if: >-\n(?P<expression>(?:      [^\r\n]+\n)+)",
        workflow,
    )
    if match is None:
        raise AssertionError("The production-like job has no explicit issue gate")
    return match.group("expression").strip()


class OpsBridgeIdentityTests(unittest.TestCase):
    def test_issue_trigger_is_pinned_to_immutable_repo_and_creator_ids(self):
        gate = issue_job_gate()
        # The repository was moved from user ikaevus to organization routegate.
        # The issue was created by the original owner, not the new organization.
        expected = (
            "github.event_name == 'workflow_dispatch' ||\n"
            "      (github.repository == 'routegate/RouteGate' &&\n"
            "       github.repository_id == '1224779806' &&\n"
            "       github.event.issue.number == 268 &&\n"
            "       github.event.issue.title == 'RouteGate Ops Bridge' &&\n"
            "       github.event.issue.user.id == 62504580)"
        )
        self.assertEqual(gate, expected)
        self.assertNotIn("github.repository_owner", gate)
        self.assertNotIn("github.actor", gate)
        self.assertNotIn("github.event.issue.user.login", gate)

    def test_only_reopened_issues_dispatch_issue_operations(self):
        source = WORKFLOW.read_text(encoding="utf-8")
        self.assertRegex(source, r"(?m)^  issues:\n    types:\n      - reopened$")
        self.assertIn("github.event.issue.number == 268", source)
        self.assertIn("case \"$REQUEST_BODY\" in", source)
        self.assertIn("'operation=diagnose') operation=diagnose", source)
        self.assertIn("'operation=traffic-inspect') operation=traffic-inspect", source)
        self.assertIn("diagnose|diagnose-sing-box|traffic-inspect|", source)
        self.assertIn('exec python3 "$1" inspect', source)
        self.assertIn('scripts/traffic-accounting-rollout.py', source)
        self.assertNotIn('exec python3 "$1" install', source)
        self.assertNotIn('exec python3 "$1" apply', source)
        self.assertIn("Unsupported ops request body.", source)
        self.assertIn("environment: production-like", source)
        self.assertIn("flock -w 600 /run/lock/routegate-production-like.lock", source)

    def test_remote_shell_step_parses_without_executing(self):
        workflow = WORKFLOW.read_text(encoding="utf-8")
        step = workflow.index("      - name: Run allow-listed remote operation\n")
        first = workflow.index("        run: |\n", step) + len("        run: |\n")
        last = workflow.index("      - name: Upload full preflight result\n", first)
        lines = workflow[first:last].splitlines()
        script_lines = []
        for line in lines:
            if line.strip() and not line.startswith("          "):
                self.fail("unexpected YAML indentation in privileged remote script")
            script_lines.append(line[10:] if line else "")
        script = "\n".join(script_lines) + "\n"
        result = subprocess.run(["bash", "-n"], input=script, text=True,
                                capture_output=True, check=False)
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_traffic_inspection_cannot_pass_mutating_subcommands(self):
        source = WORKFLOW.read_text(encoding="utf-8")
        start = source.index('elif [[ "$OPERATION" == "traffic-inspect" ]]; then')
        end = source.index('elif [[ "$OPERATION" == "preflight-applied-client-settings-155" ]]; then', start)
        branch = source[start:end]
        self.assertIn('exec python3 "$1" inspect', branch)
        self.assertNotIn('exec python3 "$1" install', branch)
        self.assertNotIn('exec python3 "$1" apply', branch)
        self.assertIn('flock -w 600 /run/lock/routegate-production-like.lock', branch)
        self.assertIn('sudo -n', branch)
        self.assertIn('StrictHostKeyChecking=yes', source)

    def test_no_workflow_dispatch_from_untrusted_ref_for_sensitive_ops(self):
        source = WORKFLOW.read_text(encoding="utf-8")
        self.assertIn('"$GITHUB_REF" != "refs/heads/main"', source)
        self.assertIn("update-manager", source)
        self.assertIn("publish-bootstrap", source)
        self.assertIn("The %s operation can only be dispatched from main.", source)


if __name__ == "__main__":
    unittest.main()
