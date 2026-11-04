#!/usr/bin/env python3
"""Scoped US traffic rollout. Never prints configs, credentials, or API responses."""
import copy
import hashlib
import json
import os
from pathlib import Path
import re
import socket
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request
import urllib.parse
import uuid

CANDIDATE = '58485650c42a394ae1305fdff1a7896e2f3c1b93'
SCHEMA = '000160_traffic_report_checkpoints'
OLD_SCHEMA = '000159_staged_account_transfers'
CONFIG = Path('/etc/routegate/agent.yaml')
MIGRATION = Path('/opt/routegate-manager/migrations/' + SCHEMA + '.up.sql')
MANAGER = Path('/usr/local/bin/routegate-manager')
AGENT = Path('/usr/local/bin/routegate-agent')
RUNTIME_SERVICES = ('sing-box', 'wg-quick@routegate-wg0', 'hysteria-server', 'routegate-mtproto', 'nginx')
PROTECTED = ('/etc/sing-box', '/etc/wireguard', '/etc/hysteria', '/etc/routegate-mtproto',
             '/etc/nginx', '/var/www/routegate', '/etc/routegate/manager.env',
             '/etc/systemd/system/routegate-manager.service', '/etc/systemd/system/routegate-agent.service')

class Stop(Exception):
    pass

def run(args, env=None):
    result = subprocess.run(args, capture_output=True, text=True, timeout=120, env=env)
    if result.returncode:
        raise Stop('command_failed_' + Path(args[0]).name + '_private_output_suppressed')
    return result.stdout.strip()

def database_environment(value):
    parsed = urllib.parse.urlsplit(value)
    if parsed.scheme not in ('postgres', 'postgresql') or not parsed.hostname or not parsed.username or not parsed.path.strip('/'):
        raise Stop('unsupported_manager_database_url')
    query = urllib.parse.parse_qs(parsed.query, strict_parsing=True)
    ssl = {'sslmode': 'PGSSLMODE', 'sslrootcert': 'PGSSLROOTCERT', 'sslcert': 'PGSSLCERT', 'sslkey': 'PGSSLKEY'}
    if set(query) - set(ssl) - {'connect_timeout', 'application_name'} or any(len(values) != 1 for values in query.values()):
        raise Stop('unsupported_manager_database_url_options')
    env = {key: value for key, value in os.environ.items() if key in ('PATH', 'HOME', 'LANG', 'LC_ALL', 'TZ')}
    env.update(PGHOST=urllib.parse.unquote(parsed.hostname), PGPORT=str(parsed.port or 5432),
               PGUSER=urllib.parse.unquote(parsed.username), PGDATABASE=urllib.parse.unquote(parsed.path[1:]),
               PGCONNECT_TIMEOUT='10', PGAPPNAME='routegate-traffic-rollout')
    if parsed.password is not None:
        env['PGPASSWORD'] = urllib.parse.unquote(parsed.password)
    for option, name in ssl.items():
        if option in query:
            env[name] = query[option][0]
    env['PGOPTIONS'] = '-c default_transaction_read_only=on -c statement_timeout=10s -c lock_timeout=3s'
    return env

def sql(query):
    # libpq does not expand a URI stored in the PGDATABASE environment value.
    # Supply decoded parameters separately, keeping credentials out of argv.
    env = database_environment(os.environ['ROUTEGATE_DATABASE_URL'])
    return run(['psql', '-X', '-qAt', '-v', 'ON_ERROR_STOP=1', '-c', query], env)

def config_values(data):
    result = {}
    for line in data.decode().splitlines():
        if not line.strip() or line.lstrip().startswith('#') or ':' not in line:
            continue
        key, value = line.split(':', 1)
        value = value.strip()
        if value.startswith('"'):
            value = json.loads(value)
        else:
            value = value.strip("'")
        if key.strip() in result:
            raise Stop('duplicate_agent_config_key')
        result[key.strip()] = value
    return result

def bootstrap_metadata(data):
    values = {}
    for field in ('AgentBundleBaseURL', 'AgentInstallerSHA256'):
        matches = re.findall(rb'github\.com/ikaevus/routegate/backend/internal/buildinfo\.' + field.encode() + rb'=([^\s"\x00]+)', data)
        if len(set(matches)) != 1:
            raise Stop('existing_bootstrap_build_metadata_unavailable')
        values[field] = matches[0].decode('ascii')
    if not re.fullmatch(r'https://us\.routegate\.org/bootstrap/[0-9a-f]{40}', values['AgentBundleBaseURL']):
        raise Stop('unexpected_existing_bootstrap_base_url')
    if not re.fullmatch(r'[0-9a-f]{64}', values['AgentInstallerSHA256']):
        raise Stop('unexpected_existing_installer_digest')
    return values

