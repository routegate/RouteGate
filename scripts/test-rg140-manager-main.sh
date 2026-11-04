#!/usr/bin/env bash
# Failure-injection tests for pinned schema159 Manager main publication.
# Stub host only: no SSH, live PostgreSQL, systemd or GitHub operations.
# shellcheck disable=SC2016
set -Eeuo pipefail

repo=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
runner="$repo/scripts/production-like-rg140-manager-main.sh"
core="$repo/scripts/routegate-update-core.sh"
role="$repo/scripts/routegate-update-role.sh"
pre_sql="$repo/docs/operations/rg140-manager-main/preflight-schema-158.sql"
post_sql="$repo/docs/operations/rg140-manager-main/postflight-schema-159.sql"
workflow="$repo/.github/workflows/production-like-ops.yml"
deploy_workflow="$repo/.github/workflows/production-like-deploy.yml"
[[ $# -le 1 ]] || { echo 'usage: test-rg140-manager-main.sh [pinned-commit]' >&2; exit 2; }
commit=${1:-c4137cc43a2385ac38fe71e059f33405535b6618}
case "$commit" in
  c4137cc43a2385ac38fe71e059f33405535b6618|567a1e154254eebb7f8ee95e46a97c5d0b3c8e4a) ;;
  *) echo 'test candidate must be one of the two approved schema159 commits' >&2; exit 2 ;;
esac
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
for spec in "$pre_sql:P2" "$pre_sql:P3" "$post_sql:Q0" "$post_sql:Q3" "$post_sql:Q4" "$post_sql:Q5" "$post_sql:Q7"; do
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
for id in P2 P3 Q0 Q3 Q4 Q5 Q7; do
  if [[ "$sql" == "$(cat "$STUB_BLOCKS/$id.sql")" ]]; then
    if [[ "$id" == P3 && "$(cat "$RG_UPDATE_ROOT/usr/local/bin/routegate-manager")" == manager-new ]]; then id=Q5; fi
    if [[ "$id" == Q7 && "${STUB_TRANSFER_RACE:-0}" == 1 && ! -f "$S/running" ]]; then echo '1|1'; exit 0; fi
    if [[ -f "$S/answer.$id" ]]; then cat "$S/answer.$id"; fi
    [[ "$id" != Q7 || -f "$S/answer.Q7" ]] || echo "0|0"
    [[ "$id" != Q0 ]] || [[ -f "$S/answer.Q0" ]] || printf '%s|%s\n' "$(head -n1 "$S/history")" "$(grep -c '^000159_' "$S/history")"
    exit 0
  fi
done
case "$sql" in
  *"SELECT md5(jsonb_build_object("*)
    if [[ "${STUB_IDENTITY_CHANGE:-0}" == 1 && "$(cat "$RG_UPDATE_ROOT/usr/local/bin/routegate-manager")" == manager-new ]]; then echo changed; else echo original; fi ;;
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
      for v in 000159_staged_account_transfers; do
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
if [[ " $* " != *" -w "* ]]; then
  [[ "${STUB_PUBLIC_CODE:-200}" == 200 ]] || exit 22
  if [[ "${STUB_PUBLIC_MISMATCH:-0}" == 1 ]]; then echo stale-index; else cat "$RG_UPDATE_ROOT/var/www/routegate/index.html"; fi
else
  printf '%s' "${STUB_PUBLIC_CODE:-200}"
fi
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
  for v in 000158_explicit_account_protocol_preferences 000156_config_version_client_settings \
    000157_applied_version_account_protocols 000159_staged_account_transfers; do
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
make_bundle bundle "$commit" 000159_staged_account_transfers
make_bundle bundle157 "$commit" 000158_explicit_account_protocol_preferences
make_bundle othercommit fedcba9876543210fedcba9876543210fedcba98 000159_staged_account_transfers

# Fake host: hybrid layout, VPN configs, a published bootstrap tree, schema 155.
vpn_files=(usr/local/bin/routegate-agent etc/systemd/system/routegate-agent.service etc/routegate/agent.yaml
  etc/sing-box/config.json etc/wireguard/routegate-wg0.conf etc/hysteria/config.json
  etc/routegate-mtproto/config.toml etc/nginx/sites-available/routegate var/www/routegate/bootstrap/oldcommit/install-agent.sh)
