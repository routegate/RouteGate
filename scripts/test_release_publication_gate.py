"""Fail-closed static guardrails for public RouteGate release publication.

This test never contacts GitHub or changes release state.
"""
from pathlib import Path
import unittest


ROOT = Path(__file__).resolve().parents[1]
WORKFLOW = ROOT / ".github" / "workflows" / "release.yml"


class ReleasePublicationGateTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.workflow = WORKFLOW.read_text(encoding="utf-8")
        marker = "      - name: Publish GitHub release assets"
        cls.publish = cls.workflow.split(marker, 1)[1]

    def test_only_tag_pushes_publish(self):
        self.assertIn("if: github.event_name == 'push'", self.publish)
        self.assertIn('tags:\n      - "v*"', self.workflow)
        self.assertIn("workflow_dispatch:", self.workflow)

    def test_tag_build_creates_draft_not_public_release(self):
        self.assertEqual(self.publish.count("gh release create"), 2)
        self.assertEqual(self.publish.count("--draft"), 2)
        self.assertEqual(self.publish.count("--verify-tag"), 2)
        self.assertIn('release_flags+=(--prerelease)', self.publish)

    def test_existing_release_assets_are_never_overwritten(self):
        self.assertNotIn("gh release upload", self.publish)
        self.assertNotIn("--clobber", self.publish)
        gate = 'if gh release view "$TAG_NAME" >/dev/null 2>&1; then'
        self.assertIn(gate, self.publish)
        first_create = self.publish.index("gh release create")
        guard = self.publish[self.publish.index(gate):first_create]
        self.assertIn("exit 1", guard)
        self.assertIn("Refusing to replace assets", guard)

    def test_attestations_and_checksums_present_before_draft(self):
        verify = self.workflow.index(
            "Verify release manifest, checksums and bundle structure"
        )
        provenance = self.workflow.index("Verify release provenance policy")
        publication = self.workflow.index("Publish GitHub release assets")
        self.assertLess(verify, provenance)
        self.assertLess(provenance, publication)
        self.assertIn("sha256sum -c SHA256SUMS", self.workflow)
        self.assertIn("sha256sum -c INSTALLER_SHA256SUMS", self.workflow)


if __name__ == "__main__":
    unittest.main()
