#!/usr/bin/env bash

# Role-aware RouteGate host-update primitives.
# This library layers Management/VPN/Hybrid ownership rules over the shared
# routegate-update-core.sh implementation.

set -Eeuo pipefail

if ! declare -F rg_update_path >/dev/null 2>&1; then
  printf '[routegate-update] ERROR: routegate-update-core.sh must be sourced first\n' >&2
  if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
    exit 1
  fi
  return 1
fi

RG_UPDATE_ROLE_MARKER=${RG_UPDATE_ROLE_MARKER:-/etc/routegate/node-role}

rg_update_validate_role() {
  case "${1:-}" in
    management|vpn|hybrid) return 0 ;;
    *)
      rg_update_die "unsupported RouteGate node role: ${1:-missing}"
      return 1
      ;;
  esac
}

rg_update_role_has_management() {
  [[ "$1" == "management" || "$1" == "hybrid" ]]
}

rg_update_role_has_vpn() {
  [[ "$1" == "vpn" || "$1" == "hybrid" ]]
}

rg_update_all_paths_exist() {
  local absolute path
  for absolute in "$@"; do
    path=$(rg_update_path "$absolute") || return 1
    [[ -e "$path" && ! -L "$path" ]] || return 1
  done
}

rg_update_any_path_exists() {
  local absolute path
  for absolute in "$@"; do
    path=$(rg_update_path "$absolute") || return 1
    if [[ -e "$path" || -L "$path" ]]; then
      return 0
    fi
  done
  return 1
}

rg_update_management_layout_complete() {
  rg_update_all_paths_exist \
    /usr/local/bin/routegate-manager \
    /opt/routegate-manager/migrations \
    /var/www/routegate/index.html \
    /etc/systemd/system/routegate-manager.service \
    /etc/routegate/manager.env
}

rg_update_vpn_layout_complete() {
  rg_update_all_paths_exist \
    /usr/local/bin/routegate-agent \
    /etc/systemd/system/routegate-agent.service \
    /etc/routegate/agent.yaml
}

rg_update_has_any_management_layout() {
  rg_update_any_path_exists \
    /usr/local/bin/routegate-manager \
    /opt/routegate-manager/migrations \
    /var/www/routegate/index.html \
    /etc/systemd/system/routegate-manager.service \
    /etc/routegate/manager.env
}

rg_update_has_any_vpn_layout() {
  rg_update_any_path_exists \
    /usr/local/bin/routegate-agent \
    /etc/systemd/system/routegate-agent.service \
    /etc/routegate/agent.yaml
}

rg_update_marker_role() {
  local marker role line_count
  marker=$(rg_update_path "$RG_UPDATE_ROLE_MARKER") || return 2

  [[ -e "$marker" || -L "$marker" ]] || return 1
  [[ -f "$marker" && ! -L "$marker" ]] || {
    rg_update_die "node role marker must be a regular file: $marker"
    return 2
  }

  role=$(sed -n '1p' "$marker") || return 2
  line_count=$(awk 'END {print NR}' "$marker") || return 2
  [[ "$line_count" == "1" ]] || {
    rg_update_die "node role marker must contain exactly one role"
    return 2
  }
  rg_update_validate_role "$role" || return 2
  printf '%s\n' "$role"
}

rg_update_infer_legacy_role() {
  local management_complete=0 vpn_complete=0 management_any=0 vpn_any=0

  if rg_update_management_layout_complete; then management_complete=1; fi
  if rg_update_vpn_layout_complete; then vpn_complete=1; fi
  if rg_update_has_any_management_layout; then management_any=1; fi
  if rg_update_has_any_vpn_layout; then vpn_any=1; fi

  if [[ "$management_any" == "1" && "$management_complete" != "1" ]]; then
    rg_update_die "cannot infer node role from a partial Management layout"
    return 1
  fi
  if [[ "$vpn_any" == "1" && "$vpn_complete" != "1" ]]; then
    rg_update_die "cannot infer node role from a partial VPN layout"
    return 1
  fi

  case "${management_complete}:${vpn_complete}" in
    1:1) printf 'hybrid\n' ;;
    1:0) printf 'management\n' ;;
    0:1) printf 'vpn\n' ;;
    *)
      rg_update_die "cannot infer RouteGate node role from this host layout"
      return 1
      ;;
  esac
}