setup_host() { # schema-history...
  root="$work/root" S="$work/state"
  rm -rf "$root" "$S"
  mkdir -p "$S" "$root"/{usr/local/bin,opt/routegate-manager/migrations,var/www/routegate/assets,etc/systemd/system,etc/routegate,root}
  printf 'manager-old\n' >"$root/usr/local/bin/routegate-manager"
  printf -- '-- up\n' >"$root/opt/routegate-manager/migrations/000159_staged_account_transfers.up.sql"
  printf -- '-- down\n' >"$root/opt/routegate-manager/migrations/000159_staged_account_transfers.down.sql"
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
H155=(000158_explicit_account_protocol_preferences 000154_agent_maintenance_operations)
H158=(000159_staged_account_transfers "${H155[@]}")

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
no_new_migrations_installed() { ! compgen -G "$root/opt/routegate-manager/migrations/000159_*" >/dev/null; }


setup() { setup_host "${H158[@]}"; }
unchanged() {
 [[ $(cat "$root/usr/local/bin/routegate-manager") == manager-old ]] && ! grep -q '^stop' "$S/systemctl.log" && [[ ! -e "$root/root/routegate-backups" ]]
}
restored() {
 [[ $rc -eq 1 ]] && grep -q 'ROLLBACK COMPLETE' <<<"$out" && [[ $(history_head) == 000159_staged_account_transfers ]] && [[ $(cat "$root/usr/local/bin/routegate-manager") == manager-old ]] && [[ $(cat "$root/var/www/routegate/index.html") == frontend-old ]] && [[ -f "$S/running" ]] && [[ ! -s "$S/down.log" ]] && vpn_unchanged && only_manager_touched
}
setup; run_update "$work/bundle.tar.gz"
check 'schema159 publication succeeds' test "$rc" -eq 0
check 'schema remains159' test "$(history_head)" == 000159_staged_account_transfers
check 'Manager and frontend published, old hashed assets retained' bash -c '[[ $(cat "$1/usr/local/bin/routegate-manager") == manager-new && $(cat "$1/var/www/routegate/index.html") == frontend-new && -f "$1/var/www/routegate/assets/old.js" ]]' _ "$root"
check 'VPN files and bootstrap unchanged' vpn_unchanged
check 'only Manager stopped/started' only_manager_touched
backup=$(find "$root/root/routegate-backups" -mindepth 1 -maxdepth 1 -type d)
check 'consistent database and Manager backup retained' bash -c 'for f in routegate-manager routegate-manager.service manager.env manager-migrations.tar.gz frontend.tar.gz routegate.pgdump manager-update.meta; do [[ -s "$1/$f" ]] || exit 1; done; grep -qx DATABASE_SCHEMA=000159_staged_account_transfers "$1/manager-update.meta"' _ "$backup"
check 'secrets absent from output' bash -c '! grep -Eq "$2" <<<"$1"' _ "$out" "$leaks"
check 'no down migration or restore on success' bash -c '[[ ! -s "$1/down.log" && ! -s "$1/restore.log" ]]' _ "$S"
check 'read-only preflight' grep -q default_transaction_read_only=on "$S/psql.log"

setup; STUB_MIGRATE_FAIL=1 run_update "$work/bundle.tar.gz"
check 'failed Manager start restores existing schema159 and previous files' restored
setup; STUB_PUBLIC_CODE=502 run_update "$work/bundle.tar.gz"
check 'public health failure restores previous Manager and interface' restored
setup; STUB_PUBLIC_MISMATCH=1 run_update "$work/bundle.tar.gz"
check 'wrong public index triggers recovery' restored
setup; STUB_IDENTITY_CHANGE=1 run_update "$work/bundle.tar.gz"
check 'account/device/token/transfer identity change triggers recovery' restored
setup; sed -i '/us-account-3/d' "$S/served.after"; run_update "$work/bundle.tar.gz"
check 'served account loss triggers recovery' restored
setup; STUB_AGENT_CHANGE=1 run_update "$work/bundle.tar.gz"
check 'unrelated service change reported separately' test "$rc" -eq 6

setup; printf '1|1\n' >"$S/answer.Q7"; run_update "$work/bundle.tar.gz"
check 'active transfer refused before stop and backup' bash -c '[[ $1 -eq 3 ]]' _ "$rc"
check 'active transfer refusal changes nothing' unchanged
setup; STUB_TRANSFER_RACE=1 run_update "$work/bundle.tar.gz"
check 'transfer racing drain aborts and restarts Manager unchanged' bash -c '[[ $1 -eq 1 ]] && grep -q "Manager restarted unchanged" <<<"$2"' _ "$rc" "$out"
check 'raced transfer starts no mutation or backup' bash -c '[[ $(cat "$1/usr/local/bin/routegate-manager") == manager-old && ! -e "$1/root/routegate-backups" && -f "$2/running" ]]' _ "$root" "$S"
setup_host "${H155[@]}"; run_update "$work/bundle.tar.gz"
check 'old schema refused before stop' bash -c '[[ $1 -eq 3 ]]' _ "$rc"
check 'old schema refusal changes nothing' unchanged
setup; run_update "$work/bundle157.tar.gz"
check 'bundle schema mismatch refused before stop' bash -c '[[ $1 -eq 3 ]]' _ "$rc"
check 'wrong bundle changes nothing' unchanged
setup; run_update "$work/othercommit.tar.gz"
check 'wrong commit rejected' bash -c '[[ $1 -ne 0 ]]' _ "$rc"
check 'wrong commit changes nothing' unchanged
setup; run_update "$work/bundle.tar.gz" "$(printf '0%.0s' {1..64})"
check 'checksum mismatch rejected' bash -c '[[ $1 -ne 0 ]]' _ "$rc"
check 'checksum mismatch changes nothing' unchanged

if ((failures)); then printf '%d checks failed\n' "$failures" >&2; exit 1; fi
echo 'all schema159 Manager publication checks passed'
