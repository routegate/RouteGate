"""Real flock contention in disposable directories; no host updates."""
import pathlib
import re
import selectors
import subprocess
import tempfile
import unittest

ROOT = pathlib.Path(__file__).resolve().parent

class HostLockTests(unittest.TestCase):
    def test_contention_preserves_inode_and_release_after_process_death(self):
        source = (ROOT / 'routegate-update-transaction.sh').read_text()
        match = re.search(r'^acquire_lock\(\) \{\n.*?^\}', source, re.M | re.S)
        self.assertIsNotNone(match)
        script = 'set -eu\nLOCK_FILE=$1\ndie() { exit 73; }\n' + match.group(0)
        with tempfile.TemporaryDirectory() as tmp:
            lock = pathlib.Path(tmp) / 'transaction.lock'
            lock.write_bytes(b'fixture sentinel\n')
            inode = lock.stat().st_ino
            holder = subprocess.Popen(['bash', '-c', script + '\nacquire_lock\nprintf "ready\\n"\nread -r finish',
                                       'fixture', str(lock)], stdin=subprocess.PIPE, stdout=subprocess.PIPE)
            try:
                with selectors.DefaultSelector() as selector:
                    selector.register(holder.stdout, selectors.EVENT_READ)
                    self.assertTrue(selector.select(5), 'holder did not acquire lock')
                self.assertEqual(holder.stdout.readline(), b'ready\n')
                contender = subprocess.run(['bash', '-c', script + '\nacquire_lock\nprintf admitted',
                                            'fixture', str(lock)], capture_output=True, timeout=5)
                self.assertEqual(contender.returncode, 73, contender.stderr)
                self.assertEqual(contender.stdout, b'')
                self.assertEqual(lock.read_bytes(), b'fixture sentinel\n')
                self.assertEqual(lock.stat().st_ino, inode)
            finally:
                holder.kill()
                holder.wait(timeout=5)
                holder.stdin.close()
                holder.stdout.close()
            successor = subprocess.run(['bash', '-c', script + '\nacquire_lock\nprintf admitted',
                                        'fixture', str(lock)], capture_output=True, timeout=5)
            self.assertEqual(successor.returncode, 0, successor.stderr)
            self.assertEqual(successor.stdout, b'admitted')
            self.assertEqual(lock.read_bytes(), b'fixture sentinel\n')
            self.assertEqual(lock.stat().st_ino, inode)