rg_update_resolve_role() {
  local requested=${1:-auto}
  local detected=""
  local marker_rc=0

  if [[ "$requested" != "auto" ]]; then
    rg_update_validate_role "$requested" || return 1
  fi

  detected=$(rg_update_marker_role) || marker_rc=$?
  if [[ "$marker_rc" == "2" ]]; then
    return 1
  fi
  if [[ "$marker_rc" == "1" ]]; then
    detected=$(rg_update_infer_legacy_role) || return 1
  fi

  rg_update_validate_role "$detected" || return 1
  if [[ "$requested" != "auto" && "$requested" != "$detected" ]]; then
    rg_update_die "requested node role '$requested' does not match detected role '$detected'"
    return 1
  fi
  printf '%s\n' "$detected"
}

rg_update_role_preflight() {
  local role=$1 service state
  rg_update_validate_role "$role" || return 1

  if rg_update_role_has_management "$role"; then
    rg_update_management_layout_complete || {
      rg_update_die "Management node layout is incomplete"
      return 1
    }
    service=$RG_UPDATE_MANAGER_SERVICE
    state=$(systemctl is-active "$service" 2>/dev/null || true)
    rg_update_log "preflight role=$role service=$service state=${state:-unknown}"
    [[ "$state" == "active" ]] || {
      rg_update_die "Management service is not active: $service"
      return 1
    }
  fi

  if rg_update_role_has_vpn "$role"; then
    rg_update_vpn_layout_complete || {
      rg_update_die "VPN node layout is incomplete"
      return 1
    }
    service=$RG_UPDATE_AGENT_SERVICE
    state=$(systemctl is-active "$service" 2>/dev/null || true)
    rg_update_log "preflight role=$role service=$service state=${state:-unknown}"
    [[ "$state" == "active" ]] || {
      rg_update_die "VPN Agent service is not active: $service"
      return 1
    }
  fi
}

rg_update_read_manager_database_url() {
  local env_file value
  env_file=$(rg_update_path /etc/routegate/manager.env) || return 1
  value=$(rg_update_read_environment_value "$env_file" ROUTEGATE_DATABASE_URL "") || return 1
  [[ -n "$value" ]] || return 1
  printf '%s\n' "$value"
}

# Administrative operations must not reuse a restricted Manager login.
rg_update_read_administrative_database_url() (
  local manager_env mode runtime_url owner_env owner_url binary directory
  manager_env=$(rg_update_path /etc/routegate/manager.env) || return 1
  mode=$(rg_update_read_environment_value "$manager_env" ROUTEGATE_DATABASE_MIGRATION_MODE startup) || return 1
  runtime_url=$(rg_update_read_manager_database_url) || return 1
  case "$mode" in
    startup) printf '%s\n' "$runtime_url"; return 0 ;;
    external) ;;
    *) return 1 ;;
  esac
  owner_env=$(rg_update_path /etc/routegate/migrator.env) || return 1
  [[ -f "$owner_env" && ! -L "$owner_env" ]] || return 1
  [[ $(stat -c '%u:%a' "$owner_env") == 0:600 ]] || return 1
  owner_url=$(rg_update_read_environment_value "$owner_env" ROUTEGATE_MIGRATOR_DATABASE_URL "") || return 1
  [[ -n "$owner_url" ]] || return 1
  binary=$(rg_update_path /usr/local/bin/routegate-migrate) || return 1
  directory=$(rg_update_path /opt/routegate-manager) || return 1
  [[ -f "$binary" && ! -L "$binary" && -x "$binary" ]] || return 1
  cd "$directory" || return 1
  env -i PATH=/usr/bin:/bin ROUTEGATE_DATABASE_URL="$runtime_url" \
    ROUTEGATE_MIGRATOR_DATABASE_URL="$owner_url" "$binary" --check-connections >&2 || return 1
  printf '%s\n' "$owner_url"
)

rg_update_write_role_backup_meta() {
  local backup_dir=$1 role=$2
  cat >"$backup_dir/role.meta" <<EOF_ROLE_META
FORMAT_VERSION=1
ROLE=$role
CREATED_AT=$(date -u +%Y-%m-%dT%H:%M:%SZ)
EOF_ROLE_META
  chmod 0600 "$backup_dir/role.meta" || return 1
}

