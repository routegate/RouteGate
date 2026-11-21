#!/usr/bin/env python3

from __future__ import annotations

import argparse
import hashlib
import json
import re
import tarfile
from datetime import datetime
from pathlib import Path, PurePosixPath
from typing import Any

FORMAT_VERSION = 1
PRODUCT = "RouteGate"
SUPPORTED_PLATFORMS = {("linux", "amd64"), ("linux", "arm64")}
VERSION_RE = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._+-]*$")
COMMIT_RE = re.compile(r"^[a-f0-9]{40}$")
MIGRATION_RE = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._-]*$")


class ManifestError(RuntimeError):
    pass


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def parse_build_date(value: str) -> None:
    try:
        parsed = datetime.fromisoformat(value.replace("Z", "+00:00"))
    except ValueError as exc:
        raise ManifestError(f"buildDate is not valid ISO-8601: {value}") from exc
    if parsed.tzinfo is None:
        raise ManifestError("buildDate must include a timezone")


def latest_migration(migrations_dir: Path) -> str:
    migrations = sorted(path.name for path in migrations_dir.glob("*.up.sql") if path.is_file())
    if not migrations:
        raise ManifestError(f"no .up.sql migrations found in {migrations_dir}")
    return migrations[-1][: -len(".up.sql")]


def parse_artifact_name(name: str, version: str) -> tuple[str, str]:
    prefix = f"routegate-{version}-"
    suffix = ".tar.gz"
    if not name.startswith(prefix) or not name.endswith(suffix):
        raise ManifestError(f"unexpected release artifact name: {name}")

    platform = name[len(prefix) : -len(suffix)]
    try:
        os_name, arch = platform.split("-", 1)
    except ValueError as exc:
        raise ManifestError(f"artifact platform is malformed: {name}") from exc

    if (os_name, arch) not in SUPPORTED_PLATFORMS:
        raise ManifestError(f"unsupported release platform in {name}: {os_name}/{arch}")
    return os_name, arch


def build_manifest(
    output_dir: Path,
    version: str,
    commit: str,
    build_date: str,
    migrations_dir: Path,
) -> dict[str, Any]:
    if not VERSION_RE.fullmatch(version):
        raise ManifestError(f"invalid version: {version}")
    if not COMMIT_RE.fullmatch(commit):
        raise ManifestError("commit must be a full lowercase Git SHA")
    parse_build_date(build_date)

    artifacts: list[dict[str, Any]] = []
    prefix = f"routegate-{version}-linux-"
    for path in sorted(output_dir.glob(f"{prefix}*.tar.gz")):
        if path.is_symlink() or not path.is_file():
            raise ManifestError(f"artifact must be a regular file: {path}")
        os_name, arch = parse_artifact_name(path.name, version)
        artifacts.append(
            {
                "name": path.name,
                "os": os_name,
                "arch": arch,
                "sha256": sha256_file(path),
                "size": path.stat().st_size,
            }
        )

    if not artifacts:
        raise ManifestError(f"no release artifacts found in {output_dir}")

    return {
        "formatVersion": FORMAT_VERSION,
        "product": PRODUCT,
        "version": version,
        "commit": commit,
        "buildDate": build_date,
        "database": {"expectedMigration": latest_migration(migrations_dir)},
        "artifacts": artifacts,
    }


def write_manifest(path: Path, manifest: dict[str, Any]) -> None:
    encoded = json.dumps(manifest, indent=2, sort_keys=True) + "\n"
    path.write_text(encoded, encoding="utf-8")


def load_manifest(path: Path) -> dict[str, Any]:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise ManifestError(f"cannot read release manifest {path}: {exc}") from exc
    if not isinstance(value, dict):
        raise ManifestError("release manifest root must be an object")
    return value


