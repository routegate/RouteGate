#!/usr/bin/env bash
# Tests for the update-manager ops operation, with stubs only (no PostgreSQL,
# SSH, systemd or GitHub access). A fake host root (RG_UPDATE_ROOT) carries the
# Manager, Agent and VPN files; stub systemctl/psql/pg_dump/pg_restore/curl keep
# a small state (schema history, Manager running) so the runner's real control
# flow is exercised:
#   * the host runner: pinned SQL and bundle checks, preflight gate, backup,
#     Manager-only apply, postflight gate, rollback order (down migrations,
#     pg_restore, previous Manager files) on start/postflight failures, Agent and
#     VPN invariance, exit codes, no secret in the output;
#   * the workflow: strict request mapping (commit only for update-manager),
#     main-only dispatch, remote copy/run/cleanup, redaction, full output for the
#     artifact, sanitized log and #268 comment;
#   * the deploy workflow no longer runs after CI (manual dispatch only).
# Run as root (the runner requires it): sudo bash scripts/test-production-like-update-manager.sh
# Real migrations 000155 -> 000158 and the real rollback are rehearsed against
# PostgreSQL separately (docs/operations/applied-client-settings/MANAGER-ONLY-UPDATE.md).
#
# Checks pass their values to `bash -c '...' _ "$value"` as positional
# parameters, so the single-quoted scripts must not expand.
# shellcheck disable=SC2016
set -Eeuo pipefail

repo=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
runner="$repo/scripts/production-like-update-manager.sh"
core="$repo/scripts/routegate-update-core.sh"
role="$repo/scripts/routegate-update-role.sh"
pre_sql="$repo/docs/operations/applied-client-settings/preflight-schema-155.sql"
post_sql="$repo/docs/operations/applied-client-settings/postflight-schema-158.sql"
workflow="$repo/.github/workflows/production-like-ops.yml"
deploy_workflow="$repo/.github/workflows/production-like-deploy.yml"
commit=0123456789abcdef0123456789abcdef01234567
work=$(mktemp -d)
export RG_UPDATE_LOCK_FILE="$work/update.lock"
trap 'rm -rf "$work"' EXIT
failures=0

pass() { printf 'ok   %s\n' "$*"; }
fail() { printf 'FAIL %s\n' "$*" >&2; failures=$((failures + 1)); }
check() { local name=$1; shift; if "$@"; then pass "$name"; else fail "$name"; fi; }

[[ ${EUID:-$(id -u)} -eq 0 ]] || { echo "run as root: sudo bash $0" >&2; exit 1; }

# The statements the runner extracts from the pinned SQL files (same rule).
blocks="$work/blocks"
mkdir -p "$blocks"
for spec in "$pre_sql:P2" "$pre_sql:P3" "$post_sql:Q0" "$post_sql:Q3" "$post_sql:Q4" "$post_sql:Q5"; do
  awk -v id="${spec##*:}" '
    index($0, "\\echo '"'"'== ") == 1 { if (inside) exit; inside = (index($0, "\\echo '"'"'== " id ".") == 1); next }
    inside && substr($0, 1, 1) != "\\" && $0 !~ /^ROLLBACK;/ { print }
  ' "${spec%:*}" >"$blocks/${spec##*:}.sql"
  [[ -s "$blocks/${spec##*:}.sql" ]] || { echo "check ${spec##*:} not found" >&2; exit 1; }
done

# --- stubs ---------------------------------------------------------------
stubs="$work/bin"
mkdir -p "$stubs"
cat >"$stubs/psql" <<'EOF'
#!/usr/bin/env bash
# Stub psql backed by $S/history (applied migrations, newest first).
S=$STUB_STATE
printf 'PGOPTIONS=%s ARGS=%s\n' "${PGOPTIONS:-}" "${*:2}" >>"$S/psql.log"
sql="" file=""
args=("$@")
for ((i = 1; i < ${#args[@]}; i++)); do
  case "${args[$i]}" in
    -v|-P|-F) i=$((i + 1)) ;;
    -f) i=$((i + 1)); file=${args[$i]} ;;
    -c) i=$((i + 1)); sql=${args[$i]} ;;
    -*c) i=$((i + 1)); sql=${args[$i]} ;;
  esac
done
if [[ -n "$file" ]]; then
  if grep -q '== P0\.' "$file"; then sections="P0 P1 P2 P3 P4 P5 P6 P7"; kind=pre; else sections="Q0 Q1 Q2 Q3 Q4 Q5 Q6"; kind=post; fi
  for s in $sections; do
    echo "== ${s}. section"
    echo " us-node | 11111111-2222-4333-8444-555555555555 | row"
  done
  if [[ "${STUB_FILE_FAIL:-}" == "$kind" ]]; then
    echo 'psql: error: connection to server at "db.internal" (10.1.2.3), port 5432 failed: FATAL:  password authentication failed for user "rgadmin"' >&2
    exit 2
  fi
  exit 0
