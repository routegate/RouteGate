import contextlib
import importlib.util
import io
import os
import fcntl
import pathlib
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('repair', pathlib.Path(__file__).with_name('rg140-repair-us-agent.py'))
repair = importlib.util.module_from_spec(spec)
spec.loader.exec_module(repair)

class RepairTests(unittest.TestCase):
    def run_case(self, *, fresh=True, drift=False, failed_scope=False, bad_checksum=False):
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            old = root / 'routegate-agent'
            old.write_bytes(b'old-agent')
            candidate = root / 'candidate'
            candidate.write_bytes(b'new-agent')
            calls, output = [], io.StringIO()
            def command(args):
                calls.append(args)
                return ''
            with patch.dict(os.environ, RG_UPDATE_LOCK_FILE=str(root/'update.lock')), \
                 patch.object(repair, 'AGENT', old), patch.object(repair, 'BACKUPS', root/'backups'), \
                 patch.object(repair, 'command', side_effect=command), \
                 patch.object(repair, 'scope', side_effect=[None, repair.Stop('agent_work_in_progress')] if failed_scope else None), \
                 patch.object(repair, 'files', side_effect=[{'file':'same'},{'file':'changed' if drift else 'same'}]), \
                 patch.object(repair, 'services', return_value={'sing-box':'same PID'}), \
                 patch.object(repair, 'identities', return_value='private-hash'), \
                 patch.object(repair, 'sql', side_effect=['2026-10-05T05:07:15Z'] + ['t' if fresh else 'f']*60), \
                 patch.object(repair.time, 'sleep'), contextlib.redirect_stdout(output):
                error = None
                try:
                    repair.repair(candidate, '0'*64 if bad_checksum else repair.digest(candidate), repair.COMMIT)
                except repair.Stop as caught:
                    error = str(caught)
            return old.read_bytes(), calls, output.getvalue(), error

    def test_host_lock_through_cli(self):
        with tempfile.TemporaryDirectory() as tmp:
            lock = pathlib.Path(tmp) / 'update.lock'
            lock.write_bytes(b'sentinel')
            env = dict(os.environ, RG_UPDATE_LOCK_FILE=str(lock))
            for setup, mode, reason in (
                ('', '', 'unapproved_candidate'),
                ('exec 8>>"$RG_UPDATE_LOCK_FILE"; flock -n 8; ', '', 'host_update_lock_unavailable'),
                ('exec 8>>"$RG_UPDATE_LOCK_FILE"; flock -n 8; ', '--inherit-host-lock', 'unapproved_candidate'),
                ('exec 8>>"$RG_UPDATE_LOCK_FILE.other"; flock -n 8; ', '--inherit-host-lock', 'invalid_inherited_host_lock'),
                ('exec 7>>"$RG_UPDATE_LOCK_FILE"; flock -n 7; exec 8>>"$RG_UPDATE_LOCK_FILE"; ',
                 '--inherit-host-lock', 'host_update_lock_unavailable'),
            ):
                with self.subTest(mode=mode, setup=setup):
                    result = subprocess.run(['bash', '-c', setup + '"$@"', 'fixture',
                                             sys.executable, repair.__file__, *([mode] if mode else []),
                                             'fixture', 'bad', 'fixture'], env=env,
                                            capture_output=True, text=True, timeout=5)
                    self.assertEqual(result.returncode, 1)
                    self.assertIn(reason, result.stdout)
            self.assertEqual(lock.read_bytes(), b'sentinel')

    def test_direct_repair_holds_lock_until_unwind(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = pathlib.Path(tmp) / 'host.lock'
            with path.open('ab') as contender:
                def injected_failure(*args):
                    with self.assertRaises(BlockingIOError):
                        fcntl.flock(contender, fcntl.LOCK_EX | fcntl.LOCK_NB)
                    raise repair.Stop('fixture_failure')
                with patch.dict(os.environ, RG_UPDATE_LOCK_FILE=str(path)), \
                     patch.object(repair, 'repair_locked', side_effect=injected_failure), \
                     self.assertRaisesRegex(repair.Stop, 'fixture_failure'):
                    repair.repair(None, None, None)
                fcntl.flock(contender, fcntl.LOCK_EX | fcntl.LOCK_NB)

    def test_success_changes_only_agent_binary_and_service(self):
        binary,calls,output,error = self.run_case()
        self.assertIsNone(error)
        self.assertEqual(binary,b'new-agent')
        self.assertEqual([x[2:] for x in calls if x[1] in ('start','stop')], [['routegate-agent'],['routegate-agent']])
        self.assertIn('fi_test=still_on_FI; no_cutover_or_cleanup',output)

    def test_checksum_failure_stops_before_service_changes(self):
        binary,calls,_,error = self.run_case(bad_checksum=True)
        self.assertEqual(error,'candidate_checksum_mismatch')
        self.assertEqual(binary,b'old-agent')
        self.assertEqual(calls,[])

    def test_missing_heartbeat_restores_old_binary(self):
        binary,_,output,error = self.run_case(fresh=False)
        self.assertEqual(error,'fresh_authenticated_heartbeat_missing')
        self.assertEqual(binary,b'old-agent')
        self.assertIn('agent_rollback=restored_previous_binary',output)

    def test_protected_drift_restores_old_binary(self):
        binary,_,_,error = self.run_case(drift=True)
        self.assertEqual(error,'protected_baseline_changed')
        self.assertEqual(binary,b'old-agent')

    def test_job_race_after_stop_restores_old_binary(self):
        binary,_,_,error = self.run_case(failed_scope=True)
        self.assertEqual(error,'agent_work_in_progress')
        self.assertEqual(binary,b'old-agent')

if __name__ == '__main__':
    unittest.main()
