#!/usr/bin/env bash

set -Eeuo pipefail
IFS=$'\n\t'
umask 022

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
OUTPUT_DIR="${OUTPUT_DIR:-${ROOT_DIR}/dist}"
VERSION="${VERSION:-}"
COMMIT="${COMMIT:-}"
BUILD_DATE="${BUILD_DATE:-}"
AGENT_INSTALLER_SHA256=""
AGENT_BUNDLE_BASE_URL="${AGENT_BUNDLE_BASE_URL:-}"
ARCHITECTURES="${ARCHITECTURES:-amd64 arm64}"

log() {
  printf '[RouteGate release] %s\n' "$*"
}

die() {
  printf '[RouteGate release] ERROR: %s\n' "$*" >&2
  exit 1
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || die "$1 is required."
}

validate_version() {
  [[ -n "$VERSION" ]] || die "VERSION is required."
  [[ "$VERSION" =~ ^[A-Za-z0-9][A-Za-z0-9._+-]*$ ]] \
    || die "VERSION contains unsupported characters: $VERSION"
}

prepare_metadata() {
  if [[ -z "$COMMIT" ]]; then
    COMMIT=$(git -C "$ROOT_DIR" rev-parse HEAD)
  fi
  [[ "$COMMIT" =~ ^[a-f0-9]{40}$ ]] || die "COMMIT must be a full Git SHA."
  AGENT_INSTALLER_SHA256=$(sha256sum "$ROOT_DIR/install-agent.sh" | awk '{print $1}')
  [[ "$AGENT_INSTALLER_SHA256" =~ ^[a-f0-9]{64}$ ]] || die "Failed to derive install-agent.sh SHA-256."
}

build_frontend() {
  log "Building production frontend."
  (
    cd "$ROOT_DIR/frontend"
    npm ci
    npm run i18n:check
    npm run build
  )
}

build_architecture() {
  local arch=$1
  local stage_dir="$OUTPUT_DIR/stage-${arch}"
  local package="$OUTPUT_DIR/routegate-${VERSION}-linux-${arch}.tar.gz"

  case "$arch" in
    amd64|arm64) ;;
    *) die "Unsupported release architecture: $arch" ;;
  esac

  rm -rf "$stage_dir"
  mkdir -p \
    "$stage_dir/bin" \
    "$stage_dir/manager" \
    "$stage_dir/frontend" \
    "$stage_dir/systemd" \
    "$stage_dir/nginx" \
    "$stage_dir/tools" \
    "$stage_dir/metadata"

  log "Building Manager for linux/${arch}."
  (
    cd "$ROOT_DIR/backend"
    CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build \
      -trimpath \
      -ldflags "-s -w -X github.com/ikaevus/routegate/backend/internal/buildinfo.Version=${VERSION} -X github.com/ikaevus/routegate/backend/internal/buildinfo.GitCommit=${COMMIT} -X github.com/ikaevus/routegate/backend/internal/buildinfo.BuildDate=${BUILD_DATE} -X github.com/ikaevus/routegate/backend/internal/buildinfo.AgentInstallerSHA256=${AGENT_INSTALLER_SHA256} -X github.com/ikaevus/routegate/backend/internal/buildinfo.AgentBundleBaseURL=${AGENT_BUNDLE_BASE_URL}" \
      -o "$stage_dir/bin/routegate-manager" \
      ./cmd/routegate-manager
    CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -trimpath -ldflags "-s -w" \
      -o "$stage_dir/bin/routegate-migrate" ./cmd/routegate-migrate
  )

  log "Building Agent for linux/${arch}."
  (
    cd "$ROOT_DIR/agent"
    CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build \
      -trimpath \
      -ldflags "-s -w -X github.com/ikaevus/routegate/agent/internal/buildinfo.Version=${VERSION} -X github.com/ikaevus/routegate/agent/internal/buildinfo.GitCommit=${COMMIT} -X github.com/ikaevus/routegate/agent/internal/buildinfo.BuildDate=${BUILD_DATE}" \
      -o "$stage_dir/bin/routegate-agent" \
      ./cmd/routegate-agent
  )

  cp -a "$ROOT_DIR/backend/migrations" "$stage_dir/manager/migrations"
  cp -a "$ROOT_DIR/frontend/dist/." "$stage_dir/frontend/"
  cp "$ROOT_DIR/deploy/systemd/routegate-manager.service" "$stage_dir/systemd/"
  cp "$ROOT_DIR/deploy/systemd/routegate-agent.service" "$stage_dir/systemd/"
  cp "$ROOT_DIR/deploy/systemd/hysteria-server.service" "$stage_dir/systemd/"
  cp "$ROOT_DIR/deploy/systemd/routegate-mtproto.service" "$stage_dir/systemd/"
  cp "$ROOT_DIR/deploy/systemd/routegate-update-dispatch.socket" "$stage_dir/systemd/"
  cp "$ROOT_DIR/deploy/systemd/routegate-update-dispatch@.service" "$stage_dir/systemd/"
  cp "$ROOT_DIR/deploy/systemd/routegate-maintenance-dispatch.socket" "$stage_dir/systemd/"
  cp "$ROOT_DIR/deploy/systemd/routegate-maintenance-dispatch@.service" "$stage_dir/systemd/"
  cp "$ROOT_DIR/deploy/nginx/routegate.conf.example" "$stage_dir/nginx/"
  cp "$ROOT_DIR/scripts/routegate-recovery" "$stage_dir/tools/"
  cp "$ROOT_DIR/scripts/release_manifest.py" "$stage_dir/tools/"
  cp "$ROOT_DIR/scripts/routegate-update-bootstrap.sh" "$stage_dir/tools/"
  cp "$ROOT_DIR/scripts/routegate-update-core.sh" "$stage_dir/tools/"
  cp "$ROOT_DIR/scripts/routegate-update-role.sh" "$stage_dir/tools/"
  cp "$ROOT_DIR/scripts/routegate-update-transaction.sh" "$stage_dir/tools/"
  cp "$ROOT_DIR/scripts/routegate-update-verified.sh" "$stage_dir/tools/"
  cp "$ROOT_DIR/scripts/routegate-update-dispatch.py" "$stage_dir/tools/"
  cp "$ROOT_DIR/scripts/routegate-maintenance-dispatch.py" "$stage_dir/tools/"

  cat >"$stage_dir/metadata/manifest.env" <<EOF_MANIFEST
