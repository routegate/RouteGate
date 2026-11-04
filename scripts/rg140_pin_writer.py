"""Unwired first-publication primitive, not an installation/conversion command.

Caller must validate credentials, effective service configuration and binary,
stop services, and hold the host transaction lock before using this primitive.
The parent descriptor must identify the trusted /var/lib directory. Tests use
an isolated directory. No path discovery, credential access or role repair.
"""
import hashlib
import os
import re
import stat

DIRECTORY = 'routegate-credential-mode'
PENDING = 'conversion.pending'
FORMAT = re.compile(rb'FORMAT_VERSION=1\nMODE=external\nENV_SHA256=[a-f0-9]{64}\nUNIT_SHA256=[a-f0-9]{64}\nBINARY_SHA256=[a-f0-9]{64}\n')


def publish_first_pin(parent_fd, payload, *, manager_binary='/usr/local/bin/routegate-manager'):
    """Publish once; never adopt, overwrite, rotate or remove an existing latch.

    Publication never completes the conversion: conversion.pending is retained
    even on success, preventing automatic rollback until explicit recovery.
    Any exception after mkdir retains the directory. A final fsync error has an
    uncertain durability outcome even when a complete pin is visible. Callers
    must not start services after any error or blindly retry by deleting state.
    The caller must keep the approved installed binary and its ancestors stable.
    """
    if os.geteuid() != 0:
        raise PermissionError('root required')
    parent = os.fstat(parent_fd)
    if not stat.S_ISDIR(parent.st_mode) or parent.st_uid != 0 or parent.st_mode & 0o022:
        raise PermissionError('unsafe parent descriptor')
    if not isinstance(payload, bytes) or FORMAT.fullmatch(payload) is None:
        raise ValueError('invalid pin bytes')
    # Pin the inode without opening a device or following a final symlink.
    # Linux excludes exec and a write-open on the same native executable.
    # O_RDWR (without O_TRUNC) provides that exclusion. The Python stream
    # permits only reads. Hold it until all publication/fsync work unwinds.
    inode = os.open(manager_binary, os.O_PATH | os.O_NOFOLLOW)
    try:
        info = os.fstat(inode)
        if (not stat.S_ISREG(info.st_mode) or info.st_uid != 0 or
                info.st_mode & 0o022 or not info.st_mode & 0o111):
            raise PermissionError('unsafe Manager executable')
        fd = os.open(f'/proc/self/fd/{inode}', os.O_RDWR)
        with os.fdopen(fd, 'rb') as executable:
            if not os.path.samestat(info, os.fstat(fd)):
                raise PermissionError('Manager executable identity changed')
            digest = hashlib.sha256()
            for chunk in iter(lambda: executable.read(1024 * 1024), b''):
                digest.update(chunk)
            if not payload.endswith(b'BINARY_SHA256=' + digest.hexdigest().encode() + b'\n'):
                raise ValueError('Manager executable differs from pinned bytes')
            _publish_locked(parent_fd, payload)
    finally:
        os.close(inode)


def _publish_locked(parent_fd, payload):
    # Exclusive mkdir arbitrates first publication. Never remove this latch,
    # including after a write/fsync failure or a competing writer's refusal.
    os.mkdir(DIRECTORY, 0o700, dir_fd=parent_fd)
    os.fsync(parent_fd)
    directory_fd = os.open(DIRECTORY, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW,
                           dir_fd=parent_fd)
    try:
        # Persist intent before any complete pin can become visible. Never clear
        # it here: successful file publication is not a completed conversion.
        os.mkdir(PENDING, 0o700, dir_fd=directory_fd)
        os.fsync(directory_fd)
        fd = os.open('pending.pin', os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW,
                     0o600, dir_fd=directory_fd)
        with os.fdopen(fd, 'wb') as stream:
            os.fchmod(stream.fileno(), 0o600)
            stream.write(payload)
            stream.flush()
            os.fsync(stream.fileno())
        os.rename('pending.pin', 'restore.pin', src_dir_fd=directory_fd, dst_dir_fd=directory_fd)
        os.fsync(directory_fd)
    finally:
        os.close(directory_fd)
