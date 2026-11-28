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
RG_UPDATE_MANAGER_OWNER=${RG_UPDATE_MANAGER_OWNER:-routegate:routegate}
RG_UPDATE_TOOLCHAIN_DIR=/usr/local/lib/routegate/update
RG_UPDATE_ENTRYPOINT=/usr/local/sbin/routegate-update

RG_UPDATE_BUNDLE_VERSION=""
RG_UPDATE_BUNDLE_COMMIT=""
RG_UPDATE_EXPECTED_SCHEMA=""
RG_UPDATE_DB_RESTORE_RC=0

rg_update_log() {
  printf '%s %s\n' "$RG_UPDATE_LOG_PREFIX" "$*"
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

# Shared by installation, update and restore. Call only for a trusted tree
# while Manager is stopped. Legacy archives and environment overrides must
# never return migration ownership to the service identity.
rg_update_secure_manager_migrations() {
  local parent=$1 migrations unsafe
  migrations="$parent/migrations"
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
    bin/routegate-migrate \
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

# Read environment entries as data, never as shell code.
rg_update_read_environment_value() {
  local file=$1 key=$2 value=$3 line name found=0
  [[ -f "$file" && ! -L "$file" ]] || return 1
  while IFS= read -r line || [[ -n "$line" ]]; do
    # EnvironmentFile permits whitespace around the assignment name. Do not
    # mistake an external-mode entry for an absent key and skip verification.
    [[ "$line" == *=* ]] || continue
    name=${line%%=*}
    name="${name#"${name%%[!$' \t']*}"}"
    name="${name%"${name##*[!$' \t']}"}"
    if [[ "$name" == "$key" ]]; then
      ((found == 0)) || return 1
      found=1
      value=${line#*=}
      if [[ "$value" == \"*\" || "$value" == \'*\' ]]; then
        value=${value:1:${#value}-2}
      fi
    fi
  done <"$file"
  [[ "$value" != *$'\r'* && "$value" != *$'\n'* ]] || return 1
  printf '%s\n' "$value"
}

rg_update_verify_restored_runtime() (
  local file=$1 mode url binary directory
  mode=$(rg_update_read_environment_value "$file" ROUTEGATE_DATABASE_MIGRATION_MODE startup) || return 1
  case "$mode" in
    startup) return 0 ;;
    external) ;;
    *) return 1 ;;
  esac
  url=$(rg_update_read_environment_value "$file" ROUTEGATE_DATABASE_URL "") || return 1
  [[ -n "$url" ]] || return 1
  binary=$(rg_update_path /usr/local/bin/routegate-migrate) || return 1
  directory=$(rg_update_path /opt/routegate-manager) || return 1
  [[ -f "$binary" && ! -L "$binary" && -x "$binary" ]] || return 1
  cd "$directory" || return 1
  # Do not inherit migration-owner or ambient PostgreSQL credentials.
  env -i PATH=/usr/bin:/bin ROUTEGATE_DATABASE_URL="$url" "$binary" --check-runtime
)

# Capture absence explicitly so restoring a legacy installation removes the new
# migrator. A declared-present file must never silently become an absent file.
rg_update_backup_migrator() {
  local backup_dir=$1 binary
  binary=$(rg_update_path /usr/local/bin/routegate-migrate) || return 1
  if [[ -e "$binary" || -L "$binary" ]]; then
    [[ -f "$binary" && ! -L "$binary" && -s "$binary" ]] || return 1
    cp -p "$binary" "$backup_dir/routegate-migrate" || return 1
    printf 'present\n' >"$backup_dir/migrator.state" || return 1
  else
    printf 'absent\n' >"$backup_dir/migrator.state" || return 1
  fi
}

rg_update_validate_migrator_backup() {
  local backup_dir=$1 state=absent
  if [[ -e "$backup_dir/migrator.state" || -L "$backup_dir/migrator.state" ]]; then
    [[ -f "$backup_dir/migrator.state" && ! -L "$backup_dir/migrator.state" ]] || return 1
    state=$(cat "$backup_dir/migrator.state") || return 1
  fi
  case "$state" in
    present) [[ -f "$backup_dir/routegate-migrate" && ! -L "$backup_dir/routegate-migrate" && -s "$backup_dir/routegate-migrate" ]] ;;
    absent) [[ ! -e "$backup_dir/routegate-migrate" && ! -L "$backup_dir/routegate-migrate" ]] ;;
    *) return 1 ;;
  esac
}

rg_update_restore_migrator() {
  local backup_dir=$1 binary
  rg_update_validate_migrator_backup "$backup_dir" || return 1
  binary=$(rg_update_path /usr/local/bin/routegate-migrate) || return 1
  [[ ! -L "$binary" ]] || return 1
  if [[ -f "$backup_dir/routegate-migrate" ]]; then
    install -m 0755 "$backup_dir/routegate-migrate" "$binary" || return 1
  else
    # Old backups predate this binary; do not leave a newer migration executable.
    rm -f -- "$binary" || return 1
  fi
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
  rg_update_backup_migrator "$backup_dir" || return 1
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
  install -m 0755 "$work_dir/bin/routegate-migrate" "$(rg_update_path /usr/local/bin/routegate-migrate)" || return 1
  install -m 0755 "$work_dir/bin/routegate-agent" "$agent_bin" || return 1

  rm -rf "$migrations_dir" || return 1
  cp -a "$work_dir/manager/migrations" "$migrations_dir" || return 1
  rg_update_secure_manager_migrations "$(dirname "$migrations_dir")" || return 1

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

# Restore the security boundary recorded in the same backup as Manager's mode.
rg_update_restore_database() {
  local backup_dir=$1 db_url=$2 mode
  local -a options=(--single-transaction --clean --if-exists)
  mode=$(rg_update_read_environment_value "$backup_dir/manager.env" ROUTEGATE_DATABASE_MIGRATION_MODE startup) || return 1
  case "$mode" in
    startup) options+=(--no-owner --no-privileges) ;;
    external) ;; # Preserve recorded owners and ACLs; never grant blanket DML.
    *) rg_update_die "unknown migration mode in database backup"; return 1 ;;
  esac
  pg_restore "${options[@]}" --exit-on-error --dbname="$db_url" "$backup_dir/routegate.pgdump"
}

