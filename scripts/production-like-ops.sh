#!/usr/bin/env bash
set -Eeuo pipefail
IFS=$'\n\t'
umask 077

OPERATION=${1:-}
PUBLIC_URL=${ROUTEGATE_PUBLIC_URL_OVERRIDE:-https://us.routegate.org}

log() {
  printf '[routegate-ops] %s\n' "$*"
}

die() {
  printf '[routegate-ops] ERROR: %s\n' "$*" >&2
  exit 1
}

require_root() {
  [[ ${EUID:-$(id -u)} -eq 0 ]] || die "must run as root"
}

service_state() {
  local label=$1
  local service=$2
  local load_state active_state sub_state result restarts exec_status exec_code

  load_state=$(systemctl show --property=LoadState --value "$service" 2>/dev/null || true)
  if [[ "$load_state" != "loaded" ]]; then
    log "service=${label} installed=false"
    return 0
  fi

  active_state=$(systemctl show --property=ActiveState --value "$service" 2>/dev/null || true)
  sub_state=$(systemctl show --property=SubState --value "$service" 2>/dev/null || true)
  result=$(systemctl show --property=Result --value "$service" 2>/dev/null || true)
  restarts=$(systemctl show --property=NRestarts --value "$service" 2>/dev/null || true)
  exec_status=$(systemctl show --property=ExecMainStatus --value "$service" 2>/dev/null || true)
  exec_code=$(systemctl show --property=ExecMainCode --value "$service" 2>/dev/null || true)
  log "service=${label} installed=true active=${active_state:-unknown} sub=${sub_state:-unknown} result=${result:-unknown} restarts=${restarts:-unknown} exec-code=${exec_code:-unknown} exec-status=${exec_status:-unknown}"
}

control_plane_diagnostics() {
  service_state postgresql postgresql
  service_state manager routegate-manager.service
  service_state agent routegate-agent.service
  service_state nginx nginx
  service_state certbot-timer certbot.timer
}

runtime_diagnostics() {
  service_state sing-box sing-box.service
  service_state wireguard wg-quick@routegate-wg0.service
  service_state hysteria2 hysteria-server.service
  service_state mtproto routegate-mtproto.service

  if command -v sing-box >/dev/null 2>&1 && [[ -r /etc/sing-box/config.json ]]; then
    if sing-box check -c /etc/sing-box/config.json >/dev/null 2>&1; then
      log "runtime=sing-box config=valid"
    else
      log "runtime=sing-box config=invalid"
    fi
  else
    log "runtime=sing-box config=unavailable"
  fi
}

classify_sing_box_failure() {
  local journal lower failure_class conflict_line conflict_port socket_line owner_process
  journal=$(journalctl -u sing-box.service -n 120 --no-pager -o cat 2>/dev/null || true)
  lower=$(printf '%s' "$journal" | tr '[:upper:]' '[:lower:]')
  failure_class=unknown

  if [[ -z "$lower" ]]; then
    failure_class=no-journal-data
  elif [[ "$lower" == *"address already in use"* ]] || [[ "$lower" == *"bind: address already in use"* ]]; then
    failure_class=address-in-use
  elif [[ "$lower" == *"permission denied"* ]]; then
    failure_class=permission-denied
  elif [[ "$lower" == *"operation not permitted"* ]]; then
    failure_class=operation-not-permitted
  elif [[ "$lower" == *"no such file or directory"* ]]; then
    failure_class=missing-file
  elif [[ "$lower" == *"certificate"* ]] && [[ "$lower" == *"error"* || "$lower" == *"failed"* ]]; then
    failure_class=certificate-error
  elif [[ "$lower" == *"unknown field"* ]] || [[ "$lower" == *"unknown option"* ]] || [[ "$lower" == *"invalid argument"* ]]; then
    failure_class=runtime-compatibility
  elif [[ "$lower" == *"network is unreachable"* ]]; then
    failure_class=network-unreachable
  fi

  log "runtime=sing-box failure-class=${failure_class}"

  if [[ "$failure_class" == address-in-use ]]; then
    conflict_line=$(printf '%s\n' "$journal" | grep -Ei 'address already in use|bind: address already in use' | tail -n 1 || true)
    conflict_port=$(printf '%s\n' "$conflict_line" | grep -Eo ':[0-9]{1,5}' | tail -n 1 | tr -d ':' || true)
    if [[ "$conflict_port" =~ ^[0-9]+$ ]] && (( conflict_port >= 1 && conflict_port <= 65535 )); then
      socket_line=$(ss -H -ltnp "sport = :${conflict_port}" 2>/dev/null | head -n 1 || true)
      if [[ -z "$socket_line" ]]; then
        socket_line=$(ss -H -lunp "sport = :${conflict_port}" 2>/dev/null | head -n 1 || true)
      fi
      owner_process=$(printf '%s\n' "$socket_line" | sed -n 's/.*users:(("\([^"]*\)".*/\1/p' | head -n 1)
      log "runtime=sing-box conflict-port=${conflict_port} owner-process=${owner_process:-unknown}"
    else
      log "runtime=sing-box conflict-port=unknown owner-process=unknown"
    fi
  fi
}

sing_box_diagnostics() {
  service_state sing-box sing-box.service
  command -v sing-box >/dev/null 2>&1 || die "sing-box binary is unavailable"
  [[ -r /etc/sing-box/config.json ]] || die "sing-box config is unavailable"

  local version config_state directory_state unit_execstart config_count config_names
  version=$(sing-box version 2>/dev/null | head -n 1 || true)
  if sing-box check -c /etc/sing-box/config.json >/dev/null 2>&1; then
    config_state=valid
  else
    config_state=invalid
  fi
  if sing-box check -C /etc/sing-box >/dev/null 2>&1; then
    directory_state=valid
  else
    directory_state=invalid
  fi

  config_count=$(find /etc/sing-box -maxdepth 1 -type f \( -name '*.json' -o -name '*.jsonc' \) -printf '%f\n' 2>/dev/null | wc -l | tr -d ' ')
  config_names=$(find /etc/sing-box -maxdepth 1 -type f \( -name '*.json' -o -name '*.jsonc' \) -printf '%f\n' 2>/dev/null | LC_ALL=C sort | head -n 20 | paste -sd ',' -)
  unit_execstart=$(systemctl show --property=ExecStart --value sing-box.service 2>/dev/null || true)
  unit_execstart=${unit_execstart//$'\n'/ }
  unit_execstart=${unit_execstart:0:240}

  log "runtime=sing-box version=${version:-unknown}"
  log "runtime=sing-box config=${config_state}"
  log "runtime=sing-box directory-config=${directory_state} files=${config_count:-0} names=${config_names:-none}"
  log "runtime=sing-box unit-execstart=${unit_execstart:-unknown}"
  classify_sing_box_failure

  # Raw journal/config/socket output never leaves the host. Only fixed failure
  # classes plus the conflicting port and process name are exposed.
}

http_diagnostics() {
  local local_status public_status
  local_status=$(curl -sS --max-time 10 -o /dev/null -w '%{http_code}' http://127.0.0.1:8080/api/admin/health || true)
  public_status=$(curl -sS --max-time 15 -o /dev/null -w '%{http_code}' "$PUBLIC_URL/" || true)
  log "http=manager-local status=${local_status:-000}"
  log "http=public-root status=${public_status:-000}"
}

load_manager_database() {
  [[ -r /etc/routegate/manager.env ]] || return 1
  command -v psql >/dev/null 2>&1 || return 1
  set -a
  # shellcheck disable=SC1091
  source /etc/routegate/manager.env
  set +a
  [[ -n ${ROUTEGATE_DATABASE_URL:-} ]]
}

schema_diagnostics() {
  if ! load_manager_database; then
    log "schema=unavailable reason=database-client-url-or-env-missing"
    return 0
  fi

  local latest_schema
  latest_schema=$(psql "$ROUTEGATE_DATABASE_URL" -qAtc "SELECT version FROM schema_migrations ORDER BY version DESC LIMIT 1" 2>/dev/null || true)
  if [[ -n "$latest_schema" ]]; then
    log "schema=ok latest=${latest_schema}"
  else
    log "schema=unavailable reason=query-failed"
  fi
}

protocol_activation_diagnostics() {
  if ! load_manager_database; then
    log "protocol-activation=unavailable reason=database-client-url-or-env-missing"
    return 0
  fi

  local counts latest protocol_rows account_index desired active primary job_status job_age job_started_age job_error_class
  counts=$(psql "$ROUTEGATE_DATABASE_URL" -qAt -F '|' -c "
    SELECT
      count(*) FILTER (WHERE status = 'pending'),
      count(*) FILTER (WHERE status = 'in_progress'),
      count(*) FILTER (WHERE status = 'failed' AND created_at > now() - interval '30 minutes'),
      count(*) FILTER (WHERE status = 'succeeded' AND created_at > now() - interval '30 minutes')
    FROM config_apply_jobs
  " 2>/dev/null || true)
  if [[ "$counts" =~ ^[0-9]+\|[0-9]+\|[0-9]+\|[0-9]+$ ]]; then
    IFS='|' read -r pending_count in_progress_count failed_recent succeeded_recent <<<"$counts"
    log "protocol-activation jobs-pending=${pending_count} jobs-in-progress=${in_progress_count} failed-30m=${failed_recent} succeeded-30m=${succeeded_recent}"
  else
    log "protocol-activation jobs=unavailable"
  fi

  latest=$(psql "$ROUTEGATE_DATABASE_URL" -qAt -F '|' -c "
    SELECT
      status,
      GREATEST(0, floor(extract(epoch FROM (now() - created_at))))::bigint,
      CASE WHEN started_at IS NULL THEN -1 ELSE GREATEST(0, floor(extract(epoch FROM (now() - started_at))))::bigint END,
      CASE
        WHEN COALESCE(error_message, '') = '' THEN 'none'
        WHEN error_message ILIKE '%completion was not confirmed%' THEN 'completion-unconfirmed'
        WHEN error_message ILIKE '%listener%' THEN 'listener-health'
        WHEN error_message ILIKE '%restart%' THEN 'restart'
        WHEN error_message ILIKE '%timeout%' THEN 'timeout'
        ELSE 'other'
      END
    FROM config_apply_jobs
    ORDER BY created_at DESC
    LIMIT 1
  " 2>/dev/null || true)
  if [[ -n "$latest" ]]; then
    IFS='|' read -r job_status job_age job_started_age job_error_class <<<"$latest"
    log "protocol-activation latest-job-status=${job_status:-unknown} age-seconds=${job_age:-unknown} started-age-seconds=${job_started_age:--1} error-class=${job_error_class:-unknown}"
  else
    log "protocol-activation latest-job-status=none"
  fi

  protocol_rows=$(psql "$ROUTEGATE_DATABASE_URL" -qAt -F '|' -c "
    WITH account_state AS (
      SELECT
        a.id,
        COALESCE(NULLIF(cp.protocol, ''), 'auto') AS primary_protocol,
        COALESCE(string_agg(pap.protocol, ',' ORDER BY CASE pap.protocol WHEN 'vless' THEN 1 WHEN 'wireguard' THEN 2 WHEN 'hysteria2' THEN 3 WHEN 'shadowsocks' THEN 4 WHEN 'mtproto' THEN 5 ELSE 99 END) FILTER (WHERE pap.desired_enabled), '') AS desired_protocols,
        COALESCE(string_agg(pap.protocol, ',' ORDER BY CASE pap.protocol WHEN 'vless' THEN 1 WHEN 'wireguard' THEN 2 WHEN 'hysteria2' THEN 3 WHEN 'shadowsocks' THEN 4 WHEN 'mtproto' THEN 5 ELSE 99 END) FILTER (WHERE pap.active_enabled), '') AS active_protocols
      FROM vpn_accounts a
      LEFT JOIN vpn_client_profiles cp ON cp.vpn_account_id = a.id
      LEFT JOIN vpn_account_protocols pap ON pap.vpn_account_id = a.id
      GROUP BY a.id, cp.protocol, a.created_at
      ORDER BY a.created_at ASC, a.id ASC
    )
    SELECT row_number() OVER (), primary_protocol, desired_protocols, active_protocols
    FROM account_state
    LIMIT 20
  " 2>/dev/null || true)

  if [[ -z "$protocol_rows" ]]; then
    log "protocol-activation accounts=none"
    return 0
  fi
  while IFS='|' read -r account_index primary desired active; do
    [[ "$account_index" =~ ^[0-9]+$ ]] || continue
    log "protocol-activation account=${account_index} primary=${primary:-auto} desired=${desired:-none} active=${active:-none}"
  done <<<"$protocol_rows"

  local trigger_state
  trigger_state=$(psql "$ROUTEGATE_DATABASE_URL" -qAt -F '|' -c "
    SELECT
      COALESCE(bool_or(position('vpn_account_protocols' IN p.prosrc) > 0), FALSE),
      (SELECT count(*) FROM pg_trigger t
        WHERE t.tgrelid = 'config_apply_jobs'::regclass
          AND t.tgname = 'config_apply_jobs_mark_version_applied'
          AND t.tgenabled <> 'D')
    FROM pg_proc p
    WHERE p.proname = 'routegate_mark_config_version_applied'
  " 2>/dev/null || true)
  log "protocol-activation trigger-promotes-protocol-sets|trigger-enabled=${trigger_state:-unavailable}"

  # Timestamps are reported as seconds relative to the newest rendered and the
  # newest successfully applied config version of the account's server. The
  # apply trigger promotes a protocol only when its updated_at is not later
  # than the applied version's created_at.
  local timing_rows account_index protocol desired_flag active_flag vs_latest vs_applied latest_status
  timing_rows=$(psql "$ROUTEGATE_DATABASE_URL" -qAt -F '|' -c "
    WITH accounts AS (
      SELECT a.id, a.server_id, row_number() OVER (ORDER BY a.created_at ASC, a.id ASC) AS account_index
      FROM vpn_accounts a
    ),
    latest_version AS (
      SELECT DISTINCT ON (cv.server_id) cv.server_id, cv.created_at, cv.status
      FROM config_versions cv
      ORDER BY cv.server_id, cv.version DESC
    ),
    latest_applied AS (
      SELECT DISTINCT ON (j.server_id) j.server_id, cv.created_at
      FROM config_apply_jobs j
      JOIN config_versions cv ON cv.id = j.config_version_id
      WHERE j.action = 'apply' AND j.status = 'succeeded'
      ORDER BY j.server_id, COALESCE(j.completed_at, j.updated_at) DESC
    )
    SELECT
      acc.account_index,
      pap.protocol,
      pap.desired_enabled,
      pap.active_enabled,
      COALESCE(round(extract(epoch FROM (pap.updated_at - lv.created_at)))::bigint::text, 'none'),
      COALESCE(round(extract(epoch FROM (pap.updated_at - la.created_at)))::bigint::text, 'none'),
      COALESCE(lv.status, 'none')
    FROM accounts acc
    JOIN vpn_account_protocols pap ON pap.vpn_account_id = acc.id
    LEFT JOIN latest_version lv ON lv.server_id = acc.server_id
    LEFT JOIN latest_applied la ON la.server_id = acc.server_id
    WHERE acc.account_index <= 20
      AND pap.desired_enabled IS DISTINCT FROM pap.active_enabled
    ORDER BY acc.account_index, pap.protocol
  " 2>/dev/null || true)
  while IFS='|' read -r account_index protocol desired_flag active_flag vs_latest vs_applied latest_status; do
    [[ "$account_index" =~ ^[0-9]+$ ]] || continue
    log "protocol-activation pending account=${account_index} protocol=${protocol} desired=${desired_flag} active=${active_flag} updated-minus-latest-version-seconds=${vs_latest} updated-minus-applied-version-seconds=${vs_applied} latest-version-status=${latest_status}"
  done <<<"$timing_rows"
}

network_diagnostics() {
  # Read-only summaries; raw rules, addresses and ban lists stay on the host.
  local tool output port count policy chain
  for tool in nft iptables ip6tables iptables-legacy ip6tables-legacy ufw fail2ban-client; do
    if ! command -v "$tool" >/dev/null 2>&1; then
      log "network tool=${tool} available=false"
      continue
    fi
    case "$tool" in
      nft)
        if output=$(timeout 10 nft list ruleset 2>/dev/null); then
          count=$(printf '%s\n' "$output" | grep -Ec '\b(drop|reject)\b' || true)
          log "network tool=nft readable=true drop-reject-lines=${count}"
          for policy in accept drop; do
            count=$(printf '%s\n' "$output" | grep -Ec "hook input.*policy ${policy}" || true)
            log "network tool=nft input-policy=${policy} chains=${count}"
          done
        else
          log "network tool=nft readable=false"
        fi
        ;;
      iptables|ip6tables|iptables-legacy|ip6tables-legacy)
        if output=$(timeout 10 "$tool" -S 2>/dev/null); then
          count=$(printf '%s\n' "$output" | grep -Ec -- '-j (DROP|REJECT)' || true)
          log "network tool=${tool} readable=true filter-drop-reject-rules=${count}"
          for chain in INPUT FORWARD OUTPUT; do
            policy=$(printf '%s\n' "$output" | awk -v chain="$chain" '$1 == "-P" && $2 == chain && ($3 == "ACCEPT" || $3 == "DROP") { print $3 }')
            log "network tool=${tool} chain=${chain} policy=${policy:-unknown}"
          done
        else
          log "network tool=${tool} readable=false"
        fi
        ;;
      ufw)
        if output=$(timeout 10 ufw status 2>/dev/null); then
          case "$output" in
            *"Status: inactive"*) log "network ufw=inactive" ;;
            *"Status: active"*) log "network ufw=active" ;;
            *) log "network ufw=unknown" ;;
          esac
        else
          log "network ufw=unavailable"
        fi
        ;;
      fail2ban-client)
        if output=$(timeout 10 fail2ban-client status sshd 2>/dev/null); then
          count=$(printf '%s\n' "$output" | sed -n 's/.*Currently banned:[[:space:]]*\([0-9][0-9]*\).*/\1/p')
          log "network fail2ban-sshd=current-bans count=${count:-unknown}"
        else
          log "network fail2ban-sshd=unavailable"
        fi
        ;;
    esac
  done
  for port in 22 443 8443; do
    if output=$(timeout 5 ss -H -ltn "sport = :${port}" 2>/dev/null); then
      count=$(printf '%s\n' "$output" | sed '/^[[:space:]]*$/d' | wc -l | tr -d ' ')
      log "network tcp-port=${port} listeners=${count}"
    else
      log "network tcp-port=${port} listeners=unknown"
    fi
  done
}

diagnose() {
  control_plane_diagnostics
  runtime_diagnostics
  http_diagnostics
  schema_diagnostics
  protocol_activation_diagnostics
  network_diagnostics
}

validate_platform() {
  local service
  for service in postgresql routegate-manager.service routegate-agent.service nginx; do
    systemctl is-active --quiet "$service" || die "required control-plane service is not active: ${service}"
  done

  local local_status public_status
  local_status=$(curl -sS --max-time 10 -o /dev/null -w '%{http_code}' http://127.0.0.1:8080/api/admin/health)
  [[ "$local_status" == 200 ]] || die "manager local health returned ${local_status}"

  public_status=$(curl -sS --max-time 15 -o /dev/null -w '%{http_code}' "$PUBLIC_URL/")
  [[ "$public_status" == 200 ]] || die "public root returned ${public_status}"

  log "validation=passed control-plane=healthy"
  runtime_diagnostics
  schema_diagnostics
}

start_manager_only() {
  # A strict, idempotent Manager-only recovery. No Agent, nginx or VPN restart.
  # Production-like workflow already holds the host deployment flock.
  local unit=routegate-manager.service load_state http_status
  load_state=$(systemctl show --property=LoadState --value "$unit" 2>/dev/null || true)
  [[ "$load_state" == loaded ]] || die "Manager unit is missing or not loaded"
  if systemctl is-active --quiet "$unit"; then
    log "manager-start=already-active"
  else
    log "manager-start=starting"
    systemctl start "$unit" || die "Manager service start failed"
  fi
  for _ in $(seq 1 20); do
    if systemctl is-active --quiet "$unit"; then
      http_status=$(curl -sS --max-time 5 -o /dev/null -w '%{http_code}' http://127.0.0.1:8080/api/admin/health 2>/dev/null || true)
      if [[ "$http_status" == 200 ]]; then
        log "manager-start=passed active=true local-health=200"
        return 0
      fi
    fi
    sleep 1
  done
  die "Manager did not become active with local health=200"
}

restart_control_plane() {
  if [[ -x /usr/local/sbin/routegate-recovery ]]; then
    /usr/local/sbin/routegate-recovery restart-services
  elif [[ -x /usr/local/bin/routegate-recovery ]]; then
    /usr/local/bin/routegate-recovery restart-services
  else
    die "routegate-recovery is not installed"
  fi
  validate_platform
}

restart_runtime() {
  local label=$1
  local service=$2
  local load_state

  load_state=$(systemctl show --property=LoadState --value "$service" 2>/dev/null || true)
  [[ "$load_state" == loaded ]] || die "runtime is not installed or unmanaged: ${label}"

  if [[ "$label" == sing-box ]]; then
    command -v sing-box >/dev/null 2>&1 || die "sing-box binary is unavailable"
    [[ -r /etc/sing-box/config.json ]] || die "sing-box config is unavailable"
    sing-box check -c /etc/sing-box/config.json >/dev/null || die "sing-box config validation failed"
  fi

  systemctl restart "$service"
  for _ in $(seq 1 20); do
    systemctl is-active --quiet "$service" && break
    sleep 1
  done
  systemctl is-active --quiet "$service" || die "runtime failed to become active: ${label}"
  log "runtime-restart=passed runtime=${label} service=${service}"
  runtime_diagnostics
}

reconcile_mtproto_port_conflict() {
  local config_file=/etc/routegate-mtproto/config.toml
  local backup_file current_line current_port row vless_port shadowsocks_port target_port server_count tmp_file

  [[ -r /etc/routegate/manager.env ]] || die "manager environment is unavailable"
  [[ -r "$config_file" ]] || die "managed MTProto config is unavailable"
  command -v psql >/dev/null 2>&1 || die "psql is unavailable"

  set -a
  # shellcheck disable=SC1091
  source /etc/routegate/manager.env
  set +a
  [[ -n ${ROUTEGATE_DATABASE_URL:-} ]] || die "database URL is unavailable"

  server_count=$(psql "$ROUTEGATE_DATABASE_URL" -qAtc 'SELECT count(*) FROM servers' 2>/dev/null || true)
  [[ "$server_count" == 1 ]] || die "production-like port reconciliation requires exactly one managed server"

  row=$(psql "$ROUTEGATE_DATABASE_URL" -qAt -F '|' -c 'SELECT vless_port, shadowsocks_port, mtproto_port FROM servers LIMIT 1' 2>/dev/null || true)
  IFS='|' read -r vless_port shadowsocks_port target_port <<<"$row"
  [[ "$vless_port" =~ ^[0-9]+$ && "$shadowsocks_port" =~ ^[0-9]+$ && "$target_port" =~ ^[0-9]+$ ]] || die "database listener ports are unavailable"
  [[ "$target_port" != "$vless_port" && "$target_port" != "$shadowsocks_port" ]] || die "database still contains a TCP listener collision"

  current_line=$(grep -E '^bind-to = "0\.0\.0\.0:[0-9]{1,5}"$' "$config_file" || true)
  [[ $(printf '%s\n' "$current_line" | sed '/^$/d' | wc -l | tr -d ' ') == 1 ]] || die "managed MTProto bind-to line is ambiguous"
  current_port=$(printf '%s\n' "$current_line" | grep -Eo '[0-9]{1,5}' | tail -n 1)
  [[ "$current_port" =~ ^[0-9]+$ ]] || die "managed MTProto port is invalid"

  if [[ "$current_port" != "$target_port" ]]; then
    backup_file="${config_file}.routegate-port-reconcile.$(date -u +%Y%m%dT%H%M%SZ).bak"
    cp -a "$config_file" "$backup_file"
    tmp_file=$(mktemp /etc/routegate-mtproto/config.toml.XXXXXX)
    if ! awk -v port="$target_port" '
      BEGIN { replaced = 0 }
      /^bind-to = "0\.0\.0\.0:[0-9]+"$/ {
        print "bind-to = \"0.0.0.0:" port "\""
        replaced = 1
        next
      }
      { print }
      END { if (replaced != 1) exit 42 }
    ' "$config_file" >"$tmp_file"; then
      rm -f "$tmp_file"
      die "failed to render reconciled MTProto config"
    fi
    chmod --reference="$config_file" "$tmp_file"
    chown --reference="$config_file" "$tmp_file"
    mv "$tmp_file" "$config_file"

    if ! systemctl restart routegate-mtproto.service; then
      cp -a "$backup_file" "$config_file"
      systemctl restart routegate-mtproto.service || true
      die "MTProto failed after port reconciliation; previous config restored"
    fi
    for _ in $(seq 1 20); do
      systemctl is-active --quiet routegate-mtproto.service && break
      sleep 1
    done
    if ! systemctl is-active --quiet routegate-mtproto.service; then
      cp -a "$backup_file" "$config_file"
      systemctl restart routegate-mtproto.service || true
      die "MTProto did not become active after port reconciliation; previous config restored"
    fi
    log "mtproto-port-reconcile=passed old-port=${current_port} new-port=${target_port}"
  else
    log "mtproto-port-reconcile=already-current port=${target_port}"
  fi

  command -v sing-box >/dev/null 2>&1 || die "sing-box binary is unavailable"
  [[ -r /etc/sing-box/config.json ]] || die "sing-box config is unavailable"
  sing-box check -c /etc/sing-box/config.json >/dev/null || die "sing-box config validation failed"
  systemctl restart sing-box.service
  for _ in $(seq 1 20); do
    systemctl is-active --quiet sing-box.service && break
    sleep 1
  done
  systemctl is-active --quiet sing-box.service || die "sing-box did not become active after MTProto port reconciliation"
  log "sing-box-recovery=passed"
  runtime_diagnostics
  validate_platform
}

renew_certificate() {
  if [[ -x /usr/local/sbin/routegate-recovery ]]; then
    /usr/local/sbin/routegate-recovery renew-certificate
  elif [[ -x /usr/local/bin/routegate-recovery ]]; then
    /usr/local/bin/routegate-recovery renew-certificate
  else
    die "routegate-recovery is not installed"
  fi
  log "certificate-renewal=passed"
}

main() {
  require_root
  case "$OPERATION" in
    diagnose)
      diagnose
      ;;
    diagnose-sing-box)
      sing_box_diagnostics
      ;;
    validate)
      validate_platform
      ;;
    restart-control-plane)
      restart_control_plane
      ;;
    start-manager-only)
      start_manager_only
      ;;
    restart-sing-box)
      restart_runtime sing-box sing-box.service
      ;;
    restart-wireguard)
      restart_runtime wireguard wg-quick@routegate-wg0.service
      ;;
    restart-hysteria2)
      restart_runtime hysteria2 hysteria-server.service
      ;;
    restart-mtproto)
      restart_runtime mtproto routegate-mtproto.service
      ;;
    reconcile-mtproto-port-conflict)
      reconcile_mtproto_port_conflict
      ;;
    renew-certificate)
      renew_certificate
      ;;
    *)
      die "unsupported operation"
      ;;
  esac
}

main "$@"