def parse_checksum_file(path: Path) -> dict[str, str]:
    checksums: dict[str, str] = {}
    try:
        lines = path.read_text(encoding="utf-8").splitlines()
    except OSError as exc:
        raise ManifestError(f"cannot read checksum file {path}: {exc}") from exc

    for line in lines:
        if not line.strip():
            continue
        parts = line.split()
        if len(parts) != 2 or not re.fullmatch(r"[a-f0-9]{64}", parts[0]):
            raise ManifestError(f"malformed SHA256SUMS line: {line}")
        name = parts[1].lstrip("*")
        if name in checksums:
            raise ManifestError(f"duplicate checksum entry: {name}")
        checksums[name] = parts[0]
    return checksums


def normalize_member_name(name: str) -> str:
    if name in {".", "./"}:
        return ""
    while name.startswith("./"):
        name = name[2:]
    return name


def validate_tar_member(member: tarfile.TarInfo) -> str:
    normalized = normalize_member_name(member.name)
    if not normalized:
        return ""
    pure = PurePosixPath(normalized)
    if pure.is_absolute() or ".." in pure.parts:
        raise ManifestError(f"unsafe bundle path: {member.name}")
    if member.issym() or member.islnk() or member.isdev() or member.isfifo():
        raise ManifestError(f"unsupported special entry in release bundle: {member.name}")
    return normalized


def parse_manifest_env(raw: bytes) -> dict[str, str]:
    values: dict[str, str] = {}
    for line in raw.decode("utf-8").splitlines():
        if not line:
            continue
        if "=" not in line:
            raise ManifestError(f"malformed bundle manifest line: {line}")
        key, value = line.split("=", 1)
        if not key or key in values:
            raise ManifestError(f"invalid or duplicate bundle manifest key: {key}")
        values[key] = value
    return values


def verify_bundle(
    path: Path,
    *,
    version: str,
    commit: str,
    build_date: str,
    os_name: str,
    arch: str,
    expected_migration: str,
) -> None:
    required_files = {
        "bin/routegate-manager",
        "bin/routegate-migrate",
        "bin/routegate-agent",
        "frontend/index.html",
        "metadata/manifest.env",
        "tools/release_manifest.py",
        "tools/routegate-update-bootstrap.sh",
        "tools/routegate-update-core.sh",
        "tools/routegate-update-role.sh",
        "tools/routegate-update-transaction.sh",
        "tools/routegate-update-verified.sh",
        f"manager/migrations/{expected_migration}.up.sql",
    }

    try:
        with tarfile.open(path, "r:gz") as archive:
            members: dict[str, tarfile.TarInfo] = {}
            for member in archive.getmembers():
                normalized = validate_tar_member(member)
                if not normalized:
                    continue
                if normalized in members:
                    raise ManifestError(f"duplicate bundle path: {normalized}")
                members[normalized] = member

            missing = required_files - members.keys()
            if missing:
                raise ManifestError(f"{path.name} is missing required entries: {sorted(missing)}")

            for required in required_files:
                if not members[required].isfile():
                    raise ManifestError(f"{path.name} required entry is not a regular file: {required}")

            metadata_member = members["metadata/manifest.env"]
            metadata_file = archive.extractfile(metadata_member)
            if metadata_file is None:
                raise ManifestError(f"{path.name} metadata/manifest.env is not readable")
            metadata = parse_manifest_env(metadata_file.read())
    except (tarfile.TarError, OSError, UnicodeDecodeError) as exc:
        raise ManifestError(f"cannot inspect release bundle {path}: {exc}") from exc

    expected_metadata = {
        "FORMAT_VERSION": "1",
        "VERSION": version,
        "COMMIT": commit,
        "BUILD_DATE": build_date,
        "OS": os_name,
        "ARCH": arch,
    }
    for key, expected in expected_metadata.items():
        if metadata.get(key) != expected:
            raise ManifestError(
                f"{path.name} metadata mismatch for {key}: "
                f"{metadata.get(key)!r} != {expected!r}"
            )


def require_string(obj: dict[str, Any], key: str) -> str:
    value = obj.get(key)
    if not isinstance(value, str) or not value:
        raise ManifestError(f"{key} must be a non-empty string")
    return value