class AdministrativeLockTests(unittest.TestCase):
    def test_default_lock_identity_matches_updater(self):
        source = (ROOT / 'routegate-update-transaction.sh').read_text()
        assignment = re.search(r'^LOCK_FILE=.*$', source, re.M).group(0)
        result = subprocess.run(['bash', '-c',
            'unset RG_UPDATE_LOCK_FILE; source "$1/routegate-recovery"; '
            'recovery_update=$UPDATE_LOCK_FILE; ' + assignment +
            '; [[ "$recovery_update" == "$LOCK_FILE" ]]; '
            'source "$1/../install.sh"; [[ "$ROUTEGATE_UPDATE_LOCK_FILE" == "$LOCK_FILE" ]]', 'fixture', str(ROOT)],
            capture_output=True, timeout=5)
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_update_recovery_installer_and_legacy_contention(self):
        source = (ROOT / 'routegate-update-transaction.sh').read_text()
        acquire = re.search(r'^acquire_lock\(\) \{\n.*?^\}', source, re.M | re.S).group(0)
        for holder_kind, contender_kind in (
            ('update', 'recovery'), ('recovery', 'update'),
            ('legacy', 'recovery'), ('recovery', 'legacy'),
            ('recovery', 'recovery'),
            ('update', 'installer'), ('installer', 'update'),
            ('recovery', 'installer'), ('installer', 'recovery'),
            ('legacy-installer', 'installer'), ('installer', 'legacy-installer'),
            ('installer', 'installer'),
        ):
            with self.subTest(holder=holder_kind, contender=contender_kind), tempfile.TemporaryDirectory() as tmp:
                directory = pathlib.Path(tmp)
                locks = [directory / name for name in ('update.lock', 'recovery.lock', 'installer.lock')]
                for lock in locks:
                    lock.write_bytes(b'fixture sentinel\n')
                inodes = [lock.stat().st_ino for lock in locks]

                def command(kind, hold, label):
                    action = 'printf "ready\\n"; read -r finish' if hold else 'printf admitted'
                    prefix = 'set -eu\ncd "$1"\n'
                    if kind == 'recovery':
                        # Execute the real main and lock setup. Replace only the
                        # privileged operation and root check, never flock.
                        script = prefix + '''RG_UPDATE_LOCK_FILE="$PWD/update.lock"
source "$2/routegate-recovery"
require_root() { :; }
LOCK_FILE="$PWD/recovery.lock"
LOG_FILE="$PWD/$3.log"
restart_services() { ''' + action + '; }\nmain restart-services'
                    elif kind == 'installer':
                        # Execute the real main through its first input step.
                        # Mock privileged logging and stop before any host work.
                        script = prefix + '''RG_UPDATE_LOCK_FILE="$PWD/update.lock"
source "$2/../install.sh"
require_root() { :; }
ROUTEGATE_LOCK_FILE="$PWD/installer.lock"
ROUTEGATE_LOG_FILE="$PWD/$3.log"
initialize_logging() { : > "$ROUTEGATE_LOG_FILE"; }
prompt_for_inputs() { ''' + action + '; exit 0; }\nmain'
                    elif kind == 'update':
                        script = prefix + 'LOCK_FILE="$PWD/update.lock"\ndie() { exit 1; }\n' + acquire + '\nacquire_lock\n' + action
                    else:
                        # Original recovery lock identity, with a real flock.
                        legacy_lock = 'installer.lock' if kind == 'legacy-installer' else 'recovery.lock'
                        script = prefix + f'exec 9>>{legacy_lock}\nflock -n 9 || exit 1\n' + action
                    return ['bash', '-c', script, 'fixture', tmp, str(ROOT), label]

                holder = subprocess.Popen(command(holder_kind, True, 'holder'),
                                          stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
                try:
                    with selectors.DefaultSelector() as selector:
                        selector.register(holder.stdout, selectors.EVENT_READ)
                        self.assertTrue(selector.select(5), 'holder did not acquire lock')
                    self.assertEqual(holder.stdout.readline(), b'ready\n')
                    contender = subprocess.run(command(contender_kind, False, 'contender'),
                                               capture_output=True, timeout=5)
                    self.assertEqual(contender.returncode, 1, contender.stderr)
                    self.assertEqual(contender.stdout, b'')
                    self.assertFalse((directory / 'contender.log').exists())
                    log = directory / 'contender.log'
                    log.write_bytes(b'existing log sentinel\n')
                    repeated = subprocess.run(command(contender_kind, False, 'contender'),
                                              capture_output=True, timeout=5)
                    self.assertEqual(repeated.returncode, 1, repeated.stderr)
                    self.assertEqual(repeated.stdout, b'')
                    self.assertEqual(log.read_bytes(), b'existing log sentinel\n')
                    for lock, inode in zip(locks, inodes):
                        self.assertEqual(lock.read_bytes(), b'fixture sentinel\n')
                        self.assertEqual(lock.stat().st_ino, inode)
                finally:
                    holder.kill()
                    holder.wait(timeout=5)
                    holder.stdin.close()
                    holder.stdout.close()
                    holder.stderr.close()
                successor = subprocess.run(command(contender_kind, False, 'successor'),
                                           capture_output=True, timeout=5)
                self.assertEqual(successor.returncode, 0, successor.stderr)
                self.assertEqual(successor.stdout, b'admitted')

class InheritedLockTests(unittest.TestCase):
    def test_child_retains_lock_after_parent_death(self):
        import os
        import signal
        source = (ROOT / 'routegate-update-transaction.sh').read_text()
        acquire = re.search(r'^acquire_lock\(\) \{\n.*?^\}', source, re.M | re.S).group(0)
        prefix = 'set -eu\nLOCK_FILE=$1\ndie() { exit 73; }\n' + acquire
        with tempfile.TemporaryDirectory() as tmp:
            lock = str(pathlib.Path(tmp) / 'update.lock')
            # The child inherits descriptor 9, like an external rollback helper.
            parent = subprocess.Popen(['bash', '-c', prefix + '''
acquire_lock
exec 7<&0
bash -c 'printf "%s\\n" "$BASHPID"; read -r finish' <&7 &
wait
''', 'fixture', lock], stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                stderr=subprocess.PIPE)
            child = None
            try:
                with selectors.DefaultSelector() as selector:
                    selector.register(parent.stdout, selectors.EVENT_READ)
                    self.assertTrue(selector.select(5), 'child did not start')
                child = int(parent.stdout.readline())
                parent.kill()
                parent.wait(timeout=5)
                denied = subprocess.run(['bash', '-c', prefix + '\nacquire_lock', 'fixture', lock],
                                        capture_output=True, timeout=5)
                self.assertEqual(denied.returncode, 73, denied.stderr)
                os.kill(child, signal.SIGKILL)
                child = None
                with selectors.DefaultSelector() as selector:
                    selector.register(parent.stdout, selectors.EVENT_READ)
                    self.assertTrue(selector.select(5), 'child retained output descriptor')
                self.assertEqual(parent.stdout.read(), b'')
                admitted = subprocess.run(['bash', '-c', prefix + '\nacquire_lock', 'fixture', lock],
                                          capture_output=True, timeout=5)
                self.assertEqual(admitted.returncode, 0, admitted.stderr)
            finally:
                if child is not None:
                    try:
                        os.kill(child, signal.SIGKILL)
                    except ProcessLookupError:
                        pass
                if parent.poll() is None:
                    parent.kill()
                parent.wait(timeout=5)
                parent.stdin.close()
                parent.stdout.close()
                parent.stderr.close()


class ProductionLikeLockTests(unittest.TestCase):
    def test_down_migration_entry_points(self):
        import os
        with tempfile.TemporaryDirectory() as tmp:
            directory = pathlib.Path(tmp)
            lock = directory / 'update.lock'
            lock.write_bytes(b'sentinel')
            inode = lock.stat().st_ino
            sql = directory / 'fixture.down.sql'
            sql.write_text('SELECT 1;\n')
            # At the database boundary an independently opened descriptor must
            # still be excluded, including on a standalone invocation.
            psql = directory / 'psql'
            psql.write_text('#!/bin/bash\nflock -n "$RG_UPDATE_LOCK_FILE" true && exit 21\necho called >> "$TRACE"\n')
            psql.chmod(0o755)
            trace = directory / 'calls'
            env = dict(os.environ, RG_UPDATE_LOCK_FILE=str(lock), TRACE=str(trace),
                       PATH=tmp + ':' + os.environ['PATH'])
            runner = str(ROOT / 'deploy-production-like-bundle.sh')
            command = 'bash "$1" "$2" fixture "$3" version; rc=$?; exit "$rc"'
            holder = 'source "$4"; rg_update_acquire_production_like_lock || exit 20; '
            for setup, mode, allowed in (
                ('', '--run-down-migration', True),
                (holder, '--run-down-migration-inherited', True),
                (holder, '--run-down-migration', False),
                ('exec 8>&-; ', '--run-down-migration-inherited', False),
                ('exec 8>>"$TRACE.other"; flock -n 8; ', '--run-down-migration-inherited', False),
                (holder + 'exec 7>&8; exec 8>>"$RG_UPDATE_LOCK_FILE"; ',
                 '--run-down-migration-inherited', False),
            ):
                with self.subTest(setup=setup, mode=mode):
                    trace.unlink(missing_ok=True)
                    result = subprocess.run(['bash', '-c', setup + command, 'fixture', runner,
                                             mode, str(sql), str(ROOT / 'routegate-update-core.sh')],
                                            env=env, capture_output=True, timeout=5)
                    self.assertEqual(result.returncode == 0, allowed, result.stderr)
                    self.assertEqual(trace.exists(), allowed)
                    self.assertEqual(lock.stat().st_ino, inode)
                    self.assertEqual(lock.read_bytes(), b'sentinel')

    def test_competing_host_lock_refuses_all_outer_entry_points(self):
        import fcntl
        import os
        with tempfile.TemporaryDirectory() as tmp:
            directory = pathlib.Path(tmp)
            lock = directory / 'update.lock'
            core = str(ROOT / 'routegate-update-core.sh')
            role = str(ROOT / 'routegate-update-role.sh')
            deploy = str(ROOT / 'deploy-production-like-bundle.sh')
            commit = 'c4137cc43a2385ac38fe71e059f33405535b6618'
            manager_args = [commit, core, 'fixture', core, role, deploy, core, core]
            with lock.open('ab') as held:
                fcntl.flock(held, fcntl.LOCK_EX | fcntl.LOCK_NB)
                for args in ([deploy, commit, core, 'fixture', core, core],
                             [ROOT / 'production-like-update-manager.sh', *manager_args],
                             [ROOT / 'production-like-rg140-manager-main.sh', *manager_args]):
                    with self.subTest(script=args[0]):
                        result = subprocess.run(['bash', *args], capture_output=True, timeout=5,
                                                env=dict(os.environ, RG_UPDATE_LOCK_FILE=str(lock),
                                                         TMPDIR=str(directory)))
                        self.assertNotEqual(result.returncode, 0)
                        self.assertIn(b'host update lock unavailable', result.stderr)
                        self.assertEqual(list(directory.iterdir()), [lock])


class RollbackCycleLockTests(unittest.TestCase):
    def test_workflow_holds_lock_across_both_deploys_and_all_validations(self):
        import os
        import textwrap

        workflow = (ROOT.parent / '.github/workflows/production-like-ops.yml').read_text()
        cycle = textwrap.dedent(workflow.split('<<\'CYCLE\'\n', 1)[1].split('\n          CYCLE\n', 1)[0])
        for scenario in ('complete', 'validation-fails', 'already-busy'):
            with self.subTest(scenario=scenario), tempfile.TemporaryDirectory() as tmp:
                directory = pathlib.Path(tmp)
                cycle_file = directory / 'cycle.sh'
                cycle_file.write_text(cycle)
                lock = directory / 'update.lock'
                lock.write_bytes(b'sentinel')
                trace = directory / 'trace'
                probe = '''
if flock -n "$RG_UPDATE_LOCK_FILE" true; then exit 77; fi
echo "$1" >>"$TRACE"
'''
                ops = directory / 'ops'
                ops.write_text(probe + '[[ $SCENARIO != validation-fails ]]\n')
                # Run both actual deploy entry points, stopping at the first
                # host preflight. The deploy EXIT trap deletes its own core.
                core = (ROOT / 'routegate-update-core.sh').read_text()
                inputs = []
                for stage in ('fail', 'redeploy'):
                    bundle, validator, lib = [directory / (stage + suffix)
                                              for suffix in ('.tar', '.sh', '-core.sh')]
                    bundle.touch()
                    validator.touch()
                    markers = """
echo '[rollback-cycle] intentional validation failure reached'
echo 'Failure at stage=observability_validation; restoring production-like baseline.'
echo 'database rollback restored backup schema=fixture'
exit 42
""" if stage == 'fail' else 'exit 0'
                    lib.write_text(core + '\nrg_update_require_root() {\nset -- ' + stage +
                                   '\n' + probe + markers + '\n}\n')
                    inputs.append((bundle, validator, lib))
                fail, redeploy = inputs
                args = ['bash', str(cycle_file), 'fixture', fail[0], redeploy[0], 'unused',
                        ROOT / 'deploy-production-like-bundle.sh', fail[1], redeploy[1],
                        fail[2], redeploy[2], ops]
                if scenario == 'already-busy':
                    args = ['flock', '-n', str(lock), *args]
                result = subprocess.run(args, env=dict(os.environ, TRACE=str(trace),
                                        SCENARIO=scenario, RG_UPDATE_LOCK_FILE=str(lock)),
                                        capture_output=True, timeout=5)
                self.assertEqual(result.returncode == 0, scenario == 'complete', result.stderr)
                expected = {'complete': ['validate', 'fail', 'validate', 'redeploy', 'validate'],
                            'validation-fails': ['validate'], 'already-busy': []}
                self.assertEqual(trace.read_text().splitlines() if trace.exists() else [],
                                 expected[scenario])
                subprocess.run(['flock', '-n', lock, 'true'], check=True, timeout=5)
                self.assertEqual(lock.read_bytes(), b'sentinel')


class RuntimeOpsLockTests(unittest.TestCase):
    def test_runtime_operations_share_host_lock(self):
        import os

        operations = ['restart-sing-box', 'restart-wireguard', 'restart-hysteria2',
                      'restart-mtproto', 'reconcile-mtproto-port-conflict']
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            lock = root / 'host.lock'
            lock.write_bytes(b'lock sentinel')
            inode = lock.stat().st_ino
            trace = root / 'trace'
            for name in ('routegate', 'routegate-mtproto', 'sing-box'):
                (root / name).mkdir()
            (root / 'routegate/manager.env').write_text(
                'echo manager-env >>"$TRACE"; ROUTEGATE_DATABASE_URL=fixture\n')
            config = root / 'routegate-mtproto/config.toml'
            initial = 'bind-to = "0.0.0.0:443"\n'
            config.write_text(initial)
            (root / 'sing-box/config.json').write_text('{}')
            # The remote script normally runs as root on the target. Redirect
            # its absolute config paths and substitute only the host boundary.
            ops = root / 'ops.sh'
            source = (ROOT / 'production-like-ops.sh').read_text()
            ops.write_text(source.replace('/etc/', tmp + '/').replace('  require_root\n', '  :\n'))
            stub = """#!/bin/bash
if flock -n "$RG_UPDATE_LOCK_FILE" true; then echo unlocked >>"$TRACE"; fi
echo "${0##*/} $*" >>"$TRACE"
case "${0##*/}" in
  systemctl)
    if [[ $1 == show ]]; then echo loaded; fi
    if [[ $1 == restart && $2 == routegate-mtproto.service && $INJECT == rollback ]]; then
      exit 1
    fi ;;
  psql)
    case "${*: -1}" in
      *'count(*) FROM servers'*) echo 1 ;;
      *'SELECT vless_port'*) echo '443|8443|1443' ;;
      *) echo fixture ;;
    esac ;;
  curl) printf 200 ;;
esac
"""
            (root / 'bin').mkdir()
            for name in ('systemctl', 'psql', 'curl', 'sing-box'):
                binary = root / 'bin' / name
                binary.write_text(stub)
                binary.chmod(0o755)
            env = dict(os.environ, PATH=f"{root}/bin:" + os.environ['PATH'],
                       RG_UPDATE_LOCK_FILE=str(lock), TRACE=str(trace), INJECT='')
            for op in operations:
                with self.subTest(busy=op):
                    result = subprocess.run(['flock', '-n', lock, 'bash', ops, op],
                                            env=env, capture_output=True, timeout=5)
                    self.assertNotEqual(result.returncode, 0)
                    self.assertIn(b'host update lock unavailable', result.stderr)
                    self.assertFalse(trace.exists())
                    self.assertEqual(config.read_text(), initial)
                    self.assertEqual(list(config.parent.iterdir()), [config])

            # The locked deploy cycle invokes the read-only validator as a child.
            validated = subprocess.run(['flock', '-n', lock, 'bash', ops, 'validate'],
                                       env=env, capture_output=True, timeout=5)
            self.assertEqual(validated.returncode, 0, validated.stderr)
            self.assertIn(b'validation=passed', validated.stdout)

            for op, inject in [(op, '') for op in operations] + [(operations[-1], 'rollback')]:
                with self.subTest(operation=op, inject=inject):
                    config.write_text(initial)
                    trace.unlink(missing_ok=True)
                    result = subprocess.run(['bash', ops, op], env=dict(env, INJECT=inject),
                                            capture_output=True, timeout=5)
                    self.assertEqual(result.returncode == 0, not inject, result.stderr)
                    calls = trace.read_text().splitlines()
                    self.assertNotIn('unlocked', calls)
                    self.assertTrue(any(c.startswith('systemctl restart ') for c in calls))
                    if op == operations[-1]:
                        self.assertEqual(config.read_text(), initial if inject else initial.replace(':443', ':1443'))
                        if inject:
                            self.assertEqual(calls.count('systemctl restart routegate-mtproto.service'), 2)
                    subprocess.run(['flock', '-n', lock, 'true'], check=True, timeout=5)
            self.assertEqual(lock.stat().st_ino, inode)
            self.assertEqual(lock.read_bytes(), b'lock sentinel')
