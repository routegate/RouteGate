#!/usr/bin/env python3
"""Approved US Agent binary repair. No config apply, transfer write or VPN restart."""
import fcntl
import hashlib
import os
import pathlib
import re
import shutil
import subprocess
import sys
import time

COMMIT = '1fd2792d203a315022240d4358638b95f661eeef'
AGENT = pathlib.Path('/usr/local/bin/routegate-agent')
BACKUPS = pathlib.Path('/root/routegate-backups')
PROTECTED = [pathlib.Path(p) for p in (
    '/usr/local/bin/routegate-manager', '/etc/routegate',
    '/etc/systemd/system/routegate-agent.service',
    '/etc/systemd/system/routegate-manager.service', '/opt/routegate-manager/migrations',
    '/var/www/routegate', '/etc/nginx', '/etc/sing-box', '/etc/wireguard',
    '/etc/hysteria', '/etc/routegate-mtproto',
)]
SERVICES = ['routegate-manager', 'sing-box', 'wg-quick@routegate-wg0',
            'hysteria-server', 'routegate-mtproto', 'nginx']

class Stop(Exception):
    pass

def command(args):
    p = subprocess.run(args, capture_output=True, text=True)
    if p.returncode:
        raise Stop('command_failed (private details suppressed)')
    return p.stdout.strip()

def sql(statement):
    env = os.environ.copy()
    env['PGOPTIONS'] = '-c default_transaction_read_only=on -c statement_timeout=10s -c lock_timeout=3s'
    p = subprocess.run(['psql', os.environ['ROUTEGATE_DATABASE_URL'], '-X', '-qAt',
                        '-v', 'ON_ERROR_STOP=1', '-c', statement],
                       env=env, capture_output=True, text=True)
    if p.returncode:
        raise Stop('database_check_failed (private details suppressed)')
    return p.stdout.strip()

def digest(path):
    with path.open('rb') as f:
        return hashlib.file_digest(f, 'sha256').hexdigest()

def files():
    out = {}
    for root in PROTECTED:
        paths = [root] if root.is_file() else sorted(root.rglob('*')) if root.is_dir() else []
        for path in paths:
            if path.is_symlink():
                out[str(path)] = ('symlink', os.readlink(path))
            elif path.is_file():
                stat = path.stat()
                out[str(path)] = (digest(path), stat.st_mode, stat.st_uid, stat.st_gid)
    return out

def services():
    return {name: command(['systemctl', 'show', name, '-p', 'MainPID', '-p', 'ActiveState',
                          '-p', 'ActiveEnterTimestampMonotonic']) for name in SERVICES}

def identities():
    return sql("SELECT md5(jsonb_build_object('accounts',(SELECT jsonb_agg(jsonb_build_array(id,server_id,status,vless_uuid) ORDER BY id) FROM vpn_accounts),'tokens',(SELECT jsonb_agg(to_jsonb(t)-ARRAY['last_used_at','updated_at'] ORDER BY id) FROM vpn_subscription_tokens t),'devices',(SELECT jsonb_agg(to_jsonb(d)-ARRAY['last_used_at','last_seen_at','updated_at'] ORDER BY id) FROM vpn_account_devices d))::text)")

def scope():
    # The exact blocked operation must remain on FI without client acknowledgement.
    value = sql("SELECT count(*)=1 FROM vpn_account_transfers t JOIN vpn_accounts a ON a.id=t.vpn_account_id WHERE t.id='942b0929-d632-46c8-a2f9-e3cbbdc50cef' AND t.vpn_account_id='83ef4f8b-78a6-474b-8dd7-c32c1b71fe66' AND t.source_server_id='da1b3f03-b017-4170-8da6-dc4314e5d7ad' AND t.target_server_id='04a9d5db-1dfe-461a-8ddd-fe94a0480a0b' AND a.server_id=t.source_server_id AND t.state='target_applying' AND t.cutover_at IS NULL AND t.cleanup_acknowledged_at IS NULL AND t.completed_at IS NULL")
    if value != 't':
        raise Stop('blocked_canary_scope_changed')
    if sql("SELECT NOT EXISTS(SELECT 1 FROM config_apply_jobs WHERE status IN ('pending','in_progress')) AND NOT EXISTS(SELECT 1 FROM agent_operation_jobs WHERE status IN ('pending','in_progress')) AND NOT EXISTS(SELECT 1 FROM agent_platform_update_jobs WHERE status IN ('pending','in_progress'))") != 't':
        raise Stop('agent_work_in_progress')