def enabled_config(data):
    config_values(data)  # Refuse ambiguous input before modifying it.
    text = data.decode()
    pattern = r'(?m)^traffic_collection_enabled\s*:.*$'
    if re.search(pattern, text):
        text = re.sub(pattern, 'traffic_collection_enabled: true', text)
    else:
        text = text.rstrip() + '\ntraffic_collection_enabled: true\n'
    return text.encode()

def atomic(path, data, mode):
    fd, name = tempfile.mkstemp(prefix='.traffic-rollout-', dir=path.parent)
    try:
        with os.fdopen(fd, 'wb') as stream:
            os.fchmod(stream.fileno(), mode)
            stream.write(data)
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(name, path)
        directory = os.open(path.parent, os.O_RDONLY | os.O_DIRECTORY)
        try:
            os.fsync(directory)
        finally:
            os.close(directory)
    finally:
        if os.path.exists(name):
            os.unlink(name)

def snapshot(paths=PROTECTED, services=RUNTIME_SERVICES):
    files = {}
    for name in paths:
        root = Path(name)
        entries = [root] if root.is_file() else sorted(root.rglob('*')) if root.is_dir() else []
        for path in entries:
            if path.is_symlink():
                files[str(path)] = ('symlink', os.readlink(path))
            elif path.is_file():
                files[str(path)] = hashlib.sha256(path.read_bytes()).hexdigest()
    states = {name: run(['systemctl', 'show', '-p', 'ActiveState', '-p', 'MainPID', '--value', name]) for name in services}
    return files, states

def api(path, payload=None, token=None):
    headers = {'Content-Type': 'application/json'}
    if token:
        headers['Authorization'] = 'Bearer ' + token
    data = json.dumps(payload).encode() if payload is not None else None
    request = urllib.request.Request('http://127.0.0.1:8080' + path, data=data, headers=headers)
    # Do not inherit proxy configuration for privileged loopback requests.
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    try:
        with opener.open(request, timeout=20) as response:
            return json.load(response)
    except urllib.error.HTTPError as error:
        raise Stop('manager_api_http_' + str(error.code)) from None
    except (urllib.error.URLError, json.JSONDecodeError):
        raise Stop('manager_api_request_failed_private_response_suppressed') from None

def login():
    name = os.environ.get('ROUTEGATE_BOOTSTRAP_ADMIN_USERNAME') or os.environ.get('ROUTEGATE_BOOTSTRAP_ADMIN_EMAIL')
    password = os.environ.get('ROUTEGATE_BOOTSTRAP_ADMIN_PASSWORD')
    if not name or not password:
        raise Stop('admin_credentials_unavailable_use_authenticated_UI_for_config_apply')
    try:
        response = api('/api/v1/auth/login', {'login': name, 'password': password})
    except Stop as error:
        if str(error) == 'manager_api_http_401':
            raise Stop('admin_authentication_failed_use_authenticated_UI') from None
        raise
    token = response.get('token')
    if not token:
        raise Stop('admin_authentication_failed_use_authenticated_UI')
    return token

def wait_manager():
    for _ in range(30):
        try:
            api('/api/admin/health')
            return
        except Stop:
            time.sleep(1)
    raise Stop('manager_health_timeout')

