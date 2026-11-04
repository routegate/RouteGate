"""Compose the real pin writer, reader and extracted outer rollback handlers.

Only systemctl and mutation endpoints are stubbed. No host or database access.
"""
import hashlib
import os
import pathlib
import re
import subprocess
import tempfile
import unittest
from unittest import mock
from scripts import rg140_pin_writer as writer

ROOT = pathlib.Path(__file__).resolve().parent


class PinPublicationRollbackTests(unittest.TestCase):
    @mock.patch.object(writer, 'require_idle_manager_account', return_value=None)
    def test_publication_outcomes_at_real_rollback_boundary(self, _idle):
        self.assertEqual(os.geteuid(), 0, 'run via sudo')
        for filename, handler, stop in (
            ('deploy-production-like-bundle.sh', 'rollback', 'stop agent manager'),
            ('production-like-update-manager.sh', 'on_error', 'stop manager'),
        ):
            match = re.search(r'^' + handler + r'\(\) \{\n.*?^\}', (ROOT / filename).read_text(), re.M | re.S)
            self.assertIsNotNone(match)
            for outcome in ('success', 'parent_fsync', 'intent_fsync', 'file_fsync', 'rename', 'directory_fsync', 'missing_env'):
                for db_mutated in (0, 1):
                    with self.subTest(handler=handler, outcome=outcome, db=db_mutated), tempfile.TemporaryDirectory() as tmp:
                        root = pathlib.Path(tmp)
                        host, backup = root / 'host', root / 'backup'
                        parent = host / 'var/lib'
                        parent.mkdir(parents=True)
                        backup.mkdir()
                        payload = b'FORMAT_VERSION=1\nMODE=external\n'
                        for key, path, name, content in (
                            ('ENV', 'etc/routegate/manager.env', 'manager.env', b'ROUTEGATE_DATABASE_MIGRATION_MODE=external\n'),
                            ('UNIT', 'etc/systemd/system/routegate-manager.service', 'routegate-manager.service', b'fixture unit'),
                            ('BINARY', 'usr/local/bin/routegate-manager', 'routegate-manager', b'fixture binary'),
                        ):
                            target = host / path
                            target.parent.mkdir(parents=True, exist_ok=True)
                            target.write_bytes(content)
                            (backup / name).write_bytes(content)
                            payload += f'{key}_SHA256={hashlib.sha256(content).hexdigest()}\n'.encode()
                        binary = host / 'usr/local/bin/routegate-manager'
                        binary.chmod(0o755)
                        fd = os.open(parent, os.O_RDONLY | os.O_DIRECTORY)
                        real_fsync = os.fsync
                        calls = 0
                        fail_at = {'parent_fsync': 1, 'intent_fsync': 2, 'file_fsync': 3, 'directory_fsync': 4}.get(outcome)
                        def fsync(descriptor):
                            nonlocal calls
                            calls += 1
                            if calls == fail_at:
                                raise OSError('injected fsync failure')
                            real_fsync(descriptor)
                        try:
                            with mock.patch.object(writer.os, 'fsync', side_effect=fsync):
                                if outcome == 'rename':
                                    with mock.patch.object(writer.os, 'rename', side_effect=OSError('injected rename failure')):
                                        with self.assertRaises(OSError):
                                            writer.publish_first_pin(fd, payload, manager_binary=binary)
                                elif fail_at:
                                    with self.assertRaises(OSError):
                                        writer.publish_first_pin(fd, payload, manager_binary=binary)
                                else:
                                    writer.publish_first_pin(fd, payload, manager_binary=binary)
                        finally:
                            os.close(fd)
                        directory = parent / writer.DIRECTORY
                        self.assertTrue(directory.is_dir())
                        snapshot = {p.name: (None if p.is_dir() else p.read_bytes()) for p in directory.iterdir()}
                        if outcome == 'missing_env':
                            (host / 'etc/routegate/manager.env').unlink()
                        script = r'''
set -u
source "$1/routegate-update-core.sh"
RG_UPDATE_ROOT=$2/host
BACKUP_DIR=$2/backup
trace=$2/trace
DB_MAY_BE_MUTATED=$3
MUTATED=1
DB_URL=fixture
STAGE=fixture
RG_UPDATE_AGENT_SERVICE=agent
RG_UPDATE_MANAGER_SERVICE=manager
systemctl() {
  if [[ $1 == show ]]; then
    if [[ $2 == --property=FragmentPath ]]; then
      printf '/etc/systemd/system/routegate-manager.service\n'
    fi
  else printf '%s\n' "$*" >> "$trace"; fi
}
log() { printf '%s\n' "$*"; }
cleanup() { :; }
rollback_database_to_backup() { printf 'database\n' >> "$trace"; exit 77; }
rollback_database() { printf 'database\n' >> "$trace"; exit 77; }
restore_maintenance_dispatch() { printf 'maintenance\n' >> "$trace"; }
rg_update_restore_backup() { printf 'files\n' >> "$trace"; exit 78; }
rg_update_restore_management_backup() { printf 'files\n' >> "$trace"; exit 78; }
''' + match.group(0) + '\nset +e\nfalse\n' + handler
                        result = subprocess.run(['bash', '-c', script, 'fixture', str(ROOT), tmp, str(db_mutated)],
                                                capture_output=True, text=True, timeout=5)
                        trace = (root / 'trace').read_text().splitlines()
                        # A fresh shell has no in-memory writer outcome. Even
                        # successful publication leaves conversion unresolved.
                        self.assertEqual(result.returncode, 1, result.stderr)
                        self.assertEqual(trace, [stop])
                        self.assertIn('credential-mode restore pin refused', result.stderr)
                        if outcome in ('success', 'directory_fsync', 'missing_env'):
                            self.assertEqual((directory / 'restore.pin').read_bytes(), payload)
                            self.assertTrue((directory / writer.PENDING).is_dir())
                        self.assertEqual({p.name: (None if p.is_dir() else p.read_bytes()) for p in directory.iterdir()}, snapshot)
                        retry_fd = os.open(parent, os.O_RDONLY | os.O_DIRECTORY)
                        try:
                            with self.assertRaises(FileExistsError):
                                writer.publish_first_pin(retry_fd, payload, manager_binary=binary)
                        finally:
                            os.close(retry_fd)