def activate(source):
    temporary = AGENT.with_name('routegate-agent.rg140-new')
    shutil.copyfile(source, temporary)
    os.chmod(temporary, 0o755)
    os.replace(temporary, AGENT)

def repair(candidate, checksum, commit, *, inherited=False):
    lock_path = os.environ.get('RG_UPDATE_LOCK_FILE') or '/run/lock/routegate-update.lock'
    if inherited:
        if not os.path.samestat(os.fstat(8), os.stat(lock_path)):
            raise Stop('invalid_inherited_host_lock')
        lock = os.fdopen(os.dup(8), 'ab')
    else:
        lock = open(lock_path, 'ab')
    with lock:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            raise Stop('host_update_lock_unavailable') from None
        repair_locked(candidate, checksum, commit)


def repair_locked(candidate, checksum, commit):
    if commit != COMMIT or not re.fullmatch('[0-9a-f]{64}', checksum):
        raise Stop('unapproved_candidate')
    if candidate.is_symlink() or not candidate.is_file() or digest(candidate) != checksum:
        raise Stop('candidate_checksum_mismatch')
    if not AGENT.is_file() or AGENT.is_symlink():
        raise Stop('agent_layout_unavailable')
    scope()
    baseline_files, baseline_services, baseline_ids = files(), services(), identities()
    command(['systemctl', 'is-active', '--quiet', 'routegate-agent'])
    heartbeat = sql("SELECT last_authenticated_heartbeat_at::text FROM agents WHERE id='dcf2c834-8a0b-4406-b095-71fb15ab0171'")
    backup = BACKUPS / ('rg140-us-agent-' + COMMIT + '-' + time.strftime('%Y%m%dT%H%M%SZ', time.gmtime()))
    backup.mkdir(mode=0o700, parents=True)
    old = backup / 'routegate-agent'
    shutil.copy2(AGENT, old)
    stopped = False
    try:
        stopped = True
        command(['systemctl', 'stop', 'routegate-agent'])
        # Re-check after stopping the only possible task consumer.
        scope()
        activate(candidate)
        command(['systemctl', 'start', 'routegate-agent'])
        fresh = False
        for _ in range(60):
            if sql("SELECT last_authenticated_heartbeat_at>" + "'" + heartbeat + "'::timestamptz FROM agents WHERE id='dcf2c834-8a0b-4406-b095-71fb15ab0171'") == 't':
                fresh = True
                break
            time.sleep(1)
        if not fresh:
            raise Stop('fresh_authenticated_heartbeat_missing')
        command(['systemctl', 'is-active', '--quiet', 'routegate-agent'])
        scope()
        if files() != baseline_files or services() != baseline_services or identities() != baseline_ids:
            raise Stop('protected_baseline_changed')
        if digest(AGENT) != checksum:
            raise Stop('installed_checksum_mismatch')
    except BaseException:
        if stopped:
            command(['systemctl', 'stop', 'routegate-agent'])
            activate(old)
            command(['systemctl', 'start', 'routegate-agent'])
            print('agent_rollback=restored_previous_binary')
        raise
    print('agent_commit=' + COMMIT)
    print('agent_binary_checksum=verified')
    print('authenticated_heartbeat=fresh')
    print('manager_vpn_configs_services_accounts_tokens_devices=preserved')
    print('fi_test=still_on_FI; no_cutover_or_cleanup')
    print('agent_backup=' + str(backup))

if __name__ == '__main__':
    try:
        args = sys.argv[1:]
        inherited = args[:1] == ['--inherit-host-lock']
        if inherited:
            args = args[1:]
        candidate, checksum, commit = args
        repair(pathlib.Path(candidate), checksum, commit, inherited=inherited)
    except Exception as e:
        print('AGENT_REPAIR_BLOCKED: ' + (str(e) if isinstance(e, Stop) else 'private_details_suppressed'))
        sys.exit(1)
