#!/usr/bin/env bash

# Shared RouteGate host-update primitives.
#
# This file is a library, not a standalone updater. Callers must provide the
# transaction boundary (locking, policy, provenance verification, and rollback
# trap) and must run privileged operations as root.

set -Eeuo pipefail

RG_UPDATE_LOG_PREFIX=${RG_UPDATE_LOG_PREFIX:-[routegate-update]}
RG_UPDATE_ROOT=${RG_UPDATE_ROOT:-}
RG_UPDATE_MANAGER_SERVICE=${RG_UPDATE_MANAGER_SERVICE:-routegate-manager}
RG_UPDATE_AGENT_SERVICE=${RG_UPDATE_AGENT_SERVICE:-routegate-agent}
RG_UPDATE_HEALTH_URL=${RG_UPDATE_HEALTH_URL:-http://127.0.0.1:8080/api/admin/health}
RG_UPDATE_TOOLCHAIN_DIR=/usr/local/lib/routegate/update
RG_UPDATE_ENTRYPOINT=/usr/local/sbin/routegate-update

RG_UPDATE_BUNDLE_VERSION=""
RG_UPDATE_BUNDLE_COMMIT=""
RG_UPDATE_EXPECTED_SCHEMA=""
RG_UPDATE_DB_RESTORE_RC=0

rg_update_log() {
  printf '%s %s\n' "$RG_UPDATE_LOG_PREFIX" "$*"
}

# Production-like entry points hold fd 8 through rollback. A nested
# down-migration receives the descriptor across exec.
rg_update_acquire_production_like_lock() {
  exec 8>>"${RG_UPDATE_LOCK_FILE:-/run/lock/routegate-update.lock}" || return 1
  flock -n 8 || { rg_update_die "host update lock unavailable"; return 1; }
}

rg_update_die() {
  printf '%s ERROR: %s\n' "$RG_UPDATE_LOG_PREFIX" "$*" >&2
  return 1
}

rg_update_path() {
  local absolute=$1
  [[ "$absolute" == /* ]] || {
    rg_update_die "internal path must be absolute: $absolute"
    return 1
  }
  if [[ -n "$RG_UPDATE_ROOT" ]]; then
    printf '%s%s' "${RG_UPDATE_ROOT%/}" "$absolute"
  else
    printf '%s' "$absolute"
  fi
}

rg_update_require_root() {
  [[ ${EUID:-$(id -u)} -eq 0 ]] || rg_update_die "host update operations must run as root"
}

# The caller has stopped services and installed/restored a trusted tree. Never
# restore runtime ownership, even from a legacy backup or an environment override.
rg_update_secure_manager_migrations() {
  local migrations parent unsafe
  migrations=$(rg_update_path /opt/routegate-manager/migrations) || return 1
  parent=$(dirname "$migrations")
  [[ -d "$parent" && ! -L "$parent" && -d "$migrations" && ! -L "$migrations" ]] || return 1
  unsafe=$(find "$migrations" \( ! -type d -a ! -type f \) -print -quit) || return 1
  [[ -z "$unsafe" ]] || return 1
  unsafe=$(find "$migrations" -type f -links +1 -print -quit) || return 1
  [[ -z "$unsafe" ]] || return 1
  chown root:root "$parent" || return 1
  chmod 0755 "$parent" || return 1
  chown -R root:root "$migrations" || return 1
  find "$migrations" -type d -exec chmod 0755 {} + || return 1
  find "$migrations" -type f -exec chmod 0644 {} + || return 1
}

rg_update_require_commands() {
  local command_name
  for command_name in "$@"; do
    command -v "$command_name" >/dev/null 2>&1 || {
      rg_update_die "missing required command: $command_name"
      return 1
    }
  done
}

rg_update_validate_archive() {
  local bundle=$1

  [[ -f "$bundle" && ! -L "$bundle" ]] || {
    rg_update_die "release bundle must be a regular file: $bundle"
    return 1
  }

  if tar -tzf "$bundle" | awk '$0 ~ /^\// || $0 ~ /(^|\/)\.\.(\/|$)/ {found=1} END {exit !found}'; then
    rg_update_die "release bundle contains an unsafe path"
    return 1
  fi

  if tar -tvzf "$bundle" | awk '$1 ~ /^[lhbcp]/ {found=1} END {exit !found}'; then
    rg_update_die "release bundle contains a link or special filesystem entry"
    return 1
  fi
}

rg_update_read_bundle_metadata() {
  local manifest=$1
  local expected_commit=$2
  local expected_os=$3
  local expected_arch=$4
  local key value
  local format_version=""
  local version=""
  local commit=""
  local build_date=""
  local os_name=""
  local arch=""

  [[ -f "$manifest" && ! -L "$manifest" ]] || {
    rg_update_die "release bundle is missing metadata/manifest.env"
    return 1
  }

  while IFS='=' read -r key value || [[ -n "$key" ]]; do
    [[ -n "$key" ]] || continue
    case "$key" in
      FORMAT_VERSION) format_version=$value ;;
      VERSION) version=$value ;;
      COMMIT) commit=$value ;;
      BUILD_DATE) build_date=$value ;;
      OS) os_name=$value ;;
      ARCH) arch=$value ;;
      *)
        rg_update_die "unsupported bundle metadata key: $key"
        return 1
        ;;
    esac
  done <"$manifest"

  [[ "$format_version" == "1" ]] || {
    rg_update_die "unsupported bundle metadata format: ${format_version:-missing}"
    return 1
  }
  [[ "$version" =~ ^[A-Za-z0-9][A-Za-z0-9._+-]*$ ]] || {
    rg_update_die "invalid bundle version metadata"
    return 1
  }
  [[ "$commit" =~ ^[a-f0-9]{40}$ ]] || {
    rg_update_die "invalid bundle commit metadata"
    return 1
  }
  [[ "$build_date" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$ ]] || {
    rg_update_die "invalid bundle build-date metadata"
    return 1
  }
  [[ "$commit" == "$expected_commit" ]] || {
    rg_update_die "bundle commit does not match the requested commit"
    return 1
  }
  [[ "$os_name" == "$expected_os" ]] || {
    rg_update_die "bundle OS does not match the target host"
    return 1
  }
  [[ "$arch" == "$expected_arch" ]] || {
    rg_update_die "bundle architecture does not match the target host"
    return 1
  }

  RG_UPDATE_BUNDLE_VERSION=$version
  RG_UPDATE_BUNDLE_COMMIT=$commit
}

rg_update_expected_schema_from_dir() {
  local migrations_dir=$1
  local expected

  expected=$(
    find "$migrations_dir" -maxdepth 1 -type f -name '*.up.sql' -printf '%f\n' \
      | LC_ALL=C sort \
      | tail -n 1 \
      | sed 's/\.up\.sql$//'
  ) || return 1
  [[ -n "$expected" ]] || {
    rg_update_die "release bundle contains no database migrations"
    return 1
  }
  [[ "$expected" =~ ^[A-Za-z0-9][A-Za-z0-9._-]*$ ]] || {
    rg_update_die "release bundle contains an invalid migration identifier"
    return 1
  }

  RG_UPDATE_EXPECTED_SCHEMA=$expected
  printf '%s\n' "$expected"
}

rg_update_toolchain_files() {
  printf '%s\n' \
    release_manifest.py \
    routegate-update-core.sh \
    routegate-update-role.sh \
    routegate-update-transaction.sh \
    routegate-update-verified.sh \
    routegate-update-dispatch.py
}

rg_update_candidate_toolchain_complete() {
  local work_dir=$1
  local file
  while IFS= read -r file; do
    [[ -f "$work_dir/tools/$file" && ! -L "$work_dir/tools/$file" ]] || {
      rg_update_die "release bundle is missing trusted updater component: tools/$file"
      return 1
    }
  done < <(rg_update_toolchain_files)
}

rg_update_verify_and_extract_bundle() {
  local bundle=$1
  local expected_sha=$2
  local expected_commit=$3
  local expected_os=$4
  local expected_arch=$5
  local work_dir=$6
  local actual_sha

  [[ "$expected_sha" =~ ^[a-f0-9]{64}$ ]] || {
    rg_update_die "expected bundle SHA-256 is invalid"
    return 1
  }
  [[ "$expected_commit" =~ ^[a-f0-9]{40}$ ]] || {
    rg_update_die "expected commit is invalid"
    return 1
  }
  [[ "$expected_os" =~ ^[a-z0-9][a-z0-9._-]*$ ]] || {
    rg_update_die "expected OS is invalid"
    return 1
  }
  [[ "$expected_arch" =~ ^[a-z0-9][a-z0-9._-]*$ ]] || {
    rg_update_die "expected architecture is invalid"
    return 1
  }

  actual_sha=$(sha256sum "$bundle" | awk '{print $1}') || return 1
  [[ "$actual_sha" == "$expected_sha" ]] || {
    rg_update_die "bundle SHA-256 mismatch"
    return 1
  }

  rg_update_validate_archive "$bundle" || return 1
  install -d -m 0700 "$work_dir" || return 1
  tar -xzf "$bundle" -C "$work_dir" --no-same-owner --no-same-permissions || return 1

  for required in \
    bin/routegate-manager \
    bin/routegate-agent \
    frontend/index.html \
    manager/migrations \
    systemd/routegate-manager.service \
    systemd/routegate-agent.service \
    metadata/manifest.env; do
    [[ -e "$work_dir/$required" && ! -L "$work_dir/$required" ]] || {
      rg_update_die "release bundle is missing required entry: $required"
      return 1
    }
  done
  rg_update_candidate_toolchain_complete "$work_dir" || return 1

  rg_update_read_bundle_metadata \
    "$work_dir/metadata/manifest.env" \
    "$expected_commit" \
    "$expected_os" \
    "$expected_arch" || return 1
  rg_update_expected_schema_from_dir "$work_dir/manager/migrations" >/dev/null || return 1
  rg_update_log "verified bundle version=${RG_UPDATE_BUNDLE_VERSION} commit=${RG_UPDATE_BUNDLE_COMMIT} schema=${RG_UPDATE_EXPECTED_SCHEMA}"
}

rg_update_toolchain_state() {
  local tool_dir entrypoint maintenance_dispatcher file path unexpected=0
  tool_dir=$(rg_update_path "$RG_UPDATE_TOOLCHAIN_DIR") || return 1
  entrypoint=$(rg_update_path "$RG_UPDATE_ENTRYPOINT") || return 1

  if [[ ! -e "$tool_dir" && ! -L "$tool_dir" && ! -e "$entrypoint" && ! -L "$entrypoint" ]]; then
    printf 'absent\n'
    return 0
  fi

  [[ -d "$tool_dir" && ! -L "$tool_dir" ]] || {
    rg_update_die "trusted updater directory is partial or unsafe: $tool_dir"
    return 1
  }
  [[ -f "$entrypoint" && ! -L "$entrypoint" && -x "$entrypoint" ]] || {
    rg_update_die "trusted updater entrypoint is partial or unsafe: $entrypoint"
    return 1
  }

  while IFS= read -r file; do
    path="$tool_dir/$file"
    [[ -f "$path" && ! -L "$path" ]] || {
      rg_update_die "trusted updater toolchain is incomplete: $path"
      return 1
    }
  done < <(rg_update_toolchain_files)

  maintenance_dispatcher="$tool_dir/routegate-maintenance-dispatch.py"
  if [[ -e "$maintenance_dispatcher" || -L "$maintenance_dispatcher" ]]; then
    [[ -f "$maintenance_dispatcher" && ! -L "$maintenance_dispatcher" ]] || {
      rg_update_die "trusted updater maintenance dispatcher is unsafe: $maintenance_dispatcher"
      return 1
    }
  fi

  while IFS= read -r path; do
    case "$(basename -- "$path")" in
      release_manifest.py|routegate-update-core.sh|routegate-update-role.sh|routegate-update-transaction.sh|routegate-update-verified.sh|routegate-update-dispatch.py|routegate-maintenance-dispatch.py) ;;
      *) unexpected=1 ;;
    esac
  done < <(find "$tool_dir" -mindepth 1 -maxdepth 1 -print)
  ((unexpected == 0)) || {
    rg_update_die "trusted updater directory contains unexpected entries"
    return 1
  }

  printf 'complete\n'
}

rg_update_create_toolchain_backup() {
  local backup_dir=$1
  local state tool_dir entrypoint backup_tools file
  state=$(rg_update_toolchain_state) || return 1
  tool_dir=$(rg_update_path "$RG_UPDATE_TOOLCHAIN_DIR") || return 1
  entrypoint=$(rg_update_path "$RG_UPDATE_ENTRYPOINT") || return 1

  cat >"$backup_dir/update-toolchain.meta" <<EOF_TOOLCHAIN_META
FORMAT_VERSION=1
STATE=$state
EOF_TOOLCHAIN_META
  chmod 0600 "$backup_dir/update-toolchain.meta" || return 1

  if [[ "$state" == "complete" ]]; then
    backup_tools="$backup_dir/update-toolchain"
    install -d -m 0700 "$backup_tools" || return 1
    while IFS= read -r file; do
      cp -a -- "$tool_dir/$file" "$backup_tools/$file" || return 1
    done < <(rg_update_toolchain_files)
    cp -a -- "$entrypoint" "$backup_dir/routegate-update-entrypoint" || return 1
  fi

  rg_update_log "trusted updater backup state=$state"
}

rg_update_write_entrypoint() {
  local entrypoint
  entrypoint=$(rg_update_path "$RG_UPDATE_ENTRYPOINT") || return 1
  install -d -m 0755 "$(dirname "$entrypoint")" || return 1
  install -m 0755 /dev/null "$entrypoint" || return 1
  cat >"$entrypoint" <<'EOF_ROUTEGATE_UPDATE_ENTRYPOINT'
#!/usr/bin/env bash
set -Eeuo pipefail
exec /usr/local/lib/routegate/update/routegate-update-verified.sh "$@"
EOF_ROUTEGATE_UPDATE_ENTRYPOINT
  chmod 0755 "$entrypoint" || return 1
}

rg_update_apply_toolchain() {
  local work_dir=$1
  local tool_dir file
  rg_update_candidate_toolchain_complete "$work_dir" || return 1
  tool_dir=$(rg_update_path "$RG_UPDATE_TOOLCHAIN_DIR") || return 1

  rm -rf -- "$tool_dir" || return 1
  install -d -m 0755 "$tool_dir" || return 1
  install -m 0755 "$work_dir/tools/release_manifest.py" "$tool_dir/release_manifest.py" || return 1
  install -m 0644 "$work_dir/tools/routegate-update-core.sh" "$tool_dir/routegate-update-core.sh" || return 1
  install -m 0644 "$work_dir/tools/routegate-update-role.sh" "$tool_dir/routegate-update-role.sh" || return 1
  install -m 0755 "$work_dir/tools/routegate-update-transaction.sh" "$tool_dir/routegate-update-transaction.sh" || return 1
  install -m 0755 "$work_dir/tools/routegate-update-verified.sh" "$tool_dir/routegate-update-verified.sh" || return 1
  install -m 0755 "$work_dir/tools/routegate-update-dispatch.py" "$tool_dir/routegate-update-dispatch.py" || return 1
  rg_update_write_entrypoint || return 1
  rg_update_log "trusted updater toolchain promoted"
}

rg_update_validate_toolchain() {
  local tool_dir entrypoint state
  tool_dir=$(rg_update_path "$RG_UPDATE_TOOLCHAIN_DIR") || return 1
  entrypoint=$(rg_update_path "$RG_UPDATE_ENTRYPOINT") || return 1
  state=$(rg_update_toolchain_state) || return 1
  [[ "$state" == "complete" ]] || {
    rg_update_die "trusted updater toolchain is not complete after promotion"
    return 1
  }

  bash -n \
    "$tool_dir/routegate-update-core.sh" \
    "$tool_dir/routegate-update-role.sh" \
    "$tool_dir/routegate-update-transaction.sh" \
    "$tool_dir/routegate-update-verified.sh" || return 1
  python3 "$tool_dir/release_manifest.py" --help >/dev/null || return 1
  python3 -c 'import ast, pathlib, sys; ast.parse(pathlib.Path(sys.argv[1]).read_text())' "$tool_dir/routegate-update-dispatch.py" || return 1
  bash "$tool_dir/routegate-update-transaction.sh" --help >/dev/null || return 1
  bash "$tool_dir/routegate-update-verified.sh" --help >/dev/null || return 1
  grep -Fxq 'exec /usr/local/lib/routegate/update/routegate-update-verified.sh "$@"' "$entrypoint" || {
    rg_update_die "trusted updater entrypoint validation failed"
    return 1
  }
  rg_update_log "trusted updater self-check passed"
}

rg_update_restore_toolchain_backup() {
  local backup_dir=$1
  local meta state tool_dir entrypoint backup_tools file rollback_rc=0
  meta="$backup_dir/update-toolchain.meta"
  [[ -f "$meta" && ! -L "$meta" ]] || {
    rg_update_die "trusted updater backup metadata is missing"
    return 1
  }
  [[ "$(sed -n 's/^FORMAT_VERSION=//p' "$meta" | head -n1)" == "1" ]] || {
    rg_update_die "unsupported trusted updater backup metadata"
    return 1
  }
  state=$(sed -n 's/^STATE=//p' "$meta" | head -n1) || return 1
  [[ "$state" == "absent" || "$state" == "complete" ]] || {
    rg_update_die "invalid trusted updater backup state: ${state:-missing}"
    return 1
  }

  tool_dir=$(rg_update_path "$RG_UPDATE_TOOLCHAIN_DIR") || return 1
  entrypoint=$(rg_update_path "$RG_UPDATE_ENTRYPOINT") || return 1

  if [[ "$state" == "absent" ]]; then
    rm -rf -- "$tool_dir" || rollback_rc=1
    rm -f -- "$entrypoint" || rollback_rc=1
    rg_update_log "trusted updater rollback restored absent state"
    return "$rollback_rc"
  fi

  backup_tools="$backup_dir/update-toolchain"
  [[ -d "$backup_tools" && ! -L "$backup_tools" ]] || {
    rg_update_die "trusted updater backup directory is missing"
    return 1
  }
  [[ -f "$backup_dir/routegate-update-entrypoint" && ! -L "$backup_dir/routegate-update-entrypoint" ]] || {
    rg_update_die "trusted updater entrypoint backup is missing"
    return 1
  }
  while IFS= read -r file; do
    [[ -f "$backup_tools/$file" && ! -L "$backup_tools/$file" ]] || {
      rg_update_die "trusted updater backup is incomplete: $file"
      return 1
    }
  done < <(rg_update_toolchain_files)

  rm -rf -- "$tool_dir" || rollback_rc=1
  install -d -m 0755 "$tool_dir" || rollback_rc=1
  while IFS= read -r file; do
    cp -a -- "$backup_tools/$file" "$tool_dir/$file" || rollback_rc=1
  done < <(rg_update_toolchain_files)
  install -d -m 0755 "$(dirname "$entrypoint")" || rollback_rc=1
  install -m 0755 "$backup_dir/routegate-update-entrypoint" "$entrypoint" || rollback_rc=1

  if ((rollback_rc == 0)); then
    rg_update_validate_toolchain || rollback_rc=1
  fi
  rg_update_log "trusted updater rollback restored complete state"
  return "$rollback_rc"
}

rg_update_require_hybrid_layout() {
  local path
  for path in \
    /usr/local/bin/routegate-manager \
    /usr/local/bin/routegate-agent \
    /opt/routegate-manager/migrations \
    /var/www/routegate \
    /etc/systemd/system/routegate-manager.service \
    /etc/systemd/system/routegate-agent.service \
    /etc/routegate/manager.env; do
    path=$(rg_update_path "$path") || return 1
    [[ -e "$path" && ! -L "$path" ]] || {
      rg_update_die "required RouteGate platform path is missing or unsafe: $path"
      return 1
    }
  done
}

rg_update_control_plane_preflight() {
  local service state
  for service in "$RG_UPDATE_MANAGER_SERVICE" "$RG_UPDATE_AGENT_SERVICE"; do
    state=$(systemctl is-active "$service" 2>/dev/null || true)
    rg_update_log "preflight control-plane ${service}=${state:-unknown}"
    [[ "$state" == "active" ]] || {
      rg_update_die "control-plane service is not active: $service"
      return 1
    }
  done
}

rg_update_create_backup() {
  local backup_dir=$1
  local db_url=$2
  local backup_root
  local manager_bin agent_bin migrations_dir frontend_dir manager_unit agent_unit manager_env

  [[ -n "$db_url" ]] || {
    rg_update_die "database URL is required for backup"
    return 1
  }

  rg_update_require_hybrid_layout || return 1

  backup_root=$(dirname "$backup_dir")
  install -d -m 0700 "$backup_root" || return 1
  [[ ! -e "$backup_dir" ]] || {
    rg_update_die "backup directory already exists: $backup_dir"
    return 1
  }
  install -d -m 0700 "$backup_dir" || return 1

  manager_bin=$(rg_update_path /usr/local/bin/routegate-manager) || return 1
  agent_bin=$(rg_update_path /usr/local/bin/routegate-agent) || return 1
  migrations_dir=$(rg_update_path /opt/routegate-manager/migrations) || return 1
  frontend_dir=$(rg_update_path /var/www/routegate) || return 1
  manager_unit=$(rg_update_path /etc/systemd/system/routegate-manager.service) || return 1
  agent_unit=$(rg_update_path /etc/systemd/system/routegate-agent.service) || return 1
  manager_env=$(rg_update_path /etc/routegate/manager.env) || return 1

  cp -a "$manager_bin" "$backup_dir/routegate-manager" || return 1
  cp -a "$agent_bin" "$backup_dir/routegate-agent" || return 1
  cp -a "$manager_unit" "$backup_dir/routegate-manager.service" || return 1
  cp -a "$agent_unit" "$backup_dir/routegate-agent.service" || return 1
  cp -a "$manager_env" "$backup_dir/manager.env" || return 1
  tar -czf "$backup_dir/manager-migrations.tar.gz" -C "$(dirname "$migrations_dir")" "$(basename "$migrations_dir")" || return 1
  tar -czf "$backup_dir/frontend.tar.gz" -C "$(dirname "$frontend_dir")" "$(basename "$frontend_dir")" || return 1
  pg_dump --format=custom --no-owner --file="$backup_dir/routegate.pgdump" "$db_url" || return 1

  cat >"$backup_dir/backup.meta" <<EOF_BACKUP_META
FORMAT_VERSION=1
CREATED_AT=$(date -u +%Y-%m-%dT%H:%M:%SZ)
EOF_BACKUP_META

  chmod -R go-rwx "$backup_dir" || return 1
  rg_update_log "backup complete: $backup_dir"
}

rg_update_apply_platform_files() {
  local work_dir=$1
  local manager_bin agent_bin migrations_dir frontend_dir manager_unit agent_unit
  local bootstrap_dir bootstrap_backup=""

  manager_bin=$(rg_update_path /usr/local/bin/routegate-manager) || return 1
  agent_bin=$(rg_update_path /usr/local/bin/routegate-agent) || return 1
  migrations_dir=$(rg_update_path /opt/routegate-manager/migrations) || return 1
  frontend_dir=$(rg_update_path /var/www/routegate) || return 1
  manager_unit=$(rg_update_path /etc/systemd/system/routegate-manager.service) || return 1
  agent_unit=$(rg_update_path /etc/systemd/system/routegate-agent.service) || return 1
  bootstrap_dir="$frontend_dir/bootstrap"

  systemctl stop "$RG_UPDATE_AGENT_SERVICE" || return 1
  systemctl stop "$RG_UPDATE_MANAGER_SERVICE" || return 1
  install -m 0755 "$work_dir/bin/routegate-manager" "$manager_bin" || return 1
  install -m 0755 "$work_dir/bin/routegate-agent" "$agent_bin" || return 1

  rm -rf "$migrations_dir" || return 1
  cp -a "$work_dir/manager/migrations" "$migrations_dir" || return 1
  rg_update_secure_manager_migrations || return 1

  install -d -m 0755 "$frontend_dir" || return 1
  if [[ -e "$bootstrap_dir" || -L "$bootstrap_dir" ]]; then
    [[ -d "$bootstrap_dir" && ! -L "$bootstrap_dir" ]] || {
      rg_update_die "bootstrap artifact directory is unsafe: $bootstrap_dir"
      return 1
    }
    bootstrap_backup=$(mktemp -d /tmp/routegate-bootstrap-preserve.XXXXXX) || return 1
    cp -a "$bootstrap_dir" "$bootstrap_backup/bootstrap" || {
      rm -rf -- "$bootstrap_backup"
      return 1
    }
  fi

  find "$frontend_dir" -mindepth 1 -maxdepth 1 ! -name bootstrap -exec rm -rf -- {} + || {
    [[ -z "$bootstrap_backup" ]] || rm -rf -- "$bootstrap_backup"
    return 1
  }
  cp -a "$work_dir/frontend/." "$frontend_dir/" || {
    [[ -z "$bootstrap_backup" ]] || rm -rf -- "$bootstrap_backup"
    return 1
  }

  if [[ -n "$bootstrap_backup" ]]; then
    rm -rf -- "$bootstrap_dir" || {
      rm -rf -- "$bootstrap_backup"
      return 1
    }
    cp -a "$bootstrap_backup/bootstrap" "$bootstrap_dir" || {
      rm -rf -- "$bootstrap_backup"
      return 1
    }
    rm -rf -- "$bootstrap_backup"
  fi

  chown -R root:root "$frontend_dir" || return 1
  find "$frontend_dir" -type d -exec chmod 0755 {} + || return 1
  find "$frontend_dir" -type f -exec chmod 0644 {} + || return 1

  install -m 0644 "$work_dir/systemd/routegate-manager.service" "$manager_unit" || return 1
  install -m 0644 "$work_dir/systemd/routegate-agent.service" "$agent_unit" || return 1
  systemctl daemon-reload || return 1

  rg_update_log "platform files applied; VPN runtimes and bootstrap artifacts were left untouched"
}

rg_update_wait_manager() {
  local attempts=${1:-45}
  local i

  systemctl start "$RG_UPDATE_MANAGER_SERVICE" || return 1
  for ((i = 0; i < attempts; i++)); do
    if curl -fsS "$RG_UPDATE_HEALTH_URL" >/dev/null 2>&1; then
      rg_update_log "manager health check passed"
      return 0
    fi
    sleep 1 || return 1
  done

  rg_update_die "manager did not become healthy within ${attempts}s"
}

rg_update_validate_database_schema() {
  local db_url=$1
  local expected_schema=$2
  local applied_schema

  applied_schema=$(psql "$db_url" -qAtc "SELECT version FROM schema_migrations ORDER BY version DESC LIMIT 1") || return 1
  [[ "$applied_schema" == "$expected_schema" ]] || {
    rg_update_die "database schema mismatch after update: applied=${applied_schema:-missing} expected=$expected_schema"
    return 1
  }
  rg_update_log "database schema=${applied_schema}"
}

rg_update_wait_agent() {
  local attempts=${1:-30}
  local i

  systemctl start "$RG_UPDATE_AGENT_SERVICE" || return 1
  for ((i = 0; i < attempts; i++)); do
    if systemctl is-active --quiet "$RG_UPDATE_AGENT_SERVICE"; then
      rg_update_log "agent service is active"
      return 0
    fi
    sleep 1 || return 1
  done

  rg_update_die "agent did not become active within ${attempts}s"
}

# Optional, fail-closed restore pin. This directory is outside ordinary backups;
# no installer/update path creates or rotates it yet. Its presence is a latch:
# an empty/partial directory must never silently become legacy mode.
rg_update_check_manager_restore_record() {
  local backup_dir=$1 directory path item metadata permissions actual fragment dropins base override
  local -a record=() paths=(
    /etc/routegate/manager.env
    /etc/systemd/system/routegate-manager.service
    /usr/local/bin/routegate-manager
  ) names=(manager.env routegate-manager.service routegate-manager)
  directory=$(rg_update_path /var/lib/routegate-credential-mode) || return 1
  [[ -e "$directory" || -L "$directory" ]] || return 0
  # Check each fixed ancestor without following a symlink. RG_UPDATE_ROOT is a
  # trusted test root, not input accepted from the runtime Manager.
  for item in / /var /var/lib /var/lib/routegate-credential-mode; do
    path=$(rg_update_path "$item") || return 1
    [[ -d "$path" && ! -L "$path" ]] || return 1
    metadata=$(stat -c '%u' -- "$path") || return 1
    permissions=$(stat -c '%A' -- "$path") || return 1
    [[ "$metadata" == 0 && "${permissions:5:1}" != w && "${permissions:8:1}" != w ]] || return 1
  done
  # Publication alone cannot complete a conversion. Any pending entry, even a
  # dangling symlink, withholds recovery before database/file writes.
  path=$directory/conversion.pending
  [[ ! -e "$path" && ! -L "$path" ]] || return 1
  path=$directory/restore.pin
  [[ -f "$path" && ! -L "$path" ]] || return 1
  [[ $(stat -c '%u:%a:%h' -- "$path") == 0:600:1 ]] || return 1
  mapfile -t record < "$path" || return 1
  [[ ${#record[@]} == 5 && ${record[0]} == FORMAT_VERSION=1 && ${record[1]} == MODE=external ]] || return 1
  [[ ${record[2]} =~ ^ENV_SHA256=[a-f0-9]{64}$ &&
     ${record[3]} =~ ^UNIT_SHA256=[a-f0-9]{64}$ &&
     ${record[4]} =~ ^BINARY_SHA256=[a-f0-9]{64}$ ]] || return 1
  # Require the exact LF-terminated byte representation, not a normalized
  # mapfile view (which also accepts a truncated final line).
  cmp -s -- "$path" <(printf '%s\n' "${record[@]}") || return 1
  # The v1 pin does not attest systemd overrides or alternate unit fragments.
  # Refuse them rather than infer compatibility from just the main unit file.
  # Also reject on-disk overrides not yet visible in systemd's loaded graph.
  for base in /etc/systemd/system /run/systemd/system /usr/local/lib/systemd/system \
    /usr/lib/systemd/system /lib/systemd/system /etc/systemd/system.control \
    /run/systemd/system.control /run/systemd/transient /run/systemd/generator \
    /run/systemd/generator.early /run/systemd/generator.late; do
    for override in service.d routegate-.service.d routegate-manager.service.d; do
      path=$(rg_update_path "$base/$override") || return 1
      [[ ! -e "$path" && ! -L "$path" ]] || return 1
    done
  done
  fragment=$(systemctl show --property=FragmentPath --value "$RG_UPDATE_MANAGER_SERVICE") || return 1
  dropins=$(systemctl show --property=DropInPaths --value "$RG_UPDATE_MANAGER_SERVICE") || return 1
  [[ "$fragment" == /etc/systemd/system/routegate-manager.service && -z "$dropins" ]] || return 1
  for ((item=0; item<3; item++)); do
    for path in "$(rg_update_path "${paths[item]}")" "$backup_dir/${names[item]}"; do
      [[ -f "$path" && ! -L "$path" ]] || return 1
      [[ $(stat -c '%h' -- "$path") == 1 ]] || return 1
      actual=$(sha256sum -- "$path") || return 1
      [[ ${actual%% *} == "${record[item+2]#*=}" ]] || return 1
    done
  done
}

# Conservative preflight for installations with any explicit migration-mode
# declaration. Compare bytes rather than interpreting systemd environment syntax.
# This protects the currently installed env/unit, not a durable conversion record.
rg_update_check_manager_restore_environment() {
  local backup_dir=$1 current_env current_unit match_rc=0
  rg_update_check_manager_restore_record "$backup_dir" || {
    rg_update_die "credential-mode restore pin refused rollback; database and file restore withheld; backup retained at $backup_dir"
    return 1
  }
  current_env=$(rg_update_path /etc/routegate/manager.env) || return 1
  current_unit=$(rg_update_path /etc/systemd/system/routegate-manager.service) || return 1
  [[ -e "$current_env" || -L "$current_env" ]] || return 0
  [[ -f "$current_env" && ! -L "$current_env" ]] || return 1
  grep -q 'ROUTEGATE_DATABASE_MIGRATION_MODE' "$current_env" || match_rc=$?
  if ((match_rc == 1)); then
    return 0
  elif ((match_rc != 0)); then
    return 1
  fi
  # Explicit auto, comments and unusual quoting deliberately take the strict path.
  if [[ ! -f "$current_unit" || -L "$current_unit" ]] ||
    ! cmp -s -- "$current_env" "$backup_dir/manager.env" ||
    ! cmp -s -- "$current_unit" "$backup_dir/routegate-manager.service"; then
    rg_update_die "explicit database mode: environment/unit rollback differs; restore withheld; backup retained at $backup_dir"
    return 1
  fi
}

rg_update_restore_backup() {
  local backup_dir=$1
  local db_url=$2
  local restore_database=${3:-0}
  # A caller that restored the database separately must carry its outcome
  # through file recovery. Do not infer it from mutable global helper state.
  local prior_database_restore_rc=${4:-0}
  local manager_bin agent_bin migrations_dir frontend_dir manager_unit agent_unit manager_env
  local migrations_parent frontend_parent
  local restore_rc=0 rollback_rc=0

  if [[ ! "$prior_database_restore_rc" =~ ^(0|[1-9][0-9]{0,2})$ ]] ||
     ((prior_database_restore_rc > 255)) ||
     [[ "$restore_database" != 0 && "$prior_database_restore_rc" != 0 ]]; then
    rg_update_die "invalid prior database restore outcome"
    return 1
  fi

  [[ -d "$backup_dir" && ! -L "$backup_dir" ]] || {
    rg_update_die "backup directory is missing or unsafe: $backup_dir"
    return 1
  }

  for required in \
    routegate-manager \
    routegate-agent \
    routegate-manager.service \
    routegate-agent.service \
    manager.env \
    manager-migrations.tar.gz \
    frontend.tar.gz; do
    [[ -e "$backup_dir/$required" && ! -L "$backup_dir/$required" ]] || {
      rg_update_die "backup is incomplete: $required"
      return 1
    }
  done

  manager_bin=$(rg_update_path /usr/local/bin/routegate-manager) || return 1
  agent_bin=$(rg_update_path /usr/local/bin/routegate-agent) || return 1
  migrations_dir=$(rg_update_path /opt/routegate-manager/migrations) || return 1
  frontend_dir=$(rg_update_path /var/www/routegate) || return 1
  manager_unit=$(rg_update_path /etc/systemd/system/routegate-manager.service) || return 1
  agent_unit=$(rg_update_path /etc/systemd/system/routegate-agent.service) || return 1
  manager_env=$(rg_update_path /etc/routegate/manager.env) || return 1
  migrations_parent=$(dirname "$migrations_dir")
  frontend_parent=$(dirname "$frontend_dir")

  systemctl stop "$RG_UPDATE_AGENT_SERVICE" "$RG_UPDATE_MANAGER_SERVICE" >/dev/null 2>&1 || {
    rg_update_die "service stop failed; restore withheld; backup retained at $backup_dir"
    return 1
  }

  rg_update_check_manager_restore_environment "$backup_dir" || return 1
  RG_UPDATE_DB_RESTORE_RC=$prior_database_restore_rc
  if [[ "$restore_database" == "1" ]]; then
    if [[ -z "$db_url" || ! -s "$backup_dir/routegate.pgdump" ]]; then
      RG_UPDATE_DB_RESTORE_RC=1
      rollback_rc=1
      printf '%s WARNING: database restore requested but database backup is unavailable\n' "$RG_UPDATE_LOG_PREFIX" >&2
    else
      pg_restore \
        --clean \
        --if-exists \
        --no-owner \
        --no-privileges \
        --exit-on-error \
        --dbname="$db_url" \
        "$backup_dir/routegate.pgdump" >/dev/null || restore_rc=$?
      RG_UPDATE_DB_RESTORE_RC=$restore_rc
      if ((RG_UPDATE_DB_RESTORE_RC != 0)); then
        rollback_rc=1
        printf '%s WARNING: database restore failed (exit %d); continuing file/service rollback\n' \
          "$RG_UPDATE_LOG_PREFIX" "$RG_UPDATE_DB_RESTORE_RC" >&2
      fi
    fi
  fi

  install -m 0755 "$backup_dir/routegate-manager" "$manager_bin" || rollback_rc=1
  install -m 0755 "$backup_dir/routegate-agent" "$agent_bin" || rollback_rc=1

  rm -rf "$migrations_dir" || rollback_rc=1
  install -d -m 0755 "$migrations_parent" || rollback_rc=1
  tar -xzf "$backup_dir/manager-migrations.tar.gz" -C "$migrations_parent" || rollback_rc=1
  rg_update_secure_manager_migrations || rollback_rc=1

  rm -rf "$frontend_dir" || rollback_rc=1
  install -d -m 0755 "$frontend_parent" || rollback_rc=1
  tar -xzf "$backup_dir/frontend.tar.gz" -C "$frontend_parent" || rollback_rc=1

  install -m 0644 "$backup_dir/routegate-manager.service" "$manager_unit" || rollback_rc=1
  install -m 0644 "$backup_dir/routegate-agent.service" "$agent_unit" || rollback_rc=1
  install -m 0600 "$backup_dir/manager.env" "$manager_env" || rollback_rc=1

  if ((RG_UPDATE_DB_RESTORE_RC != 0)); then
    rg_update_log "database restore incomplete; service restart withheld; backup retained at $backup_dir"
    return 1
  fi
  if ((rollback_rc != 0)); then
    rg_update_log "file restore incomplete; service restart withheld; backup retained at $backup_dir"
    return 1
  fi
  systemctl daemon-reload || {
    rg_update_log "systemd reload failed; service restart withheld; backup retained at $backup_dir"
    return 1
  }
  if ! rg_update_wait_manager 45; then
    rollback_rc=1
    rg_update_log "rollback readiness manager=failed"
  fi
  if ! rg_update_wait_agent 30; then
    rollback_rc=1
    rg_update_log "rollback readiness agent=failed"
  fi
  if ((rollback_rc == 0)); then
    rg_update_log "rollback readiness control-plane=healthy"
  else
    printf '%s WARNING: rollback restored files/database but control-plane readiness is incomplete\n' \
      "$RG_UPDATE_LOG_PREFIX" >&2
  fi
  rg_update_log "rollback attempt completed; VPN runtimes were left untouched; backup retained at $backup_dir"
  return "$rollback_rc"
}
