#!/usr/bin/env bash
# Manager-only update of the production-like host, run by the update-manager
# operation of the Production-like Ops workflow. It updates exactly the Manager
# binary, its migrations, the frontend and the Manager unit from a verified
# release bundle, and nothing else: the Agent, VPN runtimes and their configs,
# nginx, observability, maintenance dispatch and bootstrap artifacts are not
# installed, reconfigured, restarted or published, and no apply/render job is
# sent to any node.
#
#   production-like-update-manager.sh <commit> <bundle> <bundle-sha256> \
#     <routegate-update-core.sh> <routegate-update-role.sh> \
#     <deploy-production-like-bundle.sh> <preflight-schema-155.sql> <postflight-schema-158.sql>
#
# It reuses the project's update primitives: bundle verification from
# routegate-update-core.sh, the Management backup/apply/restore of
# routegate-update-role.sh, and the atomic down-migration runner of
# deploy-production-like-bundle.sh with its restore order (down migrations to
# the backup schema, then pg_restore --clean).
#
# Scope: the applied-client-settings rollout, from schema 000155 (upgrade, with
# the read-only preflight gate) or 000158 (same-schema redeploy) to 000158.
#
# Exit codes: 0 updated and verified; 1 failed and rolled back (see log);
# 2 usage or pinned file mismatch; 3 refused before any change; 6 updated and
# verified, but the Agent/VPN state differs from before (not rolled back:
# the update does not touch them, investigate).
set -Eeuo pipefail
umask 077

readonly PREFLIGHT_SQL_SHA256=324095e8671a2cb1bcf2f30e41b9e1658e27718a1817d32df5e1e7bf41182a49
readonly POSTFLIGHT_SQL_SHA256=1aa258232e47c9970b7a4a4de905cb937ff5e604407552e47206ce1a07fe575b
readonly SCHEMA_BEFORE=000155_config_apply_trigger_invariant_repair
readonly SCHEMA_TARGET=000158_explicit_account_protocol_preferences
readonly RO_OPTIONS='-c default_transaction_read_only=on -c statement_timeout=120s -c lock_timeout=5s -c idle_in_transaction_session_timeout=60s -c application_name=routegate-manager-update'