def inspect():
    values = config_values(CONFIG.read_bytes())
    agent_id = str(uuid.UUID(values['agent_id']))
    server_id = str(uuid.UUID(values['server_id']))
    if sql("SELECT count(*) FROM agents a JOIN servers s ON s.id=a.server_id WHERE a.id='" + agent_id + "'::uuid AND s.id='" + server_id + "'::uuid AND (lower(s.hostname)='us.routegate.org' OR lower(s.name)='us.routegate.org')") != '1':
        raise Stop('local_agent_us_server_binding_not_verified')
    schema = sql('SELECT max(version) FROM schema_migrations')
    if schema not in (OLD_SCHEMA, SCHEMA):
        raise Stop('unexpected_database_schema')
    if sql('SELECT count(*) FROM vpn_account_transfers WHERE completed_at IS NULL') != '0':
        raise Stop('unfinished_transfer_refused')
    if sql("SELECT count(*) FROM config_apply_jobs WHERE status IN ('pending','in_progress')") != '0':
        raise Stop('unfinished_config_apply_refused')
    binary = values.get('sing_box_path', '/usr/local/bin/sing-box')
    tags = run([binary, 'version'])
    supported = 'with_v2ray_api' in re.split(r'[\s,]+', tags)
    active = Path(values.get('active_config_path', '/etc/sing-box/config.json'))
    config = json.loads(active.read_bytes())
    configured = config.get('experimental', {}).get('v2ray_api', {})
    print(json.dumps({'schema': schema, 'runtime_stats_supported': supported,
                      'agent_collection_enabled': values.get('traffic_collection_enabled') == 'true',
                      'runtime_stats_enabled': configured.get('stats', {}).get('enabled') is True}), flush=True)
    print(json.dumps({'preserve_bootstrap_metadata': bootstrap_metadata(MANAGER.read_bytes())}), flush=True)
    if not supported:
        raise Stop('installed_sing_box_missing_v2ray_api_support')
    if configured.get('listen') != '127.0.0.1:18085':
        try:
            with socket.socket() as probe:
                probe.bind(('127.0.0.1', 18085))
        except OSError:
            raise Stop('statistics_loopback_port_already_in_use') from None
    return values, agent_id, server_id

def pending_transfer_evidence(server_id, expected_count):
    """Fixed, bounded read-only RG-140 stage evidence without account identifiers.

    An unfinished transfer may reserve the US node and must not be silently
    treated as an unrelated configuration deployment. No output includes UUIDs,
    device/subscription material, actors, or free-form failure messages.
    """
    raw = sql(
        "SELECT COALESCE(jsonb_agg(jsonb_build_object("
        "'state', t.state, "
        "'us_node_role', CASE WHEN t.source_server_id='" + server_id + "'::uuid THEN 'source' "
        "WHEN t.target_server_id='" + server_id + "'::uuid THEN 'target' ELSE 'unrelated' END, "
        "'placement', CASE WHEN a.server_id=t.source_server_id THEN 'source' "
        "WHEN a.server_id=t.target_server_id THEN 'target' ELSE 'other' END, "
        "'cutover_recorded', t.cutover_at IS NOT NULL, "
        "'cleanup_acknowledged', t.cleanup_acknowledged_at IS NOT NULL, "
        "'target_job_status', COALESCE(target_job.status,'absent'), "
        "'cleanup_job_status', COALESCE(cleanup_job.status,'absent'), "
        "'has_error', t.last_error<>'' "
        ") ORDER BY t.created_at, t.id), '[]'::jsonb) "
        "FROM vpn_account_transfers t "
        "JOIN vpn_accounts a ON a.id=t.vpn_account_id "
        "LEFT JOIN config_apply_jobs target_job ON target_job.id=t.target_job_id "
        "LEFT JOIN config_apply_jobs cleanup_job ON cleanup_job.id=t.cleanup_job_id "
        "WHERE t.completed_at IS NULL"
    )
    try:
        states = json.loads(raw)
    except (TypeError, ValueError):
        raise Stop('transfer_evidence_query_invalid') from None
    if not isinstance(states, list) or len(states) != expected_count or len(states) > 16:
        raise Stop('transfer_evidence_count_mismatch_or_unbounded')
    allowed_states = {
        'preparing', 'target_applying', 'target_ready',
        'client_refresh_pending', 'source_cleaning', 'target_cleaning',
    }
    allowed_jobs = {'absent', 'pending', 'in_progress', 'succeeded', 'failed'}
    for item in states:
        if not isinstance(item, dict) or set(item) != {
            'state', 'us_node_role', 'placement', 'cutover_recorded',
            'cleanup_acknowledged', 'target_job_status',
            'cleanup_job_status', 'has_error',
        }:
            raise Stop('transfer_evidence_shape_invalid')
        if item['state'] not in allowed_states or item['us_node_role'] not in ('source', 'target', 'unrelated') or item['placement'] not in ('source', 'target', 'other'):
            raise Stop('transfer_evidence_value_invalid')
        if item['target_job_status'] not in allowed_jobs or item['cleanup_job_status'] not in allowed_jobs:
            raise Stop('transfer_job_status_invalid')
        if not all(type(item[key]) is bool for key in ('cutover_recorded', 'cleanup_acknowledged', 'has_error')):
            raise Stop('transfer_evidence_flag_invalid')
    return states