rg_update_prepare_backup_dir() {
  local backup_dir=$1
  install -d -m 0700 "$(dirname "$backup_dir")" || return 1
  [[ ! -e "$backup_dir" ]] || {
    rg_update_die "backup directory already exists: $backup_dir"
    return 1
  }
  install -d -m 0700 "$backup_dir" || return 1
}

rg_update_create_management_backup() {
  local backup_dir=$1 db_url=$2
  local manager_bin migrations_dir frontend_dir manager_unit manager_env

  rg_update_management_layout_complete || {
    rg_update_die "Management node layout is incomplete"
    return 1
  }
  [[ -n "$db_url" ]] || {
    rg_update_die "database URL is required for Management backup"
    return 1
  }
  rg_update_prepare_backup_dir "$backup_dir" || return 1

  manager_bin=$(rg_update_path /usr/local/bin/routegate-manager) || return 1
  migrations_dir=$(rg_update_path /opt/routegate-manager/migrations) || return 1
  frontend_dir=$(rg_update_path /var/www/routegate) || return 1
  manager_unit=$(rg_update_path /etc/systemd/system/routegate-manager.service) || return 1
  manager_env=$(rg_update_path /etc/routegate/manager.env) || return 1

  cp -a "$manager_bin" "$backup_dir/routegate-manager" || return 1
  rg_update_backup_migrator "$backup_dir" || return 1
  cp -a "$manager_unit" "$backup_dir/routegate-manager.service" || return 1
  cp -a "$manager_env" "$backup_dir/manager.env" || return 1
  tar -czf "$backup_dir/manager-migrations.tar.gz" -C "$(dirname "$migrations_dir")" "$(basename "$migrations_dir")" || return 1
  tar -czf "$backup_dir/frontend.tar.gz" -C "$(dirname "$frontend_dir")" "$(basename "$frontend_dir")" || return 1
  pg_dump --format=custom --no-owner --file="$backup_dir/routegate.pgdump" "$db_url" || return 1
  rg_update_write_role_backup_meta "$backup_dir" management || return 1
  chmod -R go-rwx "$backup_dir" || return 1
  rg_update_log "Management backup complete: $backup_dir"
}

rg_update_create_vpn_backup() {
  local backup_dir=$1 agent_bin agent_unit

  rg_update_vpn_layout_complete || {
    rg_update_die "VPN node layout is incomplete"
    return 1
  }
  rg_update_prepare_backup_dir "$backup_dir" || return 1

  agent_bin=$(rg_update_path /usr/local/bin/routegate-agent) || return 1
  agent_unit=$(rg_update_path /etc/systemd/system/routegate-agent.service) || return 1
  cp -a "$agent_bin" "$backup_dir/routegate-agent" || return 1
  cp -a "$agent_unit" "$backup_dir/routegate-agent.service" || return 1
  rg_update_write_role_backup_meta "$backup_dir" vpn || return 1
  chmod -R go-rwx "$backup_dir" || return 1
  rg_update_log "VPN backup complete: $backup_dir"
}

rg_update_create_role_backup() {
  local role=$1 backup_dir=$2 db_url=${3:-}
  rg_update_validate_role "$role" || return 1
  case "$role" in
    management) rg_update_create_management_backup "$backup_dir" "$db_url" || return 1 ;;
    vpn) rg_update_create_vpn_backup "$backup_dir" || return 1 ;;
    hybrid)
      rg_update_create_backup "$backup_dir" "$db_url" || return 1
      rg_update_write_role_backup_meta "$backup_dir" hybrid || return 1
      ;;
  esac
}