[[ $# -eq 8 ]] || { printf 'usage: %s <commit> <bundle> <bundle-sha256> <core> <role> <down-runner> <preflight-sql> <postflight-sql>\n' "$0" >&2; exit 2; }
EXPECTED_COMMIT=$1 BUNDLE=$2 BUNDLE_SHA=$3 CORE=$4 ROLE_LIB=$5 DOWN_RUNNER=$6 PREFLIGHT_SQL=$7 POSTFLIGHT_SQL=$8
[[ "$EXPECTED_COMMIT" =~ ^[0-9a-f]{40}$ ]] || { printf 'invalid commit\n' >&2; exit 2; }
for file in "$BUNDLE" "$CORE" "$ROLE_LIB" "$DOWN_RUNNER" "$PREFLIGHT_SQL" "$POSTFLIGHT_SQL"; do
  [[ -f "$file" && ! -L "$file" && -r "$file" ]] || { printf 'missing or unsafe input file: %s\n' "$(basename "$file")" >&2; exit 2; }
done

RG_UPDATE_LOG_PREFIX='[routegate-manager-update]'
# shellcheck source=scripts/routegate-update-core.sh
source "$CORE"
# shellcheck source=scripts/routegate-update-role.sh
source "$ROLE_LIB"
rg_update_acquire_production_like_lock

PUBLIC_URL=${ROUTEGATE_PUBLIC_URL_OVERRIDE:-https://us.routegate.org}
WORK_DIR=$(mktemp -d "${TMPDIR:-/tmp}/routegate-manager-update.XXXXXX")
STAGE=initializing
DB_URL=""
CURRENT_SCHEMA=""
BACKUP_DIR=""
MANAGER_STOPPED=0
MUTATED=0
DB_MAY_BE_MUTATED=0

log() { rg_update_log "$*"; }
refuse() { printf '%s REFUSED: %s\n' "$RG_UPDATE_LOG_PREFIX" "$*" >&2; exit 3; }

ro_psql() { # extra psql arguments...
  PGOPTIONS="$RO_OPTIONS" PGCONNECT_TIMEOUT=10 psql "$DB_URL" -X -q -v ON_ERROR_STOP=1 -P pager=off "$@"
}

# The statement of a committed check file that follows `\echo '== <id>.`.
check_block() { # file id
  awk -v id="$2" '
    index($0, "\\echo '"'"'== ") == 1 { if (inside) exit; inside = (index($0, "\\echo '"'"'== " id ".") == 1); next }
    inside && substr($0, 1, 1) != "\\" && $0 !~ /^ROLLBACK;/ { print }
  ' "$1"
}

query_check() { # file id -> rows "a|b|c"
  local sql
  sql=$(check_block "$1" "$2")
  [[ -n "$sql" ]] || { rg_update_die "check $2 not found"; return 1; }
  ro_psql -At -F '|' -c "$sql"
}

run_check_file() { # label file
  log "---- $1 full output begins ----"
  ro_psql -f "$2"
  log "---- $1 full output ends ----"
}

served_account_sql() { # pinned check file, withheld check id
  local withheld_sql
  withheld_sql=$(check_block "$1" "$2")
  [[ -n "$withheld_sql" ]] || return 1
  # One read-only statement/snapshot. Reuse the pinned deployment model rather
  # than treating every active account (including new drafts) as already served.
  printf 'WITH withheld AS (%s)\nSELECT a.server_id::text, a.id::text\nFROM vpn_accounts a JOIN servers s ON s.id = a.server_id\nWHERE a.status = '\''active'\'' AND NOT EXISTS (SELECT 1 FROM withheld w WHERE w.account_id = a.id)\nORDER BY a.server_id::text, a.id::text;\n' "${withheld_sql%;}"
}

served_account_ids() {
  local sql
  sql=$(served_account_sql "$1" "$2")
  ro_psql -At -F '|' -c "$sql"
}

# Unit state and file checksums of everything this update must not touch.
# Only the comparison result is ever printed, never the contents.
readonly WATCHED_UNITS=(routegate-agent sing-box wg-quick@routegate-wg0 hysteria-server routegate-mtproto nginx routegate-maintenance-dispatch.socket)
readonly WATCHED_FILES=(
  /usr/local/bin/routegate-agent /etc/systemd/system/routegate-agent.service /etc/routegate/agent.yaml
  /etc/sing-box/config.json /etc/wireguard/routegate-wg0.conf /etc/hysteria/config.json
  /etc/systemd/system/hysteria-server.service /etc/routegate-mtproto/config.toml /etc/systemd/system/routegate-mtproto.service
  /etc/nginx/sites-available/routegate /usr/local/lib/routegate/update/routegate-maintenance-dispatch.py
  /etc/systemd/system/routegate-maintenance-dispatch.socket /etc/systemd/system/routegate-maintenance-dispatch@.service
)
fingerprint() {
  local unit path resolved bootstrap
  for unit in "${WATCHED_UNITS[@]}"; do
    printf 'unit:%s %s\n' "$unit" \
      "$(systemctl show --property=LoadState,ActiveState,SubState,MainPID,ExecMainStartTimestampMonotonic,NRestarts --value "$unit" 2>/dev/null | tr '\n' ' ')"
  done
  for path in "${WATCHED_FILES[@]}"; do
    resolved=$(rg_update_path "$path")
    if [[ -f "$resolved" ]]; then
      printf 'file:%s %s\n' "$path" "$(sha256sum <"$resolved" | awk '{print $1}')"
    else
      printf 'file:%s absent\n' "$path"
    fi
  done
  bootstrap=$(rg_update_path /var/www/routegate/bootstrap)
  if [[ -d "$bootstrap" ]]; then
    printf 'bootstrap:%s\n' "$(cd "$bootstrap" && find . -type f -exec sha256sum {} + | LC_ALL=C sort | sha256sum | awk '{print $1}')"
  else
    printf 'bootstrap:absent\n'
  fi
}

wait_for_active_agent_jobs() { # same drain as deploy-production-like-bundle.sh
  local deadline=$((SECONDS + 360)) active quiet=0
  while :; do
    active=$(ro_psql -At -c "
      SELECT (SELECT count(*) FROM config_apply_jobs WHERE status IN ('pending', 'in_progress')) +
             (SELECT count(*) FROM agent_operation_jobs WHERE status IN ('pending', 'in_progress'))") || return 1
    [[ "$active" =~ ^[0-9]+$ ]] || { rg_update_die "invalid active Agent job count"; return 1; }
    if ((active == 0)); then
      quiet=$((quiet + 1))
      ((quiet < 2)) || { log "active Agent job drain complete"; return 0; }
    else
      quiet=0
      log "waiting for ${active} active Agent job(s) before stopping Manager"
    fi
    ((SECONDS < deadline)) || { rg_update_die "timed out waiting for active Agent jobs"; return 1; }
    sleep "${ROUTEGATE_UPDATE_DRAIN_POLL_SECONDS:-2}"
  done
}

# Restore order of deploy-production-like-bundle.sh (rollback_database_to_backup):
# undo newer migrations with their atomic down files, then pg_restore --clean.
rollback_database() {
  local migrations_dir version down_file current target_seen=0
  migrations_dir=$(rg_update_path /opt/routegate-manager/migrations) || return 1
  current=$(psql "$DB_URL" -X -v ON_ERROR_STOP=1 -qAtc "SELECT version FROM schema_migrations ORDER BY applied_at DESC, version DESC LIMIT 1") || return 1
  if [[ "$current" != "$CURRENT_SCHEMA" ]]; then
    while IFS= read -r version; do
      [[ -n "$version" ]] || continue
      if [[ "$version" == "$CURRENT_SCHEMA" ]]; then target_seen=1; break; fi
      [[ "$version" =~ ^[A-Za-z0-9][A-Za-z0-9._-]*$ ]] || { rg_update_die "unsafe migration identifier during rollback"; return 1; }
      down_file="$migrations_dir/${version}.down.sql"
      [[ -f "$down_file" && ! -L "$down_file" ]] || { rg_update_die "missing down migration for $version"; return 1; }
      log "database rollback migration=${version}"
      bash "$DOWN_RUNNER" --run-down-migration-inherited "$DB_URL" "$down_file" "$version" || { rg_update_die "down migration failed for $version"; return 1; }
    done < <(psql "$DB_URL" -X -v ON_ERROR_STOP=1 -qAtc "SELECT version FROM schema_migrations ORDER BY applied_at DESC, version DESC")
    ((target_seen == 1)) || { rg_update_die "backup schema is not in the migration history"; return 1; }
  fi
  pg_restore --clean --if-exists --no-owner --no-privileges --exit-on-error \
    --dbname="$DB_URL" "$BACKUP_DIR/routegate.pgdump" >/dev/null || { rg_update_die "pg_restore of the backup failed"; return 1; }
  log "database rollback restored backup schema=${CURRENT_SCHEMA}"
}

manager_healthy() { # attempts
  local i
  for ((i = 0; i < $1; i++)); do
    curl -fsS "$RG_UPDATE_HEALTH_URL" >/dev/null 2>&1 && return 0
    sleep "${ROUTEGATE_UPDATE_HEALTH_POLL_SECONDS:-1}"
  done
  return 1
}

on_error() {
  local rc=$? restore_rc=0 db_restore_rc=0 schema newer migrations_dir
  trap - ERR
  set +e
  log "FAILED at stage=${STAGE} status=${rc}"
  if ((MUTATED == 1)); then
    log "restoring the previous Manager from ${BACKUP_DIR}"
    if ! systemctl stop "$RG_UPDATE_MANAGER_SERVICE" >/dev/null 2>&1; then
      log "ROLLBACK INCOMPLETE: Manager stop failed; restore withheld; backup retained at ${BACKUP_DIR}"
      log "RESULT=failed stage=${STAGE}"
      exit 1
    fi
    if ! rg_update_check_manager_restore_environment "$BACKUP_DIR"; then
      log "ROLLBACK INCOMPLETE: environment/unit preflight failed; database and file restore withheld; backup retained at ${BACKUP_DIR}"
      log "RESULT=failed stage=${STAGE}"
      exit 1
    fi
    if ((DB_MAY_BE_MUTATED == 1)); then
      rollback_database || db_restore_rc=$?
    fi
    # Restores binary, the previous migrations directory (so the old build can
    # never apply 000156+ again), frontend, unit and env, then starts Manager.
    rg_update_restore_management_backup "$BACKUP_DIR" "$DB_URL" 0 "$db_restore_rc" || restore_rc=1
    if ((db_restore_rc != 0)); then
      restore_rc=1
      log "database rollback failed; Manager restart and health confirmation withheld"
    elif manager_healthy 45; then log "rollback manager health=ok"; else log "rollback manager health=FAILED"; restore_rc=1; fi
    schema=$(psql "$DB_URL" -X -qAtc "SELECT version FROM schema_migrations ORDER BY version DESC LIMIT 1" 2>/dev/null)
    newer=$(psql "$DB_URL" -X -qAtc "SELECT count(*) FROM schema_migrations WHERE version > '${CURRENT_SCHEMA}'" 2>/dev/null)
    migrations_dir=$(rg_update_path /opt/routegate-manager/migrations)
    log "rollback schema=${schema:-unknown} newer_migrations=${newer:-unknown} expected=${CURRENT_SCHEMA}"
    [[ "$schema" == "$CURRENT_SCHEMA" && "$newer" == 0 ]] || restore_rc=1
    if find "$migrations_dir" -maxdepth 1 -name '*.up.sql' -newer /dev/null 2>/dev/null \
        | sed 's#.*/##; s#\.up\.sql$##' | awk -v cur="$CURRENT_SCHEMA" '$0 > cur {found=1} END {exit !found}'; then
      log "rollback migrations directory still contains migrations newer than ${CURRENT_SCHEMA}"
      restore_rc=1
    fi
    if ((restore_rc == 0)); then
      log "ROLLBACK COMPLETE: previous Manager and schema restored; backup retained at ${BACKUP_DIR}"
    else
      log "ROLLBACK INCOMPLETE: manual recovery needed; backup retained at ${BACKUP_DIR}"
    fi
  elif ((MANAGER_STOPPED == 1)); then
    systemctl start "$RG_UPDATE_MANAGER_SERVICE" >/dev/null 2>&1
    if manager_healthy 45; then log "Manager restarted unchanged; nothing was modified"; else log "Manager did not come back after the aborted update"; fi
  fi
  log "RESULT=failed stage=${STAGE}"
  exit 1
}
cleanup() { rm -rf -- "$WORK_DIR"; }
trap on_error ERR
trap cleanup EXIT

STAGE=preconditions
rg_update_require_root
rg_update_require_commands awk curl find pg_dump pg_restore psql python3 sha256sum systemctl tar
[[ "$(sha256sum "$PREFLIGHT_SQL" | awk '{print $1}')" == "$PREFLIGHT_SQL_SHA256" ]] || { printf 'preflight SQL does not match the pinned file\n' >&2; exit 2; }
[[ "$(sha256sum "$POSTFLIGHT_SQL" | awk '{print $1}')" == "$POSTFLIGHT_SQL_SHA256" ]] || { printf 'postflight SQL does not match the pinned file\n' >&2; exit 2; }

STAGE=bundle_verification
rg_update_verify_and_extract_bundle "$BUNDLE" "$BUNDLE_SHA" "$EXPECTED_COMMIT" linux amd64 "$WORK_DIR/bundle"
[[ "$RG_UPDATE_EXPECTED_SCHEMA" == "$SCHEMA_TARGET" ]] \
  || refuse "bundle schema ${RG_UPDATE_EXPECTED_SCHEMA} is not ${SCHEMA_TARGET}; this operation covers the applied-client-settings rollout only"
BUNDLE_MANAGER_SHA=$(sha256sum "$WORK_DIR/bundle/bin/routegate-manager" | awk '{print $1}')

STAGE=preflight
rg_update_role_preflight management
DB_URL=$(rg_update_read_manager_database_url)
CURRENT_SCHEMA=$(ro_psql -At -c "SELECT version FROM schema_migrations ORDER BY version DESC LIMIT 1")
case "$CURRENT_SCHEMA" in
  "$SCHEMA_BEFORE") UPGRADE=1 ;;
  "$SCHEMA_TARGET") UPGRADE=0 ;;
  *) refuse "current schema ${CURRENT_SCHEMA:-missing} is neither ${SCHEMA_BEFORE} nor ${SCHEMA_TARGET}" ;;