def validate_manifest_contract(
    manifest_path: Path, checksum_path: Path
) -> tuple[str, str, str, str, list[dict[str, Any]]]:
    manifest = load_manifest(manifest_path)

    if manifest.get("formatVersion") != FORMAT_VERSION:
        raise ManifestError(
            f"unsupported formatVersion: {manifest.get('formatVersion')!r}; "
            f"expected {FORMAT_VERSION}"
        )
    if manifest.get("product") != PRODUCT:
        raise ManifestError(f"unexpected product: {manifest.get('product')!r}")

    version = require_string(manifest, "version")
    commit = require_string(manifest, "commit")
    build_date = require_string(manifest, "buildDate")
    if not VERSION_RE.fullmatch(version):
        raise ManifestError(f"invalid version: {version}")
    if not COMMIT_RE.fullmatch(commit):
        raise ManifestError("commit must be a full lowercase Git SHA")
    parse_build_date(build_date)

    database = manifest.get("database")
    if not isinstance(database, dict):
        raise ManifestError("database must be an object")
    expected_migration = require_string(database, "expectedMigration")
    if not MIGRATION_RE.fullmatch(expected_migration):
        raise ManifestError(f"invalid expected migration: {expected_migration}")

    artifacts = manifest.get("artifacts")
    if not isinstance(artifacts, list) or not artifacts:
        raise ManifestError("artifacts must be a non-empty array")

    checksums = parse_checksum_file(checksum_path)
    seen_names: set[str] = set()
    seen_platforms: set[tuple[str, str]] = set()
    normalized_artifacts: list[dict[str, Any]] = []
    manifest_checksums: dict[str, str] = {}

    for artifact in artifacts:
        if not isinstance(artifact, dict):
            raise ManifestError("each artifact must be an object")

        name = require_string(artifact, "name")
        os_name = require_string(artifact, "os")
        arch = require_string(artifact, "arch")
        sha256 = require_string(artifact, "sha256")
        size = artifact.get("size")

        if name in seen_names:
            raise ManifestError(f"duplicate artifact name: {name}")
        seen_names.add(name)

        parsed_platform = parse_artifact_name(name, version)
        if parsed_platform != (os_name, arch):
            raise ManifestError(f"artifact platform fields do not match its name: {name}")
        if parsed_platform in seen_platforms:
            raise ManifestError(f"duplicate artifact platform: {os_name}/{arch}")
        seen_platforms.add(parsed_platform)

        if not re.fullmatch(r"[a-f0-9]{64}", sha256):
            raise ManifestError(f"invalid sha256 for {name}")
        if not isinstance(size, int) or isinstance(size, bool) or size <= 0:
            raise ManifestError(f"invalid size for {name}")
        if checksums.get(name) != sha256:
            raise ManifestError(f"SHA256SUMS does not match manifest for {name}")

        normalized_artifacts.append(
            {
                "name": name,
                "os": os_name,
                "arch": arch,
                "sha256": sha256,
                "size": size,
            }
        )
        manifest_checksums[name] = sha256

    bundle_checksums = {
        name: digest
        for name, digest in checksums.items()
        if name.startswith(f"routegate-{version}-") and name.endswith(".tar.gz")
    }
    if bundle_checksums != manifest_checksums:
        raise ManifestError("release manifest artifact set does not match SHA256SUMS")

    return version, commit, build_date, expected_migration, normalized_artifacts


def verify_artifact_file(
    artifact: dict[str, Any],
    *,
    artifacts_dir: Path,
    version: str,
    commit: str,
    build_date: str,
    expected_migration: str,
) -> None:
    path = artifacts_dir / artifact["name"]
    if path.is_symlink() or not path.is_file():
        raise ManifestError(f"artifact is missing or is not a regular file: {path}")

    actual_sha256 = sha256_file(path)
    if actual_sha256 != artifact["sha256"]:
        raise ManifestError(f"SHA-256 mismatch for {artifact['name']}")
    if path.stat().st_size != artifact["size"]:
        raise ManifestError(f"size mismatch for {artifact['name']}")

    verify_bundle(
        path,
        version=version,
        commit=commit,
        build_date=build_date,
        os_name=artifact["os"],
        arch=artifact["arch"],
        expected_migration=expected_migration,
    )