fi
if [[ "$sql" == 'WITH withheld AS ('* ]]; then
  phase=before
  [[ "$(cat "$RG_UPDATE_ROOT/usr/local/bin/routegate-manager")" != manager-new ]] || phase=after
  check=P3
  [[ "$sql" != *'not_deployed_by_active_version'* ]] || check=Q5
  awk -F'|' 'FILENAME == ARGV[1] { if (NF > 1) withheld[$2] = 1; next } !($2 in withheld)' \
    <(cat "$S/answer.$check" 2>/dev/null || true) "$S/served.$phase"
  exit 0
fi
for id in P2 P3 Q0 Q3 Q4 Q5; do
  if [[ "$sql" == "$(cat "$STUB_BLOCKS/$id.sql")" ]]; then
    if [[ -f "$S/answer.$id" ]]; then cat "$S/answer.$id"; fi
    [[ "$id" != Q0 ]] || [[ -f "$S/answer.Q0" ]] || printf '%s|%s\n' "$(head -n1 "$S/history")" "$(grep -c '^00015[678]_' "$S/history")"
    exit 0
  fi
done
case "$sql" in
  *"version > '"*)
    v=${sql#*version > \'}; v=${v%%\'*}
    awk -v v="$v" '$0 > v' "$S/history" | wc -l ;;
  *"ORDER BY applied_at DESC, version DESC LIMIT 1"*|*"ORDER BY version DESC LIMIT 1"*) head -n1 "$S/history" ;;
  *"ORDER BY applied_at DESC, version DESC"*) cat "$S/history" ;;
  *config_apply_jobs*) echo 0 ;;
  *"GROUP BY s.name"*) cat "$S/baseline" ;;
  *) echo "unexpected query: $sql" >&2; exit 3 ;;
esac
EOF
cat >"$stubs/pg_dump" <<'EOF'
#!/usr/bin/env bash
for arg in "$@"; do
  case "$arg" in --file=*) cp "$STUB_STATE/history" "${arg#--file=}"; exit 0 ;; esac
done
exit 2
EOF
cat >"$stubs/pg_restore" <<'EOF'
#!/usr/bin/env bash
echo "pg_restore $*" | sed 's#--dbname=[^ ]*#--dbname=<db>#' >>"$STUB_STATE/restore.log"
[[ "${STUB_RESTORE_FAIL:-0}" != 1 ]] || exit 1
cp "${*: -1}" "$STUB_STATE/history"
EOF
cat >"$stubs/systemctl" <<'EOF'
#!/usr/bin/env bash
# Stub systemctl: records every call; "starting" the Manager runs the stubbed
# migrations of the installed binary.
S=$STUB_STATE
echo "$*" >>"$S/systemctl.log"
root=$RG_UPDATE_ROOT
case "$1" in
  is-active) echo active ;;
  show) unit=${*: -1}; if [[ -f "$S/unit.$unit" ]]; then cat "$S/unit.$unit"; else printf 'loaded\nactive\nrunning\n100\n5\n0\n'; fi ;;
  stop) [[ "$2" != routegate-manager ]] || rm -f "$S/running" ;;
  start)
    [[ "$2" == routegate-manager ]] || exit 0
    if [[ "$(cat "$root/usr/local/bin/routegate-manager")" == manager-new ]]; then
      [[ "${STUB_AGENT_CHANGE:-0}" != 1 ]] || printf 'loaded\nactive\nrunning\n200\n9\n1\n' >"$S/unit.routegate-agent"
      for v in 000156_config_version_client_settings 000157_applied_version_account_protocols 000158_explicit_account_protocol_preferences; do
        grep -qx "$v" "$S/history" || { printf '%s\n' "$v" | cat - "$S/history" >"$S/h" && mv "$S/h" "$S/history"; }
        if [[ "${STUB_MIGRATE_FAIL:-0}" == 1 ]]; then exit 1; fi
      done
    fi
    touch "$S/running" ;;
esac
exit 0
EOF
cat >"$stubs/curl" <<'EOF'
#!/usr/bin/env bash
S=$STUB_STATE
url=${*: -1}
if [[ "$url" == */api/admin/health ]]; then
  [[ -f "$S/running" ]] || exit 7
  exit 0