esac
log "target commit=${EXPECTED_COMMIT} schema current=${CURRENT_SCHEMA} target=${SCHEMA_TARGET}"
if ((UPGRADE == 1)); then
  run_check_file "preflight P0-P7" "$PREFLIGHT_SQL"
  withheld=$(query_check "$PREFLIGHT_SQL" P3 | awk 'NF' | wc -l)
  unexpected=$(query_check "$PREFLIGHT_SQL" P2 | awk -F'|' '$2 != "served" && $2 != "served_compatibility_mode"' | wc -l)
  log "preflight gate withheld_accounts=${withheld} nodes_with_unexpected_state=${unexpected}"
  ((withheld == 0 && unexpected == 0)) || refuse "the preflight predicts accounts losing access or unchecked nodes; nothing was changed"
fi
if ((UPGRADE == 1)); then
  BASELINE=$(served_account_ids "$PREFLIGHT_SQL" P3)
else
  # Unknown active snapshots cannot provide a trustworthy access baseline.
  active_without_snapshot=$(query_check "$POSTFLIGHT_SQL" Q3 | awk -F'|' '$4 == "t"' | wc -l)
  ((active_without_snapshot == 0)) || refuse "an active version has no snapshot; cannot establish served-account baseline"
  BASELINE=$(served_account_ids "$POSTFLIGHT_SQL" Q5)
