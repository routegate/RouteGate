#!/usr/bin/env python3
"""Atomic, frontend-only publication to the existing RouteGate nginx document root.

This intentionally never changes Manager binaries, migrations, Agent, VPN
runtimes, active configurations, bootstrap artifacts or service state.
Run only under the existing production-like host-side flock.
"""
from __future__ import annotations

import hashlib
import os
from pathlib import Path, PurePosixPath
import re
import shutil
import sys
import tarfile
import tempfile
import urllib.request

DOCROOT = Path("/var/www/routegate")
PUBLIC_URL = "https://us.routegate.org"
MAX_FILES = 1500
MAX_SIZE = 96 * 1024 * 1024
REVISION = re.compile(r"[0-9a-f]{40}\Z")
DIGEST = re.compile(r"[0-9a-f]{64}\Z")
REF = re.compile(r'(?:src|href)=["\']/(assets/[A-Za-z0-9_./-]+)["\']')


class DeploymentRefused(Exception):
    pass


def sha256(path: Path) -> str:
    h = hashlib.sha256()
    with path.open("rb") as reader:
        for part in iter(lambda: reader.read(1024 * 1024), b""):
            h.update(part)
    return h.hexdigest()


def asset_paths(index: bytes) -> set[str]:
    try:
        text = index.decode("utf-8")
    except UnicodeError as exc:
        raise DeploymentRefused("invalid UTF-8 index") from exc
    files = set(REF.findall(text))
    if not files or not any(s.endswith(".js") for s in files):
        raise DeploymentRefused("index has no immutable JS bundle")
    return files


def read_archive(bundle: Path, expected_revision: str, expected_digest: str) -> dict[str, bytes]:
    if not REVISION.fullmatch(expected_revision) or not DIGEST.fullmatch(expected_digest):
        raise DeploymentRefused("invalid revision or checksum")
    if not bundle.is_file() or bundle.is_symlink() or sha256(bundle) != expected_digest:
        raise DeploymentRefused("archive integrity failure")
    files: dict[str, bytes] = {}
    total = 0
    with tarfile.open(bundle, "r:gz") as tar:
        members = tar.getmembers()
        if len(members) > MAX_FILES:
            raise DeploymentRefused("excessive archive entries")
        for member in members:
            path = PurePosixPath(member.name)
            if path.is_absolute() or ".." in path.parts or not path.parts or path.parts[0] != "dist":
                raise DeploymentRefused("unsafe archive path")
            if member.isdir():
                continue
            if not member.isfile() or member.issym() or member.islnk():
                raise DeploymentRefused("non-regular archive entry")
            relative = PurePosixPath(*path.parts[1:])
            relative_name = str(relative)
            # Only immutable Vite bundles, the SPA entry and our version marker.
            # In particular, never modify bootstrap/, connect.html, nginx or API.
            if not (relative_name in {"index.html", "routegate-ui-version.txt"}
                    or (len(relative.parts) == 2 and relative.parts[0] == "assets"
                        and relative.parts[1] not in {".", ".."})):
                continue
            if relative_name in files:
                raise DeploymentRefused("duplicate archive entry")
            total += member.size
            if total > MAX_SIZE:
                raise DeploymentRefused("archive too large")
            reader = tar.extractfile(member)
            if reader is None:
                raise DeploymentRefused("missing archive member content")
            data = reader.read(member.size + 1)
            if len(data) != member.size:
                raise DeploymentRefused("archive member size mismatch")
            files[relative_name] = data
    if files.get("routegate-ui-version.txt") != (expected_revision + "\n").encode():
        raise DeploymentRefused("frontend revision marker mismatch")
    index = files.get("index.html")
    if not index:
        raise DeploymentRefused("missing index.html")
    if not asset_paths(index).issubset(files):
        raise DeploymentRefused("index references assets absent from release")
    return files


def write_atomic(target: Path, data: bytes) -> None:
    # Callers have checked the parent path and any existing leaf symlinks.
    fd, tmp = tempfile.mkstemp(prefix=".rg-ui-", dir=target.parent)
    try:
        os.fchmod(fd, 0o644)
        with os.fdopen(fd, "wb") as writer:
            writer.write(data)
            writer.flush()
            os.fsync(writer.fileno())
        os.replace(tmp, target)
    finally:
        if os.path.exists(tmp):
            os.unlink(tmp)


