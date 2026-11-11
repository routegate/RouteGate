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

    def test_same_commit_full_hybrid_ci_is_mandatory_before_build(self):
        # Release candidate must execute native host integration on the exact
        # tag SHA, never reuse an unrelated green PR or branch status.
        self.assertIn("  source-policy:", self.workflow)
        self.assertIn("  ci:", self.workflow)
        self.assertIn("needs: source-policy", self.workflow)
        self.assertIn("uses: ./.github/workflows/ci.yml", self.workflow)
        self.assertIn("    needs: ci", self.workflow)
        policy = self.workflow.split("  source-policy:", 1)[1].split(
            "\n  ci:", 1
        )[0]
        self.assertIn("scripts/hybrid-disposable-installer-e2e.sh", policy)
        self.assertIn("scripts/hybrid-vpn-e2e.py", policy)
        self.assertIn("grep -Eq '^  hybrid-native-e2e:'", policy)
        self.assertIn("git merge-base --is-ancestor", policy)
        self.assertIn('^v[0-9]+', policy)
        self.assertIn("refusing to build a release tag outside", policy.lower())

    def test_reusable_ci_cannot_be_skipped_by_release(self):
        ci = (ROOT / ".github" / "workflows" / "ci.yml").read_text(
            encoding="utf-8"
        )
        self.assertIn("  workflow_call:", ci)
        # Current release candidate may be under development before the
        # stacked native Hybrid PR has merged; full E2E presence is checked
        # by source-policy on *tagged* commits, not by PR unit tests.
        self.assertIn("needs: ci", self.workflow)

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

    def test_published_bundle_has_dark_only_frontend_contract(self):
        # A successful source-level UI test is insufficient if release
        # packaging accidentally ships an older index.html. Enforce that
        # the actual tarball is inspected BEFORE creating a draft.
        extract = 'index_html=$(tar -xOzf "$bundle" ./frontend/index.html)'
        self.assertIn(extract, self.workflow)
        self.assertIn('data-theme="dark"', self.workflow)
        self.assertIn('color-scheme: dark', self.workflow)
        self.assertIn("stale frontend (dark-only UI contract missing)", self.workflow)
        bundle_check = self.workflow.index(extract)
        attest = self.workflow.index("Attest release manifest provenance")
        publish = self.workflow.index("Publish GitHub release assets")
        self.assertLess(bundle_check, attest)
        self.assertLess(attest, publish)
        ci = (ROOT / ".github" / "workflows" / "ci.yml").read_text(
            encoding="utf-8"
        )
        self.assertIn(
            "index_html=$(tar -xOzf routegate-ci-test-linux-amd64.tar.gz ./frontend/index.html)",
            ci,
        )
        self.assertIn("CI bundle contains outdated frontend appearance.", ci)

    def test_reusable_ci_rehearses_both_untagged_release_architectures(self):
        ci = (ROOT / ".github" / "workflows" / "ci.yml").read_text(encoding="utf-8")
        script = (ROOT / "scripts" / "test-release-candidate-dry-run.sh").read_text(
            encoding="utf-8"
        )
        self.assertIn("  tagless-release-candidate:", ci)
        self.assertIn("bash scripts/test-release-candidate-dry-run.sh", ci)
        self.assertIn('ARCHITECTURES="amd64 arm64"', script)
        self.assertIn('sha256sum -c SHA256SUMS', script)
        self.assertIn('sha256sum -c INSTALLER_SHA256SUMS', script)
        self.assertIn('verify-target', script)
        self.assertIn('struct.unpack_from("<H", head, 18)[0] == machine', script)
        self.assertIn('stale release frontend', script)
        self.assertIn('Corrupted arm64 artifact unexpectedly passed', script)
        self.assertNotIn("gh release create", script)
        self.assertNotIn("git tag", script)

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