fi
log "baseline served account identities captured: $(awk 'NF' <<<"$BASELINE" | wc -l)"
fingerprint >"$WORK_DIR/fingerprint.before"
if [[ ! -d "$(rg_update_path "/var/www/routegate/bootstrap/${EXPECTED_COMMIT}")" ]]; then
  log "WARNING: Agent bootstrap artifacts for this commit are not published; new-node connect commands of this Manager will not work until publish-bootstrap publishes them"
fi

STAGE=drain_agent_jobs
wait_for_active_agent_jobs

# Stop Manager before the final database copy so the backup is a consistent
# restore point. The Agent and VPN runtimes keep running.
STAGE=stop_manager
MANAGER_STOPPED=1
systemctl stop "$RG_UPDATE_MANAGER_SERVICE"

STAGE=backup
BACKUP_DIR="$(rg_update_path /root/routegate-backups)/update-management-${EXPECTED_COMMIT}-$(date -u +%Y%m%dT%H%M%SZ)"
rg_update_create_management_backup "$BACKUP_DIR" "$DB_URL"
cat >"$BACKUP_DIR/manager-update.meta" <<EOF_META
FORMAT_VERSION=1
DATABASE_SCHEMA=${CURRENT_SCHEMA}
TARGET_COMMIT=${EXPECTED_COMMIT}
PREVIOUS_MANAGER_SHA256=$(sha256sum "$BACKUP_DIR/routegate-manager" | awk '{print $1}')
CREATED_AT=$(date -u +%Y-%m-%dT%H:%M:%SZ)
EOF_META
chmod 0600 "$BACKUP_DIR/manager-update.meta"