def diagnose():
    """Read-only US accounting evidence. No binary metadata scraping or port binding.

    Inspect must work before the statistics-enabled Manager/Agent are installed:
    missing build tags, unavailable counter config, and empty rollups are
    diagnostic findings, not reasons to refuse a read-only inspection.
    """
    values = config_values(CONFIG.read_bytes())
    agent_id = str(uuid.UUID(values['agent_id']))
    server_id = str(uuid.UUID(values['server_id']))
    binding = sql(
        "SELECT count(*) FROM agents a JOIN servers s ON s.id=a.server_id "
        "WHERE a.id='" + agent_id + "'::uuid AND s.id='" + server_id + "'::uuid "
        "AND (lower(s.hostname)='us.routegate.org' OR lower(s.name)='us.routegate.org')"
    )
    if binding != '1':
        raise Stop('local_agent_us_server_binding_not_verified')

    schema = sql('SELECT max(version) FROM schema_migrations')
    if schema not in (OLD_SCHEMA, SCHEMA):
        raise Stop('unexpected_database_schema')
    transfers = int(sql('SELECT count(*) FROM vpn_account_transfers WHERE completed_at IS NULL'))
    transfer_evidence = pending_transfer_evidence(server_id, transfers)
    reservations = int(sql('SELECT count(*) FROM vpn_account_transfer_nodes'))
    apply_jobs = int(sql("SELECT count(*) FROM config_apply_jobs WHERE status IN ('pending','in_progress')"))

    # Only fixed aggregate SQL and allow-listed transfer status evidence.
    # Never print account identities, connection details, runtime config,
    # bearer tokens, actors, or usage-event payloads.
    recent_native = int(sql(
        "SELECT count(*) FROM traffic_usage_events "
        "WHERE server_id='" + server_id + "'::uuid "
        "AND metadata->>'source'='sing-box-v2ray-api' "
        "AND reported_at >= now() - interval '30 days'"
    ))
    recent_daily = int(sql(
        "SELECT count(*) FROM traffic_usage_daily "
        "WHERE usage_date >= (now() AT TIME ZONE 'UTC')::date - 29 "
        "AND usage_date <= (now() AT TIME ZONE 'UTC')::date"
    ))

    runtime_path = values.get('sing_box_path', '/usr/local/bin/sing-box')
    try:
        version = run([runtime_path, 'version'])
    except (Stop, OSError):
        runtime_supported = None
    else:
        runtime_supported = 'with_v2ray_api' in re.split(r'[\s,]+', version)

    active = Path(values.get('active_config_path', '/etc/sing-box/config.json'))
    try:
        active_config = json.loads(active.read_bytes())
        stats = active_config.get('experimental', {}).get('v2ray_api', {})
        stats_enabled = stats.get('stats', {}).get('enabled') is True
        stats_loopback = stats.get('listen') == '127.0.0.1:18085'
    except (OSError, ValueError, AttributeError):
        stats_enabled = None
        stats_loopback = None

    report = {
        'mode': 'read_only',
        'schema': schema,
        'schema160_ready': schema == SCHEMA,
        'unfinished_transfers': transfers,
        'pending_transfer_evidence': transfer_evidence,
        'transfer_node_reservations': reservations,
        'unfinished_apply_jobs': apply_jobs,
        'runtime_stats_supported': runtime_supported,
        'agent_collection_enabled': values.get('traffic_collection_enabled') == 'true',
        'runtime_stats_enabled': stats_enabled,
        'runtime_stats_loopback_configured': stats_loopback,
        'recent_native_reports_30d': recent_native,
        'dashboard_daily_rows_30d': recent_daily,
        'dashboard_daily_available_from_database': recent_daily > 0,
        'live_api_not_checked': True,
        'no_service_or_config_changes': True,
    }
    print(json.dumps(report, sort_keys=True), flush=True)
    return report

