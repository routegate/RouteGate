"""Contract tests for the frontend-only production publisher (no network/SSH)."""
import hashlib
import importlib.util
import io
from pathlib import Path
import tarfile
import tempfile
import unittest
from unittest import mock

SCRIPT = Path(__file__).with_name("production-like-frontend-static.py")
spec = importlib.util.spec_from_file_location("frontend_static", SCRIPT)
frontend = importlib.util.module_from_spec(spec)
spec.loader.exec_module(frontend)

REV = "a" * 40
INDEX = b'<html><script type="module" src="/assets/index-123.js"></script><link rel="stylesheet" href="/assets/index-456.css"></html>'


def release(path, payload=None, extra=None):
    members = {
        "dist/index.html": INDEX,
        "dist/assets/index-123.js": b"hello from new JS",
        "dist/assets/index-456.css": b".health-row{gap:9px}",
        "dist/routegate-ui-version.txt": (REV + "\n").encode(),
        "dist/connect.html": b"must not be deployed",
        "dist/favicon.svg": b"unrelated root file",
    }
    members.update(payload or {})
    with tarfile.open(path, "w:gz") as tar:
        for name, data in members.items():
            item = tarfile.TarInfo(name)
            item.size = len(data)
            tar.addfile(item, io.BytesIO(data))
        if extra is not None:
            tar.addfile(extra)
    return hashlib.sha256(path.read_bytes()).hexdigest()


class FrontendStaticTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.base = Path(self.tmp.name)
        self.root = self.base / "routegate"
        self.root.mkdir()
        (self.root / "index.html").write_bytes(b"previous UI")
        (self.root / "bootstrap").mkdir()
        (self.root / "bootstrap" / "installer.sh").write_bytes(b"production bootstrap")
        (self.root / "connect.html").write_bytes(b"old connection landing")
        (self.root / "assets").mkdir()
        (self.root / "assets" / "old.js").write_bytes(b"old script")
        self.bundle = self.base / "ui.tar.gz"

    def test_atomic_frontend_only_update_preserves_bootstrap_and_legacy_assets(self):
        digest = release(self.bundle)
        checked = []

        def verify(commit, html):
            checked.append((commit, html))
            self.assertEqual((self.root / "index.html").read_bytes(), INDEX)

        frontend.deploy(REV, self.bundle, digest, root=self.root, verifier=verify)
        self.assertEqual(checked, [(REV, INDEX)])
        self.assertEqual((self.root / "routegate-ui-version.txt").read_text(), REV + "\n")
        self.assertEqual((self.root / "assets" / "index-123.js").read_bytes(), b"hello from new JS")
        self.assertEqual((self.root / "assets" / "old.js").read_bytes(), b"old script")
        self.assertEqual((self.root / "bootstrap" / "installer.sh").read_bytes(), b"production bootstrap")
        self.assertEqual((self.root / "connect.html").read_bytes(), b"old connection landing")
        self.assertEqual((self.base / ".routegate-ui-backups" / (REV + ".index.html.bak")).read_bytes(), b"previous UI")

    def test_failed_postflight_restores_previous_index_without_restart(self):
        digest = release(self.bundle)
        old = self.root / "routegate-ui-version.txt"
        old.write_text("old revision\n")

        def fail(*_):
            raise OSError("public endpoint unavailable")

        with self.assertRaises(OSError):
            frontend.deploy(REV, self.bundle, digest, root=self.root, verifier=fail)
        self.assertEqual((self.root / "index.html").read_bytes(), b"previous UI")
        self.assertEqual(old.read_bytes(), b"old revision\n")
        self.assertEqual((self.root / "assets" / "old.js").read_bytes(), b"old script")
        self.assertEqual((self.root / "bootstrap" / "installer.sh").read_bytes(), b"production bootstrap")

    def test_missing_previous_marker_is_restored_as_absent(self):
        digest = release(self.bundle)
        with self.assertRaises(RuntimeError):
            frontend.deploy(REV, self.bundle, digest, root=self.root,
                            verifier=lambda *_: (_ for _ in ()).throw(RuntimeError("no")))
        self.assertFalse((self.root / "routegate-ui-version.txt").exists())

    def test_rejects_invalid_revision_or_hash_without_mutation(self):
        digest = release(self.bundle)
        for revision, checksum in [("invalid", digest), (REV, "0" * 64)]:
            with self.subTest(revision=revision, checksum=checksum), self.assertRaises(frontend.DeploymentRefused):
                frontend.deploy(revision, self.bundle, checksum, root=self.root, verifier=lambda *_: None)
        self.assertEqual((self.root / "index.html").read_bytes(), b"previous UI")

    def test_rejects_archive_path_traversal(self):
        digest = release(self.bundle, {"dist/../../etc/passwd": b"bad"})
        with self.assertRaises(frontend.DeploymentRefused):
            frontend.read_archive(self.bundle, REV, digest)

    def test_rejects_linked_archive_members(self):
        link = tarfile.TarInfo("dist/assets/suspicious")
        link.type = tarfile.SYMTYPE
        link.linkname = "/etc/shadow"
        digest = release(self.bundle, extra=link)
        with self.assertRaises(frontend.DeploymentRefused):
            frontend.read_archive(self.bundle, REV, digest)

    def test_rejects_overwriting_old_hashed_asset(self):
        (self.root / "assets" / "index-123.js").write_bytes(b"not the new JS")
        digest = release(self.bundle)
        with self.assertRaises(frontend.DeploymentRefused):
            frontend.deploy(REV, self.bundle, digest, root=self.root, verifier=lambda *_: None)
        self.assertEqual((self.root / "index.html").read_bytes(), b"previous UI")

    def test_rejects_public_root_symlink(self):
        digest = release(self.bundle)
        fake_root = self.base / "linked"
        fake_root.symlink_to(self.root, target_is_directory=True)
        with self.assertRaises(frontend.DeploymentRefused):
            frontend.deploy(REV, self.bundle, digest, root=fake_root, verifier=lambda *_: None)

    def test_new_index_must_reference_available_immutable_assets(self):
        digest = release(self.bundle, {"dist/index.html": b'<script src="/assets/missing.js"></script>'})
        with self.assertRaises(frontend.DeploymentRefused):
            frontend.read_archive(self.bundle, REV, digest)

    def test_script_never_controls_services_or_database(self):
        script = SCRIPT.read_text()
        for forbidden in ("systemctl", "pg_restore", "psql", "nginx -s", "routegate-agent", "sing-box", "subprocess.run"):
            self.assertNotIn(forbidden, script)


if __name__ == "__main__":
    unittest.main()