STAGE=apply_manager_files
MUTATED=1
rg_update_apply_management_files "$WORK_DIR/bundle"
installed_sha=$(sha256sum "$(rg_update_path /usr/local/bin/routegate-manager)" | awk '{print $1}')
[[ "$installed_sha" == "$BUNDLE_MANAGER_SHA" ]] || { rg_update_die "installed Manager binary differs from the verified bundle"; false; }
log "installed Manager binary matches bundle commit=${EXPECTED_COMMIT}"

STAGE=manager_start
DB_MAY_BE_MUTATED=1
rg_update_wait_manager 60

STAGE=schema_validation
rg_update_validate_database_schema "$DB_URL" "$SCHEMA_TARGET"

STAGE=postflight
run_check_file "postflight Q0-Q6" "$POSTFLIGHT_SQL"
q0=$(query_check "$POSTFLIGHT_SQL" Q0)
[[ "$q0" == "${SCHEMA_TARGET}|3" ]] || { rg_update_die "postflight Q0: expected ${SCHEMA_TARGET} with three new migrations"; false; }
active_without_snapshot=$(query_check "$POSTFLIGHT_SQL" Q3 | awk -F'|' '$4 == "t"' | wc -l)
inactive_without_snapshot=$(query_check "$POSTFLIGHT_SQL" Q3 | awk -F'|' 'NF && $4 != "t"' | wc -l)
withheld=$(query_check "$POSTFLIGHT_SQL" Q5 | awk 'NF' | wc -l)
states=$(query_check "$POSTFLIGHT_SQL" Q4)
not_ready=$(awk -F'|' 'NF && $2 != "ready"' <<<"$states" | wc -l)
served=$(served_account_ids "$POSTFLIGHT_SQL" Q5)
lost=$(comm -23 <(awk 'NF' <<<"$BASELINE" | LC_ALL=C sort) <(awk 'NF' <<<"$served" | LC_ALL=C sort))
log "postflight gate active_versions_without_snapshot=${active_without_snapshot} inactive_versions_without_snapshot=${inactive_without_snapshot} withheld_accounts=${withheld} node_states_not_ready=${not_ready}"
log "served account identities after update: $(awk 'NF' <<<"$served" | wc -l); lost previously served identities: $(awk 'NF' <<<"$lost" | wc -l)"
((active_without_snapshot == 0)) || { rg_update_die "postflight: an active version has no snapshot"; false; }
if ((UPGRADE == 1)); then
  ((withheld == 0 && not_ready == 0)) || { rg_update_die "postflight: upgrade left accounts without deployed access"; false; }