FORMAT_VERSION=1
VERSION=${VERSION}
COMMIT=${COMMIT}
BUILD_DATE=${BUILD_DATE}
OS=linux
ARCH=${arch}
EOF_MANIFEST

  if [[ -f "$ROOT_DIR/LICENSE" ]]; then
    cp "$ROOT_DIR/LICENSE" "$stage_dir/metadata/LICENSE"
  fi
  if [[ -f "$ROOT_DIR/NOTICE" ]]; then
    cp "$ROOT_DIR/NOTICE" "$stage_dir/metadata/NOTICE"
  fi

  chmod 0755 "$stage_dir/bin/routegate-migrate" "$stage_dir/bin/routegate-manager" "$stage_dir/bin/routegate-agent"
  find "$stage_dir" -type d -exec chmod 0755 {} +
  find "$stage_dir" -type f ! -path '*/bin/*' -exec chmod 0644 {} +
  chmod 0755 \
    "$stage_dir/tools/routegate-recovery" \
    "$stage_dir/tools/routegate-update-bootstrap.sh" \
    "$stage_dir/tools/routegate-update-transaction.sh" \
    "$stage_dir/tools/routegate-update-verified.sh" \
    "$stage_dir/tools/routegate-update-dispatch.py" \
    "$stage_dir/tools/routegate-maintenance-dispatch.py"

  tar -C "$stage_dir" \
    --sort=name \
    --owner=0 \
    --group=0 \
    --numeric-owner \
    --mtime="@$SOURCE_DATE_EPOCH" \
    -czf "$package" .

  tar -tzf "$package" >/dev/null
  log "Created $(basename "$package")."
}

main() {
  require_command date
  require_command git
  require_command go
  require_command npm
  require_command python3
  require_command tar
  require_command sha256sum
  validate_version
  prepare_metadata

  if [[ -z "${SOURCE_DATE_EPOCH:-}" ]]; then
    SOURCE_DATE_EPOCH=$(git -C "$ROOT_DIR" show -s --format=%ct "$COMMIT")
    export SOURCE_DATE_EPOCH
  fi
  [[ "$SOURCE_DATE_EPOCH" =~ ^[0-9]+$ ]] || die "SOURCE_DATE_EPOCH must be numeric."

  if [[ -z "$BUILD_DATE" ]]; then
    BUILD_DATE=$(date -u -d "@$SOURCE_DATE_EPOCH" +%Y-%m-%dT%H:%M:%SZ)
  fi

  rm -rf "$OUTPUT_DIR"
  mkdir -p "$OUTPUT_DIR"

  build_frontend

  local arch
  local architectures=()
  IFS=' ' read -r -a architectures <<<"$ARCHITECTURES"
  for arch in "${architectures[@]}"; do
    [[ -n "$arch" ]] || continue
    build_architecture "$arch"
  done

  cp "$ROOT_DIR/install.sh" "$OUTPUT_DIR/install.sh"
  cp "$ROOT_DIR/install-agent.sh" "$OUTPUT_DIR/install-agent.sh"
  cp "$ROOT_DIR/install-grafana.sh" "$OUTPUT_DIR/install-grafana.sh"
  chmod 0644     "$OUTPUT_DIR/install.sh"     "$OUTPUT_DIR/install-agent.sh"     "$OUTPUT_DIR/install-grafana.sh"

  (
    cd "$OUTPUT_DIR"
    sha256sum routegate-*.tar.gz >SHA256SUMS
    sha256sum install.sh install-agent.sh install-grafana.sh >INSTALLER_SHA256SUMS
  )

  python3 "$ROOT_DIR/scripts/release_manifest.py" build \
    --output-dir "$OUTPUT_DIR" \
    --version "$VERSION" \
    --commit "$COMMIT" \
    --build-date "$BUILD_DATE" \
    --migrations-dir "$ROOT_DIR/backend/migrations"

  python3 "$ROOT_DIR/scripts/release_manifest.py" verify \
    --manifest "$OUTPUT_DIR/release-manifest.json" \
    --artifacts-dir "$OUTPUT_DIR"

  rm -rf "$OUTPUT_DIR"/stage-*
  log "Release bundles, installer scripts, checksums, and release-manifest.json are ready in ${OUTPUT_DIR}."
}

main "$@"
