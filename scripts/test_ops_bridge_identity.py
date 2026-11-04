"""Regression guards for the production-like issue-control trust boundary.

This is a static workflow contract test. It deliberately does NOT dispatch any
GitHub workflow, use secrets, or connect to the production host.
"""
from pathlib import Path
import re
import unittest

WORKFLOW = Path(__file__).resolve().parents[1] / ".github/workflows/production-like-ops.yml"


def issue_job_gate():
    workflow = WORKFLOW.read_text(encoding="utf-8")
    match = re.search(
        r"(?ms)^jobs:\n  operate:\n(?:(?!^    if:).)*^    if: >-\n(?P<expression>(?:      .+\n)+)",
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
        self.assertIn("Unsupported ops request body.", source)
        self.assertIn("environment: production-like", source)
        self.assertIn("flock -w 600 /run/lock/routegate-production-like.lock", source)

    def test_no_workflow_dispatch_from_untrusted_ref_for_sensitive_ops(self):
        source = WORKFLOW.read_text(encoding="utf-8")
        self.assertIn('"$GITHUB_REF" != "refs/heads/main"', source)
        self.assertIn("update-manager", source)
        self.assertIn("publish-bootstrap", source)
        self.assertIn("The %s operation can only be dispatched from main.", source)


if __name__ == "__main__":
    unittest.main()