fi
[[ -z "$lost" ]] || { rg_update_die "postflight: previously served account identities lost access after the update"; false; }

STAGE=public_health
public_status=$(curl -sS -o /dev/null -w '%{http_code}' --max-time 15 "$PUBLIC_URL/" || true)
[[ "$public_status" == 200 ]] || { rg_update_die "public frontend returned HTTP ${public_status:-000}"; false; }
log "public frontend=http_200 manager=active"

STAGE=agent_vpn_invariance
fingerprint >"$WORK_DIR/fingerprint.after"
changed=$(diff <(cat "$WORK_DIR/fingerprint.before") <(cat "$WORK_DIR/fingerprint.after") | awk '/^[<>]/ {print $2}' | cut -d' ' -f1 | LC_ALL=C sort -u | tr '\n' ' ' || true)

STAGE=complete
trap - ERR
log "backup retained at ${BACKUP_DIR}"
if [[ -n "$changed" ]]; then
  log "Agent/VPN/unrelated state changed during the update: ${changed}"
  log "RESULT=updated-but-agent-vpn-state-changed commit=${EXPECTED_COMMIT}"
  exit 6
fi
log "Agent, VPN runtimes, nginx, maintenance dispatch and bootstrap artifacts: unchanged"
log "RESULT=updated commit=${EXPECTED_COMMIT} schema=${SCHEMA_TARGET}"