def verify_public(revision: str, index: bytes) -> None:
    paths = asset_paths(index)
    request_headers = {"Cache-Control": "no-cache", "Accept-Encoding": "identity"}
    for relative, expected in [
        (f"/?rg-ui={revision}", index),
        (f"/routegate-ui-version.txt?rg-ui={revision}", (revision + "\n").encode()),
    ]:
        request = urllib.request.Request(PUBLIC_URL + relative, headers=request_headers)
        with urllib.request.urlopen(request, timeout=15) as response:
            if response.status != 200 or response.read() != expected:
                raise DeploymentRefused("public frontend verification mismatch")
    # Confirm a hashed JS entry is served, not merely present on disk.
    js = sorted(p for p in paths if p.endswith(".js"))[0]
    request = urllib.request.Request(PUBLIC_URL + "/" + js, method="HEAD", headers=request_headers)
    with urllib.request.urlopen(request, timeout=15) as response:
        if response.status != 200:
            raise DeploymentRefused("new JavaScript bundle not publicly reachable")


def deploy(revision: str, bundle: Path, digest: str, *,
           root: Path = DOCROOT, verifier=verify_public) -> None:
    files = read_archive(bundle, revision, digest)
    if not root.is_dir() or root.is_symlink() or not (root / "index.html").is_file():
        raise DeploymentRefused("unexpected live document root")
    assets = root / "assets"
    if assets.is_symlink() or (assets.exists() and not assets.is_dir()):
        raise DeploymentRefused("invalid static assets directory")
    index_path = root / "index.html"
    marker_path = root / "routegate-ui-version.txt"
    if index_path.is_symlink() or marker_path.is_symlink():
        raise DeploymentRefused("static entry is a symlink")

    previous_index = index_path.read_bytes()
    previous_marker = marker_path.read_bytes() if marker_path.exists() else None
    # Backup is outside the served root. Old hashed bundles remain available
    # for fast rollback and for browsers still using the prior index.html.
    backup = root.parent / ".routegate-ui-backups"
    backup.mkdir(mode=0o700, exist_ok=True)
    os.chmod(backup, 0o700)
    backup_path = backup / (revision + ".index.html.bak")
    if backup_path.exists():
        if backup_path.read_bytes() != previous_index:
            raise DeploymentRefused("a different backup exists for this revision")
    else:
        write_atomic(backup_path, previous_index)
        os.chmod(backup_path, 0o600)

    # Publish immutable assets first. Do not overwrite a hash collision with
    # different bytes. The existing UI remains intact until index replacement.
    assets.mkdir(mode=0o755, exist_ok=True)
    for relative, data in files.items():
        if not relative.startswith("assets/"):
            continue
        target = root / relative
        if target.is_symlink():
            raise DeploymentRefused("symlink in assets directory")
        if target.exists():
            if not target.is_file() or target.read_bytes() != data:
                raise DeploymentRefused("immutable asset collision")
        else:
            write_atomic(target, data)

    changed_index = False
    try:
        write_atomic(marker_path, files["routegate-ui-version.txt"])
        write_atomic(index_path, files["index.html"])
        changed_index = True
        verifier(revision, files["index.html"])
    except Exception:
        # Never remove old or new hashed assets; restoring index suffices.
        if changed_index:
            write_atomic(index_path, previous_index)
        if previous_marker is None:
            marker_path.unlink(missing_ok=True)
        else:
            write_atomic(marker_path, previous_marker)
        raise
    print(f"[routegate-frontend] RESULT=success revision={revision} "
          "scope=index+immutable-assets manager=unchanged vpn=unchanged")


def main() -> int:
    try:
        if os.geteuid() != 0:
            raise DeploymentRefused("root required")
        if len(sys.argv) != 4:
            raise DeploymentRefused("expected revision, archive, sha256")
        deploy(sys.argv[1], Path(sys.argv[2]), sys.argv[3])
        return 0
    except (DeploymentRefused, OSError, tarfile.TarError, ValueError) as exc:
        # Do not print sensitive paths from arbitrary exceptions.
        print(f"[routegate-frontend] RESULT=failed type={type(exc).__name__}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
