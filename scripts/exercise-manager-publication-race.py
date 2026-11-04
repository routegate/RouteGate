"""Exercise the real publisher and Manager in the startup-latch test's chroot."""
import errno
import hashlib
import os
import pathlib
import socket
import subprocess
import sys
import time
from unittest.mock import patch

import rg140_pin_writer as writer


def exercise(root, manager_command, enter_service=None):
    binary = root / 'manager'
    parent = root / 'var/lib'
    latch = parent / writer.DIRECTORY
    pristine = hashlib.sha256(binary.read_bytes()).digest()
    payload = (b'FORMAT_VERSION=1\nMODE=external\nENV_SHA256=' + b'a' * 64 +
               b'\nUNIT_SHA256=' + b'b' * 64 + b'\nBINARY_SHA256=' +
               pristine.hex().encode() + b'\n')

    def publish():
        fd = os.open(parent, os.O_RDONLY | os.O_DIRECTORY)
        try:
            writer.publish_first_pin(fd, payload, manager_binary=binary)
        finally:
            os.close(fd)

    # A real TCP accept is the barrier: the Manager has passed its latch check
    # and is still alive, waiting for the PostgreSQL handshake.
    with socket.socket() as listener:
        listener.bind(('127.0.0.1', 0))
        listener.listen()
        listener.settimeout(10)
        url = f'postgres://fixture@127.0.0.1:{listener.getsockname()[1]}/fixture?sslmode=disable'
        process = subprocess.Popen(manager_command, env=dict(os.environ, ROUTEGATE_DATABASE_URL=url),
                                   preexec_fn=enter_service)
        try:
            connection, _ = listener.accept()
            with connection:
                try:
                    publish()
                except OSError as err:
                    if err.errno != errno.ETXTBSY:
                        raise
                else:
                    raise AssertionError('published after Manager began database startup')
                assert not latch.exists()
        finally:
            process.kill()
            process.wait(timeout=5)

    # Stop at the actual first mkdir, with publication admitted but no visible
    # marker. The kernel must refuse exec on that inode, before main runs.
    mkdir = os.mkdir
    observed = []
    def before_mkdir(name, *args, **kwargs):
        if name == writer.DIRECTORY:
            assert not latch.exists()
            try:
                subprocess.run(manager_command, check=True, timeout=5, preexec_fn=enter_service,
                               env=dict(os.environ, ROUTEGATE_DATABASE_URL='postgres://%'))
            except OSError as err:
                if err.errno != errno.ETXTBSY:
                    raise
            else:
                raise AssertionError('Manager executed during publication')
            observed.append(name)
        return mkdir(name, *args, **kwargs)

    with patch.object(writer.os, 'mkdir', side_effect=before_mkdir):
        # ETXTBSY can briefly survive waitpid during kernel teardown. Only
        # retry this pre-publication refusal; never adopt a partial latch.
        deadline = time.monotonic() + 5
        while True:
            try:
                publish()
                break
            except OSError as err:
                if err.errno != errno.ETXTBSY or latch.exists() or time.monotonic() >= deadline:
                    raise
                time.sleep(0.01)
    assert observed == [writer.DIRECTORY]
    assert (latch / writer.PENDING).is_dir()
    assert (latch / 'restore.pin').read_bytes() == payload
    assert hashlib.sha256(binary.read_bytes()).digest() == pristine


if __name__ == '__main__':
    root = pathlib.Path(sys.argv[1])
    def enter_service():
        os.chroot(root)
        os.chdir('/')
        os.setgroups([])
        os.setgid(65534)
        os.setuid(65534)
    exercise(root, ['/manager'], enter_service)
    print('Manager/publication exclusion passed in both orders')