def verify_manifest(manifest_path: Path, artifacts_dir: Path) -> None:
    version, commit, build_date, expected_migration, artifacts = validate_manifest_contract(
        manifest_path, artifacts_dir / "SHA256SUMS"
    )
    for artifact in artifacts:
        verify_artifact_file(
            artifact,
            artifacts_dir=artifacts_dir,
            version=version,
            commit=commit,
            build_date=build_date,
            expected_migration=expected_migration,
        )


def verify_target(
    manifest_path: Path, artifacts_dir: Path, os_name: str, arch: str
) -> dict[str, Any]:
    if (os_name, arch) not in SUPPORTED_PLATFORMS:
        raise ManifestError(f"unsupported target platform: {os_name}/{arch}")

    version, commit, build_date, expected_migration, artifacts = validate_manifest_contract(
        manifest_path, artifacts_dir / "SHA256SUMS"
    )
    selected = next(
        (artifact for artifact in artifacts if artifact["os"] == os_name and artifact["arch"] == arch),
        None,
    )
    if selected is None:
        raise ManifestError(f"release manifest has no artifact for {os_name}/{arch}")

    verify_artifact_file(
        selected,
        artifacts_dir=artifacts_dir,
        version=version,
        commit=commit,
        build_date=build_date,
        expected_migration=expected_migration,
    )

    return {
        "formatVersion": FORMAT_VERSION,
        "product": PRODUCT,
        "version": version,
        "commit": commit,
        "buildDate": build_date,
        "database": {"expectedMigration": expected_migration},
        "artifact": selected,
    }


def command_build(args: argparse.Namespace) -> None:
    output_dir = Path(args.output_dir).resolve()
    manifest = build_manifest(
        output_dir=output_dir,
        version=args.version,
        commit=args.commit,
        build_date=args.build_date,
        migrations_dir=Path(args.migrations_dir).resolve(),
    )
    output = output_dir / "release-manifest.json"
    write_manifest(output, manifest)
    print(f"wrote {output}")


def command_verify(args: argparse.Namespace) -> None:
    manifest_path = Path(args.manifest).resolve()
    artifacts_dir = Path(args.artifacts_dir).resolve()
    verify_manifest(manifest_path, artifacts_dir)
    print(f"verified {manifest_path}")


def command_verify_target(args: argparse.Namespace) -> None:
    descriptor = verify_target(
        Path(args.manifest).resolve(),
        Path(args.artifacts_dir).resolve(),
        args.os,
        args.arch,
    )
    print(json.dumps(descriptor, sort_keys=True, separators=(",", ":")))


def parser() -> argparse.ArgumentParser:
    root = argparse.ArgumentParser(description="Build and verify RouteGate release manifests")
    commands = root.add_subparsers(dest="command", required=True)

    build = commands.add_parser("build", help="build release-manifest.json from release artifacts")
    build.add_argument("--output-dir", required=True)
    build.add_argument("--version", required=True)
    build.add_argument("--commit", required=True)
    build.add_argument("--build-date", required=True)
    build.add_argument("--migrations-dir", required=True)
    build.set_defaults(func=command_build)

    verify = commands.add_parser("verify", help="verify a release manifest and all local artifacts")
    verify.add_argument("--manifest", required=True)
    verify.add_argument("--artifacts-dir", required=True)
    verify.set_defaults(func=command_verify)

    verify_target_parser = commands.add_parser(
        "verify-target",
        help="verify the full manifest/checksum contract and one local target artifact",
    )
    verify_target_parser.add_argument("--manifest", required=True)
    verify_target_parser.add_argument("--artifacts-dir", required=True)
    verify_target_parser.add_argument("--os", required=True)
    verify_target_parser.add_argument("--arch", required=True)
    verify_target_parser.set_defaults(func=command_verify_target)

    return root


def main() -> int:
    args = parser().parse_args()
    try:
        args.func(args)
    except ManifestError as exc:
        print(f"release manifest error: {exc}")
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())