fi
printf '%s' "${STUB_PUBLIC_CODE:-200}"
EOF
cat >"$stubs/chown" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
chmod +x "$stubs"/*

down_runner="$work/down-runner.sh"
cat >"$down_runner" <<'EOF'
#!/usr/bin/env bash
# Stub of deploy-production-like-bundle.sh --run-down-migration.
[[ "$1" == --run-down-migration-inherited && $# -eq 4 && /proc/self/fd/8 -ef "$RG_UPDATE_LOCK_FILE" ]] || exit 2
echo "down $4" >>"$STUB_STATE/down.log"
[[ "${STUB_DOWN_FAIL:-0}" != 1 ]] || exit 1
[[ "$(head -n1 "$STUB_STATE/history")" == "$4" ]] || exit 1
sed -i 1d "$STUB_STATE/history"
EOF
chmod +x "$down_runner"

make_bundle() { # name manifest-commit latest-migration
  local stage="$work/stage-$1" tool
  rm -rf "$stage"
  mkdir -p "$stage"/{bin,frontend/assets,manager/migrations,systemd,metadata,tools}
  printf 'manager-new\n' >"$stage/bin/routegate-manager"
  printf 'agent-new\n' >"$stage/bin/routegate-agent"
  printf 'frontend-new\n' >"$stage/frontend/index.html"
  printf 'asset-new\n' >"$stage/frontend/assets/app.js"
  for v in 000155_config_apply_trigger_invariant_repair 000156_config_version_client_settings \
    000157_applied_version_account_protocols 000158_explicit_account_protocol_preferences; do
    [[ "$v" > "$3" ]] && break
    printf -- '-- up\n' >"$stage/manager/migrations/$v.up.sql"
    printf -- '-- down\n' >"$stage/manager/migrations/$v.down.sql"
  done
  printf 'manager-unit-new\n' >"$stage/systemd/routegate-manager.service"
  printf 'agent-unit-new\n' >"$stage/systemd/routegate-agent.service"
  for tool in release_manifest.py routegate-update-core.sh routegate-update-role.sh routegate-update-transaction.sh routegate-update-verified.sh routegate-update-dispatch.py; do
    printf '# tool\n' >"$stage/tools/$tool"
  done
  printf 'FORMAT_VERSION=1\nVERSION=production-like\nCOMMIT=%s\nBUILD_DATE=2026-10-01T00:00:00Z\nOS=linux\nARCH=amd64\n' "$2" >"$stage/metadata/manifest.env"
  tar -czf "$work/$1.tar.gz" -C "$stage" .
}
make_bundle bundle "$commit" 000158_explicit_account_protocol_preferences
make_bundle bundle157 "$commit" 000157_applied_version_account_protocols
make_bundle othercommit fedcba9876543210fedcba9876543210fedcba98 000158_explicit_account_protocol_preferences

# Fake host: hybrid layout, VPN configs, a published bootstrap tree, schema 155.
vpn_files=(usr/local/bin/routegate-agent etc/systemd/system/routegate-agent.service etc/routegate/agent.yaml
  etc/sing-box/config.json etc/wireguard/routegate-wg0.conf etc/hysteria/config.json
  etc/routegate-mtproto/config.toml etc/nginx/sites-available/routegate var/www/routegate/bootstrap/oldcommit/install-agent.sh)
setup_host() { # schema-history...
  root="$work/root" S="$work/state"
  rm -rf "$root" "$S"
  mkdir -p "$S" "$root"/{usr/local/bin,opt/routegate-manager/migrations,var/www/routegate/assets,etc/systemd/system,etc/routegate,root}
  printf 'manager-old\n' >"$root/usr/local/bin/routegate-manager"
  printf -- '-- up\n' >"$root/opt/routegate-manager/migrations/000155_config_apply_trigger_invariant_repair.up.sql"
  printf -- '-- down\n' >"$root/opt/routegate-manager/migrations/000155_config_apply_trigger_invariant_repair.down.sql"
  printf 'frontend-old\n' >"$root/var/www/routegate/index.html"
  printf 'asset-old\n' >"$root/var/www/routegate/assets/old.js"
  printf 'manager-unit-old\n' >"$root/etc/systemd/system/routegate-manager.service"
  printf 'ROUTEGATE_DATABASE_URL="postgres://rgadmin:S3cretPW@db.internal:5432/routegate"\nOTHER_SECRET=do-not-print\n' >"$root/etc/routegate/manager.env"
  local f
  for f in "${vpn_files[@]}"; do mkdir -p "$(dirname "$root/$f")"; printf 'original %s\n' "$f" >"$root/$f"; done
  printf '%s\n' "$@" >"$S/history"
  printf 'ru-node|1\nus-node|3\n' >"$S/baseline"
  printf 'ru-id|ru-account\nus-id|us-account-1\nus-id|us-account-2\nus-id|us-account-3\n' >"$S/served.before"
  cp "$S/served.before" "$S/served.after"
  printf 'ru-node|ready|1\nus-node|ready|3\n' >"$S/answer.Q4"
  printf 'ru-node|served|1\nus-node|served|3\n' >"$S/answer.P2"
  : >"$S/systemctl.log"; : >"$S/down.log"; : >"$S/restore.log"; : >"$S/psql.log"
  touch "$S/running"
  (cd "$root" && sha256sum "${vpn_files[@]}") >"$work/vpn.sums"
}
H155=(000155_config_apply_trigger_invariant_repair 000154_agent_maintenance_operations)
H158=(000158_explicit_account_protocol_preferences 000157_applied_version_account_protocols 000156_config_version_client_settings "${H155[@]}")

run_update() { # bundle [sha] [preflight-sql] -> rc, out
  local bundle=$1 sha=${2:-} pre=${3:-$pre_sql}
  [[ -n "$sha" ]] || sha=$(sha256sum "$bundle" | awk '{print $1}')
  mkdir -p "$work/tmp"
  set +e
  out=$(PATH="$stubs:$PATH" RG_UPDATE_ROOT="$root" STUB_STATE="$S" STUB_BLOCKS="$blocks" TMPDIR="$work/tmp" \
    ROUTEGATE_UPDATE_HEALTH_POLL_SECONDS=0 ROUTEGATE_UPDATE_DRAIN_POLL_SECONDS=0 \
    bash "$runner" "$commit" "$bundle" "$sha" "$core" "$role" "$down_runner" "$pre" "$post_sql" 2>&1)
  rc=$?
  set -e
}
leaks='S3cretPW|do-not-print|postgres://|db\.internal|rgadmin'
vpn_unchanged() { (cd "$root" && sha256sum --quiet -c "$work/vpn.sums") >/dev/null 2>&1; }
only_manager_touched() { ! grep -Ev '^(is-active|show|daemon-reload)( |$)|^(start|stop) routegate-manager$' "$S/systemctl.log" | grep -q .; }
history_head() { head -n1 "$S/history"; }
no_new_migrations_installed() { ! compgen -G "$root/opt/routegate-manager/migrations/00015[678]_*" >/dev/null; }

# --- runner: success from 155 --------------------------------------------
setup_host "${H155[@]}"
run_update "$work/bundle.tar.gz"
check "runner: 155 -> 158 succeeds" bash -c '[[ $1 -eq 0 ]] && grep -q "RESULT=updated commit=" <<<"$2"' _ "$rc" "$out"
check "runner: schema is 158 after the update" test "$(history_head)" == 000158_explicit_account_protocol_preferences
check "runner: new Manager binary, migrations and frontend installed" bash -c '
  [[ $(cat "$1/usr/local/bin/routegate-manager") == manager-new && -f "$1/opt/routegate-manager/migrations/000158_explicit_account_protocol_preferences.up.sql"
     && $(cat "$1/var/www/routegate/index.html") == frontend-new && ! -e "$1/var/www/routegate/assets/old.js" ]]' _ "$root"
check "runner: Agent, VPN, nginx files and bootstrap artifacts unchanged" vpn_unchanged
check "runner: only the Manager service is stopped/started" only_manager_touched
check "runner: Agent/VPN invariance reported" grep -q "Agent, VPN runtimes, nginx, maintenance dispatch and bootstrap artifacts: unchanged" <<<"$out"
check "runner: full P0-P7 and Q0-Q6 output kept" bash -c '[[ $(grep -c "^== P[0-7]\." <<<"$1") -eq 8 && $(grep -c "^== Q[0-6]\." <<<"$1") -eq 7 ]]' _ "$out"
check "runner: exact commit verified through the binary checksum" grep -q "installed Manager binary matches bundle commit=$commit" <<<"$out"
check "runner: warns that bootstrap artifacts for the commit are unpublished" grep -q "bootstrap artifacts for this commit are not published" <<<"$out"
backup=$(find "$root/root/routegate-backups" -mindepth 1 -maxdepth 1 -type d)
check "runner: backup named for maintenance dispatch and complete" bash -c '
  [[ $(basename "$1") =~ ^update-management-[0-9a-f]{40}-[0-9]{8}T[0-9]{6}Z$ ]] || exit 1
  for f in routegate-manager routegate-manager.service manager.env manager-migrations.tar.gz frontend.tar.gz routegate.pgdump role.meta manager-update.meta; do [[ -s "$1/$f" ]] || exit 1; done
  grep -qx "DATABASE_SCHEMA=000155_config_apply_trigger_invariant_repair" "$1/manager-update.meta"
  grep -qx "TARGET_COMMIT=$2" "$1/manager-update.meta"' _ "$backup" "$commit"
check "runner: Manager stopped before the database backup" bash -c 'grep -n "" "$1" | grep -m1 "stop routegate-manager" >/dev/null' _ "$S/systemctl.log"
check "runner: read-only session for the checks" grep -q 'default_transaction_read_only=on' "$S/psql.log"
check "runner: no rollback on success" bash -c '[[ ! -s "$1" && ! -s "$2" ]]' _ "$S/down.log" "$S/restore.log"
check "runner: prints no URL or secret" bash -c '! grep -Eq "$2" <<<"$1"' _ "$out" "$leaks"
check "runner: removes its working directory" bash -c '[[ -z $(ls -A "$1") ]]' _ "$work/tmp"

# --- runner: same-schema redeploy ---------------------------------------
setup_host "${H158[@]}"
run_update "$work/bundle.tar.gz"
check "runner: 158 redeploy succeeds without the 155 preflight" bash -c '[[ $1 -eq 0 ]] && ! grep -q "^== P0\." <<<"$2"' _ "$rc" "$out"

setup_host "${H158[@]}"
printf 'us-node|new-account|vless|{vless}|awaiting_apply|{vless}\n' >"$S/answer.Q5"
printf 'us-node|awaiting_apply|1\n' >>"$S/answer.Q4"
printf 'us-id|new-account\n' >>"$S/served.before"
printf 'us-id|new-account\n' >>"$S/served.after"
run_update "$work/bundle.tar.gz"
check "runner: already pending new account does not block same-schema update" test "$rc" -eq 0
check "runner: pending account update leaves VPN untouched" vpn_unchanged

setup_host "${H158[@]}"
: >"$S/served.before"; : >"$S/served.after"
printf 'us-node|awaiting_first_apply|1\n' >"$S/answer.Q4"
printf 'us-node|new-account|vless|{vless}|awaiting_first_apply|{vless}\n' >"$S/answer.Q5"
run_update "$work/bundle.tar.gz"
check "runner: no previously served accounts allows a first-apply-pending node" test "$rc" -eq 0

setup_host "${H158[@]}"
sed -i '/us-account-3/d' "$S/served.after"
run_update "$work/bundle.tar.gz"
check "runner: actual same-schema access loss triggers rollback" bash -c '[[ $1 -eq 1 ]] && grep -q "ROLLBACK COMPLETE" <<<"$2"' _ "$rc" "$out"

setup_host "${H158[@]}"
printf 'us-id|newly-served-account\n' >>"$S/served.after"
run_update "$work/bundle.tar.gz"
check "runner: gaining served access does not falsely fail preservation" test "$rc" -eq 0

setup_host "${H158[@]}"
sed -i 's/us-account-3/replacement-account/' "$S/served.after"
run_update "$work/bundle.tar.gz"
check "runner: equal counts with a different served identity roll back" bash -c '[[ $1 -eq 1 ]] && grep -q "ROLLBACK COMPLETE" <<<"$2" && grep -q "previously served account identities lost access" <<<"$2"' _ "$rc" "$out"
check "runner: same-schema rollback restores Manager without down migrations" bash -c '[[ $(cat "$1/usr/local/bin/routegate-manager") == manager-old && ! -s "$2/down.log" && -s "$2/restore.log" ]]' _ "$root" "$S"

setup_host "${H158[@]}"
printf 'us-node|v7|applied|t\n' >"$S/answer.Q3"
run_update "$work/bundle.tar.gz"
check "runner: missing active snapshot refuses an untrustworthy baseline before changes" bash -c '[[ $1 -eq 3 ]] && ! grep -q "^stop" "$2/systemctl.log"' _ "$rc" "$S"

# --- runner: failures roll back ------------------------------------------
rolled_back() { # expected down migrations
  [[ $rc -eq 1 ]] && grep -q "ROLLBACK COMPLETE" <<<"$out" && grep -q "RESULT=failed" <<<"$out" \
    && [[ "$(history_head)" == 000155_config_apply_trigger_invariant_repair ]] \
    && [[ "$(cat "$root/usr/local/bin/routegate-manager")" == manager-old ]] \
    && [[ "$(cat "$root/var/www/routegate/index.html")" == frontend-old ]] \
    && no_new_migrations_installed && [[ -f "$S/running" ]] \
    && [[ "$(awk '{print $2}' "$S/down.log" | tr '\n' ' ')" == "$1" ]] \
    && grep -q -- '--clean --if-exists --no-owner --no-privileges --exit-on-error' "$S/restore.log" \
    && vpn_unchanged && only_manager_touched
}
setup_host "${H155[@]}"
STUB_MIGRATE_FAIL=1 run_update "$work/bundle.tar.gz"
check "runner: Manager start/migration failure rolls back 156 and restores 155" rolled_back "000156_config_version_client_settings "

setup_host "${H155[@]}"
printf 'us-node|us-account-3|vless|{vless}|awaiting_apply|{vless}\n' >"$S/answer.Q5"
run_update "$work/bundle.tar.gz"
check "runner: postflight account refusal rolls back 158..156" rolled_back "000158_explicit_account_protocol_preferences 000157_applied_version_account_protocols 000156_config_version_client_settings "

setup_host "${H155[@]}"
printf 'us-node|v7|applied|t\n' >"$S/answer.Q3"
run_update "$work/bundle.tar.gz"
check "runner: active version without snapshot rolls back" rolled_back "000158_explicit_account_protocol_preferences 000157_applied_version_account_protocols 000156_config_version_client_settings "

setup_host "${H155[@]}"
printf 'fi-node|v1|pending|f\n' >"$S/answer.Q3"
run_update "$work/bundle.tar.gz"
check "runner: pending inactive version without snapshot does not block" test "$rc" -eq 0

setup_host "${H155[@]}"
printf 'ru-node|ready|1\nus-node|ready|2\n' >"$S/answer.Q4"
sed -i '/us-account-3/d' "$S/served.after"
run_update "$work/bundle.tar.gz"
check "runner: fewer served accounts than before rolls back" rolled_back "000158_explicit_account_protocol_preferences 000157_applied_version_account_protocols 000156_config_version_client_settings "

setup_host "${H155[@]}"
STUB_FILE_FAIL=post run_update "$work/bundle.tar.gz"
check "runner: postflight psql error rolls back" rolled_back "000158_explicit_account_protocol_preferences 000157_applied_version_account_protocols 000156_config_version_client_settings "

setup_host "${H155[@]}"
STUB_PUBLIC_CODE=502 run_update "$work/bundle.tar.gz"
check "runner: public frontend failure rolls back" rolled_back "000158_explicit_account_protocol_preferences 000157_applied_version_account_protocols 000156_config_version_client_settings "

setup_host "${H155[@]}"
STUB_MIGRATE_FAIL=1 STUB_DOWN_FAIL=1 run_update "$work/bundle.tar.gz"
check "runner: failed rollback is reported as incomplete" bash -c '[[ $1 -eq 1 ]] && grep -q "ROLLBACK INCOMPLETE" <<<"$2" && ! grep -q "ROLLBACK COMPLETE" <<<"$2"' _ "$rc" "$out"

# --- runner: Agent/VPN change is detected, not rolled back ---------------
setup_host "${H155[@]}"
STUB_AGENT_CHANGE=1 run_update "$work/bundle.tar.gz"
check "runner: Agent state change gives exit 6 with labels only" bash -c '
  [[ $1 -eq 6 ]] && grep -q "state changed during the update: unit:routegate-agent" <<<"$2" && ! grep -q "running 200" <<<"$2"' _ "$rc" "$out"

# --- runner: refusals before any change ----------------------------------
unchanged_host() {
  [[ "$(cat "$root/usr/local/bin/routegate-manager")" == manager-old ]] && ! grep -q '^stop' "$S/systemctl.log" \
    && [[ ! -e "$root/root/routegate-backups" ]] && [[ "$(history_head)" == "$1" ]]
}
setup_host "${H155[@]}"
printf 'us-node|aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee|vless|{vless}|awaiting_apply|{vless}\n' >"$S/answer.P3"
run_update "$work/bundle.tar.gz"
check "runner: preflight predicting refused accounts is refused (exit 3)" bash -c '[[ $1 -eq 3 ]]' _ "$rc"
check "runner: refused preflight changes nothing" unchanged_host 000155_config_apply_trigger_invariant_repair

setup_host "${H155[@]}"
printf 'fi-node|unchecked_no_snapshot|1\nus-node|served|3\n' >"$S/answer.P2"
run_update "$work/bundle.tar.gz"
check "runner: unchecked node in the preflight is refused" bash -c '[[ $1 -eq 3 ]]' _ "$rc"

setup_host 000154_agent_maintenance_operations
run_update "$work/bundle.tar.gz"
check "runner: unexpected schema refused, nothing changed" bash -c '[[ $1 -eq 3 ]]' _ "$rc"
check "runner: unexpected schema leaves host as is" unchanged_host 000154_agent_maintenance_operations

setup_host "${H155[@]}"
run_update "$work/bundle157.tar.gz"
check "runner: bundle not targeting 158 refused" bash -c '[[ $1 -eq 3 ]]' _ "$rc"
check "runner: wrong-target bundle leaves host as is" unchanged_host 000155_config_apply_trigger_invariant_repair

setup_host "${H155[@]}"
run_update "$work/othercommit.tar.gz"
check "runner: bundle of another commit fails before any change" bash -c '[[ $1 -ne 0 ]] && grep -q "bundle commit does not match" <<<"$2"' _ "$rc" "$out"
check "runner: other-commit bundle leaves host as is" unchanged_host 000155_config_apply_trigger_invariant_repair

setup_host "${H155[@]}"
run_update "$work/bundle.tar.gz" "$(printf '0%.0s' {1..64})"
check "runner: bundle checksum mismatch fails before any change" bash -c '[[ $1 -ne 0 ]]' _ "$rc"
check "runner: checksum mismatch leaves host as is" unchanged_host 000155_config_apply_trigger_invariant_repair

setup_host "${H155[@]}"
cp "$pre_sql" "$work/altered.sql"; printf -- '-- extra\n' >>"$work/altered.sql"
run_update "$work/bundle.tar.gz" "" "$work/altered.sql"
check "runner: altered preflight SQL refused (exit 2)" bash -c '[[ $1 -eq 2 ]]' _ "$rc"
set +e; bash "$runner" "$commit" 2>/dev/null; rc=$?; set -e
check "runner: wrong argument count refused (exit 2)" test "$rc" -eq 2
set +e; bash "$runner" "ABC" "$work/bundle.tar.gz" x "$core" "$role" "$down_runner" "$pre_sql" "$post_sql" 2>/dev/null; rc=$?; set -e
check "runner: invalid commit refused (exit 2)" test "$rc" -eq 2

# --- workflow --------------------------------------------------------------
step() { # workflow step name -> its run script
  python3 - "$workflow" "$1" <<'EOF'
import sys, yaml
workflow, name = sys.argv[1], sys.argv[2]
for step in yaml.safe_load(open(workflow))["jobs"]["operate"]["steps"]:
    if step.get("name") == name:
        sys.stdout.write(step["run"])
        sys.exit(0)
sys.exit(f"step not found: {name}")
EOF
}
step "Resolve allow-listed operation" >"$work/resolve.sh"
resolve() { # event value [commit] [ref] -> "operation commit" or "exit N"
  local event=$1 value=$2 dcommit=${3:-} ref=${4:-refs/heads/main} output="$work/resolve.out" code
  : >"$output"
  set +e
  if [[ "$event" == workflow_dispatch ]]; then
    EVENT_NAME=$event DISPATCH_OPERATION=$value DISPATCH_COMMIT=$dcommit REQUEST_BODY='' GITHUB_REF=$ref GITHUB_OUTPUT=$output bash "$work/resolve.sh" >/dev/null 2>&1
  else
    EVENT_NAME=$event DISPATCH_OPERATION='' DISPATCH_COMMIT='' REQUEST_BODY=$value GITHUB_REF=$ref GITHUB_OUTPUT=$output bash "$work/resolve.sh" >/dev/null 2>&1
  fi
  code=$?
  set -e
  if ((code == 0)); then printf '%s %s\n' "$(sed -n 's/^operation=//p' "$output")" "$(sed -n 's/^commit=//p' "$output")"; else echo "exit $code"; fi
}
check "workflow: exact issue request accepted" test "$(resolve issues "operation=update-manager commit=$commit")" == "update-manager $commit"
check "workflow: main dispatch with commit accepted" test "$(resolve workflow_dispatch update-manager "$commit")" == "update-manager $commit"
check "workflow: dispatch from another ref refused" test "$(resolve workflow_dispatch update-manager "$commit" refs/heads/feature)" == "exit 2"
for value in "" "${commit^^}" "${commit:0:39}" "${commit}0" "$commit;id" "main" "HEAD" "../$commit"; do
  check "workflow: dispatch commit '$value' refused" test "$(resolve workflow_dispatch update-manager "$value")" == "exit 2"
done
for body in "operation=update-manager commit=$commit " "operation=update-manager commit=$commit"$'\n' "operation=update-manager  commit=$commit" \
  "operation=update-manager" "operation=update-manager commit=main" "operation=update-manager commit=$commit"$'\n'"url=https://example.com" \
  "commit=$commit operation=update-manager" "operation=update-manager commit=${commit^^}" "operation=update-manager commit=$commit&sql=1"; do
  check "workflow: request '${body//$'\n'/\\n}' refused" test "$(resolve issues "$body")" == "exit 2"
done
check "workflow: commit input refused for other operations" test "$(resolve workflow_dispatch diagnose "$commit")" == "exit 2"
check "workflow: other operations still resolve without a commit" test "$(resolve workflow_dispatch diagnose)" == "diagnose "

check "workflow: update steps verify CI, ancestry and the pinned build before SSH" python3 - "$workflow" <<'EOF'
import sys, yaml
steps = yaml.safe_load(open(sys.argv[1]))["jobs"]["operate"]["steps"]
names = [s.get("name") for s in steps]
def idx(n): return names.index(n)
by = {s.get("name"): s for s in steps}
ci = by["Verify update-manager target is on main with a successful CI run"]["run"]
co = by["Checkout pinned update-manager target"]
anc = by["Verify pinned checkout is in main"]["run"]
build = by["Build pinned Manager update bundle"]
upload = by["Upload full Manager update result"]
ok = (idx("Checkout trusted main") == 0
      and by["Checkout trusted main"]["with"]["ref"] == "main"
      and "ci.yml/runs?head_sha=${TARGET_SHA}&branch=main&event=push&status=success" in ci
      and co["with"]["ref"] == "${{ steps.request.outputs.commit }}" and co["with"]["persist-credentials"] is False
      and "merge-base --is-ancestor" in anc
      and build["working-directory"] == "target" and build["env"]["COMMIT"] == "${{ steps.request.outputs.commit }}"
      and "COMMIT=${COMMIT}" in build["run"]
      and idx("Verify update-manager target is on main with a successful CI run") < idx("Build pinned Manager update bundle") < idx("Configure SSH")
      and upload["with"]["retention-days"] == 7 and "update-manager" in upload["if"])
sys.exit(0 if ok else 1)
EOF

# Remote step end to end: stub ssh/scp run the real runner on the fake host.
cat >"$stubs/sudo" <<'EOF'
#!/usr/bin/env bash
exec "$@"
EOF
cat >"$stubs/flock" <<'EOF'
#!/usr/bin/env bash
if [[ $1 == -n ]]; then exec /usr/bin/flock "$@"; fi
shift 3
exec "$@"
EOF
cat >"$stubs/scp" <<'EOF'
#!/usr/bin/env bash
src=${*: -2:1}
dst=${*: -1}
dst=${dst#*:}
[[ "${STUB_SCP_FAIL:-}" == "$(basename "$src")" ]] && exit 1
cp "$src" "$dst"
echo "$dst" >>"$STUB_REMOTE_FILES"
EOF
cat >"$stubs/ssh" <<'EOF'
#!/usr/bin/env bash
exec bash -c "${*: -1}"
EOF
cat >"$stubs/gh" <<'EOF'
#!/usr/bin/env bash
cat >>"$STUB_GH_LOG"
EOF
chmod +x "$stubs"/*
step "Run allow-listed remote operation" >"$work/remote.sh"
remote() { # [scp-fail]
  export GITHUB_RUN_ID=$RANDOM$RANDOM RUNNER_TEMP="$work/runner" GITHUB_OUTPUT="$work/gh-output"
  rm -rf "$RUNNER_TEMP"; mkdir -p "$RUNNER_TEMP"; : >"$GITHUB_OUTPUT"; : >"$work/remote-files"
  # The real runner on the fake host needs the real deploy script's down runner path:
  # point the remote copy at the stub by overriding the local source file list.
  sed 's#scripts/deploy-production-like-bundle.sh$#'"$down_runner"'#' "$work/remote.sh" >"$work/remote-test.sh"
  set +e
  log=$(cd "$repo" && PATH="$stubs:$PATH" HOME="$work/home" RG_UPDATE_ROOT="$root" STUB_STATE="$S" STUB_BLOCKS="$blocks" \
    STUB_REMOTE_FILES="$work/remote-files" STUB_SCP_FAIL=${1:-} ROUTEGATE_UPDATE_HEALTH_POLL_SECONDS=0 \
    ROUTEGATE_UPDATE_DRAIN_POLL_SECONDS=0 STUB_FILE_FAIL=${STUB_FILE_FAIL:-} TMPDIR="$work/tmp" \
    ROUTEGATE_HOST=manager.example ROUTEGATE_USER=ops OPERATION=update-manager TARGET_SHA='' BUNDLE_FILE='' BUNDLE_SHA='' \
    MANAGER_COMMIT=$commit MANAGER_BUNDLE="$work/bundle.tar.gz" MANAGER_BUNDLE_SHA="$(sha256sum "$work/bundle.tar.gz" | awk '{print $1}')" \
    bash "$work/remote-test.sh" 2>&1)
  step_rc=$?
  set -e
  remote_rc=$(sed -n 's/^rc=//p' "$GITHUB_OUTPUT")
}
remote_files_removed() { local f; while read -r f; do [[ ! -e "$f" ]] || return 1; done <"$work/remote-files"; }
check "workflow: remote step copies the runner from main, not the target" grep -q 'scripts/production-like-update-manager.sh' "$work/remote.sh"

setup_host "${H155[@]}"
remote
check "workflow: remote update succeeds with rc=0" bash -c '[[ $1 -eq 0 && $2 == 0 ]]' _ "$step_rc" "$remote_rc"
check "workflow: copies the bundle, runner, libraries and pinned SQL only" bash -c '[[ $(wc -l <"$1") -eq 7 ]]' _ "$work/remote-files"
check "workflow: removes the remote files" remote_files_removed
check "workflow: full output kept for the artifact" bash -c '[[ $(grep -c "^== Q[0-6]\." "$1") -eq 7 && $(grep -c "^== P[0-7]\." "$1") -eq 8 ]]' _ "$RUNNER_TEMP/routegate-ops-output.txt"
check "workflow: log shows only status lines" bash -c '! grep -q "^== [PQ]" <<<"$1" && ! grep -q "4333-8444" <<<"$1" && grep -q "RESULT=updated" <<<"$1"' _ "$log"

setup_host "${H155[@]}"
STUB_FILE_FAIL=post remote
check "workflow: failed update propagates rc=1" test "$remote_rc" == 1
check "workflow: connection details are redacted in the artifact" bash -c '! grep -Eq "$2" "$1" && grep -q "details redacted" "$1"' _ "$RUNNER_TEMP/routegate-ops-output.txt" "$leaks"
check "workflow: failed update still removes remote files" remote_files_removed

setup_host "${H155[@]}"
remote production-like-update-manager.sh
check "workflow: copy failure gives rc=2, runs nothing, cleans up" bash -c '[[ $1 == 2 ]] && [[ $(cat "$2/usr/local/bin/routegate-manager") == manager-old ]]' _ "$remote_rc" "$root"
check "workflow: copy failure leaves no remote files" remote_files_removed

step "Publish sanitized audit result" >"$work/audit.sh"
setup_host "${H155[@]}"
remote
: >"$work/gh.log"
(cd "$repo" && PATH="$stubs:$PATH" STUB_GH_LOG="$work/gh.log" GH_TOKEN=x OPERATION=update-manager REMOTE_RC=0 \
  EVENT_NAME=workflow_dispatch MANAGER_COMMIT=$commit GITHUB_REPOSITORY=owner/repo GITHUB_RUN_ID=42 RUNNER_TEMP="$work/runner" bash "$work/audit.sh")
check "workflow: #268 comment has status, commit and artifact only" bash -c '
  grep -q PASSED "$1" && grep -q "target commit=$2" "$1" && grep -q "routegate-update-manager-42" "$1" && ! grep -q "== Q" "$1" && ! grep -q "4333-8444" "$1"' _ "$work/gh.log" "$commit"

# --- deploy workflow ---------------------------------------------------------
check "deploy workflow: manual dispatch from main only, never after CI" python3 - "$deploy_workflow" "$repo/.github/workflows" <<'EOF'
import glob, sys, yaml
wf = yaml.safe_load(open(sys.argv[1]))
on = wf.get(True, wf.get("on"))
job = wf["jobs"]["deploy"]
ok = (list(on) == ["workflow_dispatch"]
      and job["if"] == "github.event_name == 'workflow_dispatch' && github.ref == 'refs/heads/main'")
# No workflow holding production-like secrets starts on push, CI completion or a PR.
for path in glob.glob(sys.argv[2] + "/*.yml"):
    text = open(path).read()
    if "ROUTEGATE_SSH_KEY" not in text:
        continue
    w = yaml.safe_load(text)
    triggers = w.get(True, w.get("on"))
    triggers = [triggers] if isinstance(triggers, str) else list(triggers)
    if set(triggers) - {"workflow_dispatch", "issues"}:
        ok = False
sys.exit(0 if ok else 1)
EOF

if ((failures)); then
  printf '%d check(s) failed\n' "$failures" >&2
  exit 1
fi
echo "all update-manager ops checks passed"