def install_files(files, backup, health=wait_manager, protected=snapshot, command=run, recover_health=wait_manager):
    before = protected()
    old = {path: path.read_bytes() if path.exists() else None for path in files}
    modes = {path: path.stat().st_mode & 0o777 if path.exists() else 0o644 for path in files}
    backup.mkdir(mode=0o700)
    for index, (path, content) in enumerate(old.items()):
        if content is not None:
            atomic(backup / str(index), content, 0o600)
    atomic(backup / 'manifest.json', json.dumps({str(index): str(path) for index, path in enumerate(old)}).encode(), 0o600)
    try:
        command(['systemctl', 'stop', 'routegate-agent'])
        command(['systemctl', 'stop', 'routegate-manager'])
        for path, content in files.items():
            atomic(path, content, modes[path])
        command(['systemctl', 'start', 'routegate-manager'])
        health()
        command(['systemctl', 'start', 'routegate-agent'])
        command(['systemctl', 'is-active', '--quiet', 'routegate-agent'])
        if protected() != before:
            raise Stop('protected_runtime_files_or_processes_changed')
    except BaseException:
        # No down migration or database restore: checkpoint migration is additive.
        # Preserve it if startup applied it, so reported usage cannot be erased.
        command(['systemctl', 'stop', 'routegate-agent'])
        command(['systemctl', 'stop', 'routegate-manager'])
        for path, content in old.items():
            if path == MIGRATION:
                continue
            if content is not None:
                atomic(path, content, modes[path])
        command(['systemctl', 'start', 'routegate-manager'])
        recover_health()
        command(['systemctl', 'start', 'routegate-agent'])
        print('control_plane=previous_binaries_and_agent_config_restored; additive_schema_retained', flush=True)
        raise

def install(stage, run_id):
    values, _, _ = inspect()
    manifest = json.loads((stage / 'manifest.json').read_bytes())
    if manifest.get('commit') != CANDIDATE:
        raise Stop('candidate_mismatch')
    if manifest.get('bootstrapMetadata') != bootstrap_metadata(MANAGER.read_bytes()):
        raise Stop('bootstrap_metadata_change_refused')
    expected = {'routegate-manager', 'routegate-agent', SCHEMA + '.up.sql'}
    if set(manifest.get('files', {})) != expected:
        raise Stop('unexpected_payload_files')
    contents = {}
    for name, digest in manifest['files'].items():
        path = stage / name
        if path.is_symlink() or hashlib.sha256(path.read_bytes()).hexdigest() != digest:
            raise Stop('candidate_digest_mismatch')
        contents[name] = path.read_bytes()
    if MIGRATION.exists() and MIGRATION.read_bytes() != contents[SCHEMA + '.up.sql']:
        raise Stop('existing_checkpoint_migration_differs_from_candidate')
    files = {MANAGER: contents['routegate-manager'], AGENT: contents['routegate-agent'],
             MIGRATION: contents[SCHEMA + '.up.sql'], CONFIG: enabled_config(CONFIG.read_bytes())}
    backup = Path('/root/routegate-backups') / ('traffic-' + CANDIDATE + '-' + run_id)
    def verified_health():
        wait_manager()
        if sql('SELECT max(version) FROM schema_migrations') != SCHEMA:
            raise Stop('checkpoint_schema_not_applied')
    install_files(files, backup, health=verified_health)
    print('control_plane=updated; runtime_config_apply=not_requested; backup=' + str(backup), flush=True)

def comparable_runtime(envelope):
    result = {key: copy.deepcopy(envelope.get(key)) for key in ('singBox', 'wireGuard', 'hysteria2', 'mtproto')}
    experimental = (result.get('singBox') or {}).get('experimental', {})
    experimental.pop('v2ray_api', None)
    if not experimental:
        (result.get('singBox') or {}).pop('experimental', None)
    return result