rg_update_restore_backup() {
  local backup_dir=$1
  local db_url=$2
  local restore_database=${3:-0}
  local manager_bin agent_bin migrations_dir frontend_dir manager_unit agent_unit manager_env
  local migrations_parent frontend_parent
  local restore_rc=0 rollback_rc=0

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
    rg_update_die "cannot stop services before restoring privileged inputs"
    return 1
  }

  rg_update_validate_migrator_backup "$backup_dir" || {
    rg_update_die "migrator backup is incomplete or unsafe"
    return 1
  }
  RG_UPDATE_DB_RESTORE_RC=0
  if [[ "$restore_database" == "1" ]]; then
    if [[ -z "$db_url" || ! -s "$backup_dir/routegate.pgdump" ]]; then
      RG_UPDATE_DB_RESTORE_RC=1
      rollback_rc=1
      printf '%s WARNING: database restore requested but database backup is unavailable\n' "$RG_UPDATE_LOG_PREFIX" >&2
    else
      # Keep the pre-restore database intact if any restore command fails.
      rg_update_restore_database "$backup_dir" "$db_url" >/dev/null || restore_rc=$?
      RG_UPDATE_DB_RESTORE_RC=$restore_rc
      if ((RG_UPDATE_DB_RESTORE_RC != 0)); then
        rollback_rc=1
        printf '%s WARNING: database restore failed (exit %d); continuing file/service rollback\n' \
          "$RG_UPDATE_LOG_PREFIX" "$RG_UPDATE_DB_RESTORE_RC" >&2
      fi
    fi
  fi

  rg_update_restore_migrator "$backup_dir" || {
    rg_update_die "cannot restore matching migrator; services remain stopped"
    return 1
  }
  install -m 0755 "$backup_dir/routegate-manager" "$manager_bin" || rollback_rc=1
  install -m 0755 "$backup_dir/routegate-agent" "$agent_bin" || rollback_rc=1

  rm -rf "$migrations_dir" || rollback_rc=1
  install -d -m 0755 "$migrations_parent" || rollback_rc=1
  tar -xzf "$backup_dir/manager-migrations.tar.gz" -C "$migrations_parent" || rollback_rc=1
  rg_update_secure_manager_migrations "$migrations_parent" || {
    rg_update_die "cannot protect restored Manager migrations; services remain stopped"
    return 1
  }

  rm -rf "$frontend_dir" || rollback_rc=1
  install -d -m 0755 "$frontend_parent" || rollback_rc=1
  tar -xzf "$backup_dir/frontend.tar.gz" -C "$frontend_parent" || rollback_rc=1

  install -m 0644 "$backup_dir/routegate-manager.service" "$manager_unit" || rollback_rc=1
  install -m 0644 "$backup_dir/routegate-agent.service" "$agent_unit" || rollback_rc=1
  install -m 0600 "$backup_dir/manager.env" "$manager_env" || return 1
  rg_update_verify_restored_runtime "$manager_env" || {
    rg_update_die "restored runtime verification failed; Manager remains stopped"
    return 1
  }

  systemctl daemon-reload || rollback_rc=1
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