rg_update_apply_management_files() {
  local work_dir=$1 manager_bin migrations_dir frontend_dir manager_unit
  local bootstrap_dir bootstrap_backup=""
  manager_bin=$(rg_update_path /usr/local/bin/routegate-manager) || return 1
  migrations_dir=$(rg_update_path /opt/routegate-manager/migrations) || return 1
  frontend_dir=$(rg_update_path /var/www/routegate) || return 1
  manager_unit=$(rg_update_path /etc/systemd/system/routegate-manager.service) || return 1
  bootstrap_dir="$frontend_dir/bootstrap"

  systemctl stop "$RG_UPDATE_MANAGER_SERVICE" || return 1
  install -m 0755 "$work_dir/bin/routegate-manager" "$manager_bin" || return 1
  install -m 0755 "$work_dir/bin/routegate-migrate" "$(rg_update_path /usr/local/bin/routegate-migrate)" || return 1
  rm -rf "$migrations_dir" || return 1
  cp -a "$work_dir/manager/migrations" "$migrations_dir" || return 1
  rg_update_secure_manager_migrations "$(dirname "$migrations_dir")" || return 1
  install -d -m 0755 "$frontend_dir" || return 1
  # Published Agent bootstrap artifacts live under the frontend root but are
  # not part of the frontend; keep them exactly as rg_update_apply_platform_files does.
  if [[ -e "$bootstrap_dir" || -L "$bootstrap_dir" ]]; then
    [[ -d "$bootstrap_dir" && ! -L "$bootstrap_dir" ]] || {
      rg_update_die "bootstrap artifact directory is unsafe: $bootstrap_dir"
      return 1
    }
    bootstrap_backup=$(mktemp -d "${TMPDIR:-/tmp}/routegate-bootstrap-preserve.XXXXXX") || return 1
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
  systemctl daemon-reload || return 1
  rg_update_log "Management platform files applied; VPN runtimes and bootstrap artifacts were left untouched"
}

rg_update_apply_vpn_files() {
  local work_dir=$1 agent_bin agent_unit
  agent_bin=$(rg_update_path /usr/local/bin/routegate-agent) || return 1
  agent_unit=$(rg_update_path /etc/systemd/system/routegate-agent.service) || return 1

  systemctl stop "$RG_UPDATE_AGENT_SERVICE" || return 1
  install -m 0755 "$work_dir/bin/routegate-agent" "$agent_bin" || return 1
  install -m 0644 "$work_dir/systemd/routegate-agent.service" "$agent_unit" || return 1
  systemctl daemon-reload || return 1
  rg_update_log "VPN Agent files applied; VPN runtimes were left untouched"
}

rg_update_apply_role_files() {
  local role=$1 work_dir=$2
  rg_update_validate_role "$role" || return 1
  case "$role" in
    management) rg_update_apply_management_files "$work_dir" || return 1 ;;
    vpn) rg_update_apply_vpn_files "$work_dir" || return 1 ;;
    hybrid) rg_update_apply_platform_files "$work_dir" || return 1 ;;
  esac
}

rg_update_start_and_validate_role() {
  local role=$1 db_url=${2:-} expected_schema=${3:-}
  rg_update_validate_role "$role" || return 1

  if rg_update_role_has_management "$role"; then
    [[ -n "$db_url" && -n "$expected_schema" ]] || {
      rg_update_die "Management validation requires database URL and expected schema"
      return 1
    }
    rg_update_wait_manager 45 || return 1
    rg_update_validate_database_schema "$db_url" "$expected_schema" || return 1
  fi
  if rg_update_role_has_vpn "$role"; then
    rg_update_wait_agent 30 || return 1
  fi
}

rg_update_require_role_backup() {
  local backup_dir=$1 role=$2 meta stored_role
  meta="$backup_dir/role.meta"
  [[ -f "$meta" && ! -L "$meta" ]] || {
    rg_update_die "role backup metadata is missing"
    return 1
  }
  stored_role=$(sed -n 's/^ROLE=//p' "$meta" | head -n1) || return 1
  [[ "$stored_role" == "$role" ]] || {
    rg_update_die "backup role '$stored_role' does not match transaction role '$role'"
    return 1
  }
}