def apply_metrics():
    values, agent_id, server_id = inspect()
    if sql('SELECT max(version) FROM schema_migrations') != SCHEMA:
        raise Stop('install_control_plane_first')
    if values.get('traffic_collection_enabled') != 'true':
        raise Stop('agent_collection_not_enabled')
    # Wait for the new Agent's authenticated heartbeat, not a synthetic DB flag.
    for _ in range(30):
        ready = sql("SELECT count(*) FROM agents WHERE id='" + agent_id + "'::uuid AND capabilities->'trafficAccounting'->>'enabled'='true' AND capabilities->'vpnCore'->>'v2rayAPI'='true'")
        if ready == '1':
            break
        time.sleep(2)
    else:
        raise Stop('compatible_agent_heartbeat_missing')
    token = login()
    prefix = '/api/v1/servers/' + server_id + '/config'
    versions = api(prefix + '/versions', token=token)
    previous_id = str(uuid.UUID(versions['currentConfigVersionId']))
    rendered = api(prefix + '/render', {'comment': 'Enable verified native traffic accounting'}, token)
    version_id = str(uuid.UUID(rendered['configVersion']['id']))
    previous = json.loads(sql("SELECT rendered_config FROM config_versions WHERE id='" + previous_id + "'::uuid AND server_id='" + server_id + "'::uuid"))
    candidate = json.loads(sql("SELECT rendered_config FROM config_versions WHERE id='" + version_id + "'::uuid AND server_id='" + server_id + "'::uuid"))
    if comparable_runtime(previous) != comparable_runtime(candidate):
        raise Stop('additional_runtime_changes_refused_review_rendered_version_in_UI')
    active = json.loads(Path(values.get('active_config_path', '/etc/sing-box/config.json')).read_bytes())
    if comparable_runtime({'singBox': active})['singBox'] != comparable_runtime(candidate)['singBox']:
        raise Stop('active_runtime_drift_refused')
    stats = candidate.get('singBox', {}).get('experimental', {}).get('v2ray_api', {})
    if stats.get('listen') != '127.0.0.1:18085' or stats.get('stats', {}).get('enabled') is not True:
        raise Stop('rendered_statistics_configuration_missing')
    validated = api(prefix + '/versions/' + version_id + '/validate', {}, token)
    if not validated.get('validationResult', {}).get('valid'):
        raise Stop('rendered_version_invalid')
    fd, name = tempfile.mkstemp(prefix='traffic-runtime-', suffix='.json')
    try:
        with os.fdopen(fd, 'w') as stream:
            json.dump(candidate['singBox'], stream)
        run([values.get('sing_box_path', '/usr/local/bin/sing-box'), 'check', '-c', name])
    finally:
        os.unlink(name)
    response = api(prefix + '/versions/' + version_id + '/apply', {'comment': 'Verified statistics-only runtime change'}, token)
    job_id = str(uuid.UUID(response['job']['id']))
    for _ in range(90):
        job = api(prefix + '/apply-jobs/' + job_id, token=token)
        state = job.get('status', job.get('job', {}).get('status'))
        if state == 'succeeded':
            print('runtime_config_apply=succeeded; statistics_only_change=verified', flush=True)
            for _ in range(60):
                reported = sql("SELECT count(*) FROM traffic_usage_events WHERE agent_id='" + agent_id + "'::uuid AND metadata->>'source'='sing-box-v2ray-api' AND reported_at > now()-interval '5 minutes'")
                overview = api('/api/v1/dashboard/traffic', token=token)
                if reported != '0' and overview.get('dailyAvailable') is True and overview.get('monthlyAvailable') is True:
                    print('native_usage_report=verified; dashboard_daily_and_monthly_available=true', flush=True)
                    return
                time.sleep(2)
            print('runtime_statistics=enabled; dashboard=awaiting_client_traffic', flush=True)
            return
        if state == 'failed':
            raise Stop('runtime_apply_failed_use_existing_config_recovery_UI')
        time.sleep(2)
    raise Stop('runtime_apply_status_unresolved_no_second_job_dispatched')

def main():
    os.umask(0o077)
    if os.geteuid() != 0:
        raise Stop('root_required')
    mode = sys.argv[1]
    if mode != 'inspect':
        raise Stop('install_and_apply_disabled_pending_bootstrap_identity_repair')
    if mode == 'inspect':
        diagnose()
    elif mode == 'install' and len(sys.argv) == 4 and re.fullmatch(r'[0-9]+', sys.argv[3]):
        install(Path(sys.argv[2]), sys.argv[3])
    elif mode == 'apply' and len(sys.argv) == 4 and re.fullmatch(r'[0-9]+', sys.argv[3]):
        stage=Path(sys.argv[2])
        manifest=json.loads((stage/'manifest.json').read_bytes())
        if manifest.get('commit') != CANDIDATE:
            raise Stop('candidate_mismatch')
        for path,name in ((MANAGER,'routegate-manager'),(AGENT,'routegate-agent')):
            if hashlib.sha256(path.read_bytes()).hexdigest()!=manifest['files'].get(name):
                raise Stop('installed_control_plane_not_pinned_candidate')
        apply_metrics()
    else:
        raise Stop('unsupported_operation')

if __name__ == '__main__':
    try:
        main()
    except BaseException as error:
        print('traffic_rollout=stopped; reason=' + (str(error) if isinstance(error, Stop) else type(error).__name__), file=sys.stderr, flush=True)
        sys.exit(1)
