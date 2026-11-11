#!/usr/bin/env bash
# Real two-architecture release artifact rehearsal. No tags, release objects,
# publication credentials, privileges, VPN listeners or production hosts.
set -Eeuo pipefail
IFS=$'\n\t'
umask 077

die() { printf '[tagless-candidate] ERROR: %s\n' "$*" >&2; exit 1; }
log() { printf '[tagless-candidate] %s\n' "$*"; }
[[ "$GITHUB_ACTIONS" == true ]] || die "CI-only job"
[[ "$RUNNER_TEMP" == /home/runner/work/_temp* ]] || die "Require a disposable GitHub-hosted runner"
[[ -n "$GITHUB_RUN_ID" && -n "$GITHUB_SHA" && -n "$GITHUB_WORKSPACE" ]] || die "Missing GitHub run metadata"
ROOT=$(cd "$(dirname "$0")/.." && pwd)
[[ "$(realpath "$ROOT")" == "$(realpath "$GITHUB_WORKSPACE")" ]] || die "Unexpected workspace"
[[ "$(git -C "$ROOT" rev-parse HEAD)" == "$GITHUB_SHA" ]] || die "Checkout is not the exact tested commit"
for command in python3 go npm tar sha256sum; do
  command -v "$command" >/dev/null || die "Missing $command"
done

VERSION="ci-candidate-$GITHUB_RUN_ID"
COMMIT="$GITHUB_SHA"
STAGE="$RUNNER_TEMP/routegate-candidate-dry-run-$GITHUB_RUN_ID"
OUTPUT_DIR="$STAGE/dist"
mkdir -p "$STAGE"
log "Building genuine linux/amd64 and linux/arm64 release bundles, no tag or publication"
VERSION="$VERSION" COMMIT="$COMMIT" ARCHITECTURES="amd64 arm64" OUTPUT_DIR="$OUTPUT_DIR" \
  bash "$ROOT/scripts/build-release-bundle.sh"

[[ -s "$OUTPUT_DIR/release-manifest.json" ]] || die "Release manifest missing"
(
  cd "$OUTPUT_DIR"
  sha256sum -c SHA256SUMS
  sha256sum -c INSTALLER_SHA256SUMS
  bash -n install.sh install-agent.sh install-grafana.sh
)
python3 "$ROOT/scripts/release_manifest.py" verify \
  --manifest "$OUTPUT_DIR/release-manifest.json" --artifacts-dir "$OUTPUT_DIR"
for arch in amd64 arm64; do
  python3 "$ROOT/scripts/release_manifest.py" verify-target \
    --manifest "$OUTPUT_DIR/release-manifest.json" \
    --artifacts-dir "$OUTPUT_DIR" --os linux --arch "$arch" >/dev/null
done

# Check the binaries *inside* the two release TARs and their shared frontend.
# ELF e_machine 62 means x86-64; 183 means AArch64.
python3 - "$OUTPUT_DIR" "$VERSION" "$COMMIT" <<'PY'
import hashlib
import json
from pathlib import Path
import struct
import sys
import tarfile

out, version, commit = Path(sys.argv[1]), sys.argv[2], sys.argv[3]
manifest = json.loads((out / "release-manifest.json").read_text())
assert manifest["version"] == version, "manifest version mismatch"
assert manifest["commit"] == commit, "manifest source commit mismatch"
assert {a["arch"] for a in manifest["artifacts"]} == {"amd64", "arm64"}, "missing architecture"

frontends = set()
for arch, machine in (("amd64", 62), ("arm64", 183)):
    bundle = out / f"routegate-{version}-linux-{arch}.tar.gz"
    assert bundle.is_file(), f"missing {arch} release TAR"
    with tarfile.open(bundle, "r:gz") as archive:
        metadata = archive.extractfile("./metadata/manifest.env").read().decode("utf-8")
        values = dict(line.split("=", 1) for line in metadata.splitlines() if "=" in line)
        assert values["VERSION"] == version, "embedded release version mismatch"
        assert values["COMMIT"] == commit, "embedded source commit mismatch"
        assert values["ARCH"] == arch and values["OS"] == "linux", "embedded platform mismatch"
        html = archive.extractfile("./frontend/index.html").read()
        assert b'data-theme="dark"' in html and b'color-scheme: dark' in html, "stale release frontend"
        frontends.add(hashlib.sha256(html).hexdigest())
        for name in ("routegate-manager", "routegate-agent"):
            reader = archive.extractfile(f"./bin/{name}")
            head = reader.read(20)
            assert head[:4] == b"\x7fELF" and head[4:6] == b"\x02\x01", f"{arch}/{name}: not 64-bit LE ELF"
            assert struct.unpack_from("<H", head, 18)[0] == machine, f"{arch}/{name}: wrong machine architecture"
assert len(frontends) == 1, "amd64/arm64 frontend packages differ"
print("[tagless-candidate] Both real TARs: exact version/commit, ELF architecture, identical dark UI — PASS")
PY

# Deliberately corrupt one *copy* of ARM64 and require integrity rejection.
mkdir -p "$STAGE/tampered"
cp "$OUTPUT_DIR"/routegate-"$VERSION"-linux-*.tar.gz \
   "$OUTPUT_DIR/SHA256SUMS" "$OUTPUT_DIR/release-manifest.json" "$STAGE/tampered/"
printf '\0' >> "$STAGE/tampered/routegate-$VERSION-linux-arm64.tar.gz"
if python3 "$ROOT/scripts/release_manifest.py" verify \
  --manifest "$STAGE/tampered/release-manifest.json" \
  --artifacts-dir "$STAGE/tampered" >"$STAGE/tamper-check.log" 2>&1; then
  die "Corrupted arm64 artifact unexpectedly passed integrity verification"
fi
grep -Eq 'SHA-256 mismatch|size mismatch' "$STAGE/tamper-check.log" ||
  die "Corrupt ARM artifact rejected for an unexpected reason"

log "PASS: both real release archives and installer files verified; tampering rejected."
log "No attestation, GitHub Release, tag, public TLS, or ARM runtime execution."
