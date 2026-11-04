"""Root-only isolated first-publication and failure tests."""
import concurrent.futures
import hashlib
import os
import pathlib
import tempfile
import unittest
from unittest import mock
from scripts import rg140_pin_writer as writer

PAYLOAD = (b'FORMAT_VERSION=1\nMODE=external\n' +
           b'ENV_SHA256=' + b'a'*64 + b'\nUNIT_SHA256=' + b'b'*64 +
           b'\nBINARY_SHA256=' + hashlib.sha256(b'fixture executable').hexdigest().encode() + b'\n')


class PinWriterTests(unittest.TestCase):
    def setUp(self):
        self.assertEqual(os.geteuid(), 0, 'run via sudo')
        idle = mock.patch.object(writer, 'require_idle_manager_account')
        idle.start()
        self.addCleanup(idle.stop)
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = pathlib.Path(self.tmp.name)
        self.binary = self.root / 'manager'
        self.binary.write_bytes(b'fixture executable')
        self.binary.chmod(0o755)
        self.fd = os.open(self.root, os.O_RDONLY | os.O_DIRECTORY)
        self.addCleanup(os.close, self.fd)
        self.directory = self.root / writer.DIRECTORY

    def publish(self, fd, payload):
        writer.publish_first_pin(fd, payload, manager_binary=self.binary)

    def test_wrong_executable_refuses_before_publication(self):
        self.binary.write_bytes(b'different executable')
        with self.assertRaisesRegex(ValueError, 'differs from pinned bytes'):
            self.publish(self.fd, PAYLOAD)
        self.assertFalse(self.directory.exists())

    def test_unsafe_executable_refuses_before_publication(self):
        for mode in (0o644, 0o777):
            with self.subTest(mode=mode):
                self.binary.chmod(mode)
                with self.assertRaises(PermissionError):
                    self.publish(self.fd, PAYLOAD)
        self.binary.chmod(0o755)
        link = self.root / 'candidate-link'
        link.symlink_to(self.binary)
        with self.assertRaises(PermissionError):
            writer.publish_first_pin(self.fd, PAYLOAD, manager_binary=link)
        self.assertFalse(self.directory.exists())

    def test_publication_and_refusal_to_overwrite(self):
        self.publish(self.fd, PAYLOAD)
        pin = self.directory / 'restore.pin'
        self.assertEqual(pin.read_bytes(), PAYLOAD)
        self.assertEqual(pin.stat().st_mode & 0o777, 0o600)
        self.assertEqual(pin.stat().st_nlink, 1)
        self.assertEqual(pin.stat().st_uid, 0)
        self.assertEqual({p.name for p in self.directory.iterdir()}, {'restore.pin', writer.PENDING})
        self.assertTrue((self.directory / writer.PENDING).is_dir())
        with self.assertRaises(FileExistsError):
            self.publish(self.fd, PAYLOAD.replace(b'a'*64, b'd'*64))
        self.assertEqual(pin.read_bytes(), PAYLOAD)

    def test_pending_intent_precedes_pin_publication(self):
        real_fsync = os.fsync
        seen = []
        def checkpoint(fd):
            seen.append({p.name for p in self.directory.iterdir()})
            real_fsync(fd)
        with mock.patch.object(writer.os, 'fsync', side_effect=checkpoint):
            self.publish(self.fd, PAYLOAD)
        self.assertEqual(seen, [set(), {writer.PENDING},
                               {writer.PENDING, 'pending.pin'},
                               {writer.PENDING, 'restore.pin'}])

    def test_late_scan_error_retains_durable_intent_before_pin_write(self):
        events = []
        fsync = os.fsync
        def record_sync(fd):
            fsync(fd)
            events.append('synced')
        def scan():
            events.append('scan')
            if self.directory.exists():
                raise PermissionError('late inspection failed')
        with mock.patch.object(writer.os, 'fsync', side_effect=record_sync), \
             mock.patch.object(writer, 'require_idle_manager_account', side_effect=scan):
            with self.assertRaisesRegex(PermissionError, 'late inspection failed'):
                self.publish(self.fd, PAYLOAD)
        self.assertEqual(events, ['scan', 'synced', 'synced', 'scan'])
        self.assertEqual({p.name for p in self.directory.iterdir()}, {writer.PENDING})
        with self.assertRaises(FileExistsError):
            self.publish(self.fd, PAYLOAD)

    def test_invalid_payload_does_not_create_latch(self):
        for payload in (PAYLOAD[:-1], PAYLOAD+b'\0', PAYLOAD+b'\n', b''):
            with self.subTest(payload=payload[:10]), self.assertRaises(ValueError):
                self.publish(self.fd, payload)
            self.assertFalse(self.directory.exists())

    def test_existing_partial_directory_is_not_adopted(self):
        self.directory.mkdir()
        with self.assertRaises(FileExistsError):
            self.publish(self.fd, PAYLOAD)
        self.assertEqual(list(self.directory.iterdir()), [])

    def test_symlink_is_not_followed(self):
        target = self.root / 'outside'
        target.mkdir()
        self.directory.symlink_to(target)
        with self.assertRaises(FileExistsError):
            self.publish(self.fd, PAYLOAD)
        self.assertEqual(list(target.iterdir()), [])

    def test_unsafe_parent(self):
        self.root.chmod(0o777)
        with self.assertRaises(PermissionError):
            self.publish(self.fd, PAYLOAD)
        self.assertFalse(self.directory.exists())

    def test_failure_at_each_durability_boundary_retains_latch(self):
        real_fsync = os.fsync
        for fail_at in (1, 2, 3, 4):
            with self.subTest(fail_at=fail_at), tempfile.TemporaryDirectory() as tmp:
                fd = os.open(tmp, os.O_RDONLY | os.O_DIRECTORY)
                calls = 0
                def fail(f):
                    nonlocal calls
                    calls += 1
                    if calls == fail_at:
                        raise OSError('injected fsync failure')
                    real_fsync(f)
                try:
                    with mock.patch.object(writer.os, 'fsync', side_effect=fail):
                        with self.assertRaises(OSError):
                            self.publish(fd, PAYLOAD)
                    directory = pathlib.Path(tmp) / writer.DIRECTORY
                    self.assertTrue(directory.is_dir())
                    pin = directory / 'restore.pin'
                    if fail_at == 4:
                        self.assertEqual(pin.read_bytes(), PAYLOAD)
                    else:
                        self.assertFalse(pin.exists())
                    with self.assertRaises(FileExistsError):
                        self.publish(fd, PAYLOAD)
                finally:
                    os.close(fd)

    def test_concurrent_first_writers_have_one_winner(self):
        def publish(payload):
            try:
                self.publish(self.fd, payload)
                return payload
            except FileExistsError:
                return None
        other = PAYLOAD.replace(b'a'*64, b'd'*64)
        with concurrent.futures.ThreadPoolExecutor(max_workers=2) as pool:
            results = list(pool.map(publish, (PAYLOAD, other)))
        winners = [r for r in results if r is not None]
        self.assertEqual(len(winners), 1)
        self.assertEqual((self.directory / 'restore.pin').read_bytes(), winners[0])

    def test_failed_rename_never_publishes(self):
        with mock.patch.object(writer.os, 'rename', side_effect=OSError('rename failure')):
            with self.assertRaises(OSError):
                self.publish(self.fd, PAYLOAD)
        self.assertTrue(self.directory.is_dir())
        self.assertFalse((self.directory / 'restore.pin').exists())
        self.assertEqual((self.directory / 'pending.pin').read_bytes(), PAYLOAD)


class ManagerAccountTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.proc = pathlib.Path(self.tmp.name)
        (self.proc / 'self').mkdir()
        (self.proc / 'self/uid_map').write_text('0 0 4294967295\n')
        self.mountinfo = self.proc / 'self/mountinfo'
        self.mountinfo.write_text('10 9 0:3 / /proc rw - proc proc rw\n')
        (self.proc / '1').mkdir()
        self.status = self.proc / '1/status'
        self.status.write_bytes(b'Uid:\t0 0 0 0\n')
        lookup = mock.patch.object(writer.pwd, 'getpwnam')
        self.account = lookup.start()
        self.addCleanup(lookup.stop)
        self.account.return_value.pw_uid = 1234

    def test_checks_all_uid_fields_and_refuses_incomplete_status(self):
        writer.require_idle_manager_account(self.proc)
        for i in range(4):
            values = ['0'] * 4
            values[i] = '1234'
            self.status.write_text('Uid:\t' + ' '.join(values) + '\n')
            with self.assertRaisesRegex(PermissionError, 'still in use'):
                writer.require_idle_manager_account(self.proc)
        self.status.write_bytes(b'Uid:\t0 0 0\n')
        with self.assertRaises(ValueError):
            writer.require_idle_manager_account(self.proc)
        self.status.unlink()
        with self.assertRaises(FileNotFoundError):
            writer.require_idle_manager_account(self.proc)

    def test_requires_service_account_and_unrestricted_proc(self):
        self.account.side_effect = KeyError('routegate')
        with self.assertRaises(KeyError):
            writer.require_idle_manager_account(self.proc)
        self.account.side_effect = None
        self.mountinfo.write_text('10 9 0:3 / /proc rw - proc proc rw,hidepid=2\n')
        with self.assertRaisesRegex(PermissionError, 'visibility'):
            writer.require_idle_manager_account(self.proc)
        (self.proc / 'self/uid_map').write_text('0 100000 65536\n')
        with self.assertRaisesRegex(PermissionError, 'mapping'):
            writer.require_idle_manager_account(self.proc)
