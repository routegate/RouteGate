import io
import json
import tarfile
import tempfile
import unittest
from pathlib import Path

from scripts import release_manifest

VERSION = "v0.2.0"
COMMIT = "a" * 40
BUILD_DATE = "2026-08-23T12:00:00Z"
MIGRATION = "000134_distinct_tcp_listener_ports"
REQUIRED_TOOLS = {
    "release_manifest.py",
    "routegate-update-bootstrap.sh",
    "routegate-update-core.sh",
    "routegate-update-role.sh",
    "routegate-update-transaction.sh",
    "routegate-update-verified.sh",
}


class ReleaseManifestTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name)
        self.dist = self.root / "dist"
        self.migrations = self.root / "migrations"
        self.dist.mkdir()
        self.migrations.mkdir()
        (self.migrations / f"{MIGRATION}.up.sql").write_text("SELECT 1;\n", encoding="utf-8")

    def tearDown(self):
        self.temp.cleanup()

    def make_bundle(
        self,
        arch="amd64",
        *,
        metadata_version=VERSION,
        unsafe=False,
        omit_tool=None,
        omit_migrator=False,
    ):
        stage = self.root / f"stage-{arch}"
        for directory in ("bin", "frontend", "metadata", "manager/migrations", "tools"):
            (stage / directory).mkdir(parents=True, exist_ok=True)
        (stage / "bin/routegate-manager").write_text("manager", encoding="utf-8")
        if not omit_migrator:
            (stage / "bin/routegate-migrate").write_text("migrator", encoding="utf-8")
        (stage / "bin/routegate-agent").write_text("agent", encoding="utf-8")
        (stage / "frontend/index.html").write_text("html", encoding="utf-8")
        (stage / f"manager/migrations/{MIGRATION}.up.sql").write_text(
            "SELECT 1;\n", encoding="utf-8"
        )
        for tool in REQUIRED_TOOLS:
            if tool != omit_tool:
                (stage / "tools" / tool).write_text("#!/usr/bin/env bash\n", encoding="utf-8")
        (stage / "metadata/manifest.env").write_text(
            "\n".join(
                [
                    "FORMAT_VERSION=1",
                    f"VERSION={metadata_version}",
                    f"COMMIT={COMMIT}",
                    f"BUILD_DATE={BUILD_DATE}",
                    "OS=linux",
                    f"ARCH={arch}",
                    "",
                ]
            ),
            encoding="utf-8",
        )
        bundle = self.dist / f"routegate-{VERSION}-linux-{arch}.tar.gz"
        with tarfile.open(bundle, "w:gz") as archive:
            archive.add(stage, arcname=".")
            if unsafe:
                entry = tarfile.TarInfo("../escape")
                entry.size = 1
                archive.addfile(entry, io.BytesIO(b"x"))
        return bundle

    def write_checksums(self):
        lines = []
        for bundle in sorted(self.dist.glob("routegate-*.tar.gz")):
            lines.append(f"{release_manifest.sha256_file(bundle)}  {bundle.name}\n")
        (self.dist / "SHA256SUMS").write_text("".join(lines), encoding="utf-8")

    def build(self):
        manifest = release_manifest.build_manifest(
            self.dist, VERSION, COMMIT, BUILD_DATE, self.migrations
        )
        path = self.dist / "release-manifest.json"
        release_manifest.write_manifest(path, manifest)
        return path

    def test_build_and_verify(self):
        self.make_bundle("amd64")
        self.make_bundle("arm64")
        self.write_checksums()
        path = self.build()

        release_manifest.verify_manifest(path, self.dist)

        payload = json.loads(path.read_text(encoding="utf-8"))
        self.assertEqual(payload["database"]["expectedMigration"], MIGRATION)
        self.assertEqual(
            [item["arch"] for item in payload["artifacts"]], ["amd64", "arm64"]
        )

    def test_rejects_missing_migrator(self):
        self.make_bundle(omit_migrator=True)
        self.write_checksums()
        path = self.build()
        with self.assertRaisesRegex(release_manifest.ManifestError, "bin/routegate-migrate"):
            release_manifest.verify_manifest(path, self.dist)

    def test_verify_target_does_not_require_unselected_bundle_file(self):
        amd64 = self.make_bundle("amd64")
        arm64 = self.make_bundle("arm64")
        self.write_checksums()
        path = self.build()
        arm64.unlink()

        descriptor = release_manifest.verify_target(path, self.dist, "linux", "amd64")

        self.assertEqual(descriptor["version"], VERSION)
        self.assertEqual(descriptor["commit"], COMMIT)
        self.assertEqual(descriptor["artifact"]["name"], amd64.name)
        self.assertEqual(descriptor["artifact"]["arch"], "amd64")
        self.assertEqual(
            descriptor["artifact"]["sha256"], release_manifest.sha256_file(amd64)
        )

    def test_verify_target_requires_selected_bundle_file(self):
        amd64 = self.make_bundle("amd64")
        self.make_bundle("arm64")
        self.write_checksums()
        path = self.build()
        amd64.unlink()

        with self.assertRaisesRegex(release_manifest.ManifestError, "artifact is missing"):
            release_manifest.verify_target(path, self.dist, "linux", "amd64")

    def test_verify_target_validates_unselected_checksum_contract(self):
        self.make_bundle("amd64")
        self.make_bundle("arm64")
        self.write_checksums()
        path = self.build()
        checksums = self.dist / "SHA256SUMS"
        lines = checksums.read_text(encoding="utf-8").splitlines()
        checksums.write_text(
            "\n".join(
                ("b" * 64 + line[64:]) if "arm64" in line else line for line in lines
            )
            + "\n",
            encoding="utf-8",
        )

        with self.assertRaisesRegex(release_manifest.ManifestError, "SHA256SUMS"):
            release_manifest.verify_target(path, self.dist, "linux", "amd64")

    def test_verify_target_rejects_unsupported_platform(self):
        self.make_bundle("amd64")
        self.write_checksums()
        path = self.build()

        with self.assertRaisesRegex(release_manifest.ManifestError, "unsupported target platform"):
            release_manifest.verify_target(path, self.dist, "linux", "riscv64")

    def test_rejects_tampered_bundle(self):
        bundle = self.make_bundle()
        self.write_checksums()
        path = self.build()
        bundle.write_bytes(bundle.read_bytes() + b"tamper")

        with self.assertRaisesRegex(release_manifest.ManifestError, "SHA-256 mismatch"):
            release_manifest.verify_manifest(path, self.dist)

    def test_rejects_bundle_metadata_mismatch(self):
        self.make_bundle(metadata_version="v9.9.9")
        self.write_checksums()
        path = self.build()

        with self.assertRaisesRegex(release_manifest.ManifestError, "metadata mismatch"):
            release_manifest.verify_manifest(path, self.dist)

    def test_rejects_unsafe_bundle_path(self):
        self.make_bundle(unsafe=True)
        self.write_checksums()
        path = self.build()

        with self.assertRaisesRegex(release_manifest.ManifestError, "unsafe bundle path"):
            release_manifest.verify_manifest(path, self.dist)

    def test_rejects_bundle_without_verified_update_gate(self):
        self.make_bundle(omit_tool="routegate-update-verified.sh")
        self.write_checksums()
        path = self.build()

        with self.assertRaisesRegex(
            release_manifest.ManifestError, "tools/routegate-update-verified.sh"
        ):
            release_manifest.verify_manifest(path, self.dist)

    def test_rejects_bundle_without_updater_bootstrap(self):
        self.make_bundle(omit_tool="routegate-update-bootstrap.sh")
        self.write_checksums()
        path = self.build()

        with self.assertRaisesRegex(
            release_manifest.ManifestError, "tools/routegate-update-bootstrap.sh"
        ):
            release_manifest.verify_manifest(path, self.dist)


if __name__ == "__main__":
    unittest.main()