rg_update_restore_management_backup() {
  local backup_dir=$1 db_url=$2 restore_database=${3:-0}
  local manager_bin migrations_dir frontend_dir manager_unit manager_env
  local migrations_parent frontend_parent restore_rc=0 rollback_rc=0

  rg_update_require_role_backup "$backup_dir" management || return 1
  for required in routegate-manager routegate-manager.service manager.env manager-migrations.tar.gz frontend.tar.gz; do
    [[ -e "$backup_dir/$required" && ! -L "$backup_dir/$required" ]] || {
      rg_update_die "Management backup is incomplete: $required"
      return 1
    }
  done

  manager_bin=$(rg_update_path /usr/local/bin/routegate-manager) || return 1
  migrations_dir=$(rg_update_path /opt/routegate-manager/migrations) || return 1
  frontend_dir=$(rg_update_path /var/www/routegate) || return 1
  manager_unit=$(rg_update_path /etc/systemd/system/routegate-manager.service) || return 1
  manager_env=$(rg_update_path /etc/routegate/manager.env) || return 1
  migrations_parent=$(dirname "$migrations_dir")
  frontend_parent=$(dirname "$frontend_dir")

  systemctl stop "$RG_UPDATE_MANAGER_SERVICE" >/dev/null 2>&1 || {
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
    else
      pg_restore --single-transaction --clean --if-exists --no-owner --no-privileges --exit-on-error \
        --dbname="$db_url" "$backup_dir/routegate.pgdump" >/dev/null || restore_rc=$?
      RG_UPDATE_DB_RESTORE_RC=$restore_rc
    fi
    if ((RG_UPDATE_DB_RESTORE_RC != 0)); then
      rollback_rc=1
      printf '%s WARNING: Management database restore failed (exit %d); continuing file rollback\n' \
        "$RG_UPDATE_LOG_PREFIX" "$RG_UPDATE_DB_RESTORE_RC" >&2
    fi
  fi

  rg_update_restore_migrator "$backup_dir" || {
    rg_update_die "cannot restore matching migrator; services remain stopped"
    return 1
  }
  install -m 0755 "$backup_dir/routegate-manager" "$manager_bin" || rollback_rc=1
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
  install -m 0600 "$backup_dir/manager.env" "$manager_env" || return 1
  rg_update_verify_restored_runtime "$manager_env" || {
    rg_update_die "restored runtime verification failed; Manager remains stopped"
    return 1
  }
  systemctl daemon-reload || rollback_rc=1
  systemctl start "$RG_UPDATE_MANAGER_SERVICE" >/dev/null 2>&1 || rollback_rc=1
  rg_update_log "Management rollback attempt completed; backup retained at $backup_dir"
  return "$rollback_rc"
}

rg_update_restore_vpn_backup() {
  local backup_dir=$1 agent_bin agent_unit rollback_rc=0
  rg_update_require_role_backup "$backup_dir" vpn || return 1
  [[ -f "$backup_dir/routegate-agent" && ! -L "$backup_dir/routegate-agent" ]] || {
    rg_update_die "VPN backup is missing routegate-agent"
    return 1
  }
  [[ -f "$backup_dir/routegate-agent.service" && ! -L "$backup_dir/routegate-agent.service" ]] || {
    rg_update_die "VPN backup is missing routegate-agent.service"
    return 1
  }

  agent_bin=$(rg_update_path /usr/local/bin/routegate-agent) || return 1
  agent_unit=$(rg_update_path /etc/systemd/system/routegate-agent.service) || return 1
  systemctl stop "$RG_UPDATE_AGENT_SERVICE" >/dev/null 2>&1 || true
  install -m 0755 "$backup_dir/routegate-agent" "$agent_bin" || rollback_rc=1
  install -m 0644 "$backup_dir/routegate-agent.service" "$agent_unit" || rollback_rc=1
  systemctl daemon-reload || rollback_rc=1
  systemctl start "$RG_UPDATE_AGENT_SERVICE" >/dev/null 2>&1 || rollback_rc=1
  rg_update_log "VPN rollback attempt completed; VPN runtimes were left untouched; backup retained at $backup_dir"
  return "$rollback_rc"
}

rg_update_restore_role_backup() {
  local role=$1 backup_dir=$2 db_url=${3:-} restore_database=${4:-0}
  rg_update_validate_role "$role" || return 1
  case "$role" in
    management) rg_update_restore_management_backup "$backup_dir" "$db_url" "$restore_database" ;;
    vpn) rg_update_restore_vpn_backup "$backup_dir" ;;
    hybrid)
      rg_update_require_role_backup "$backup_dir" hybrid || return 1
      rg_update_restore_backup "$backup_dir" "$db_url" "$restore_database"
      ;;
  esac
}
