"""Храните потоковые передачи на диске без полной копии пользовательского файла."""

import ctypes
import errno
import hashlib
import json
import os
import sqlite3
import stat
import threading
import time
from uuid import UUID, uuid4

from .config import MAX_WORKSPACE_FILE_BYTES as MAX_FILE
from .observability import LoggedConnection
from .workspace_listing import entry_version
from .workspace_paths import open_entry, validate_path

CHUNK = 1024**2
CREDIT = 4 * CHUNK
TTL = 1800


def public_transfer(item):
    """Скройте внутреннюю идентичность родителя из публичного transfer DTO."""
    return {key: value for key, value in item.items() if key != "parent_identity"}


class FileError(Exception):
    """Храните безопасный код ошибки файлового протокола."""

    def __init__(self, code, *, resync=False):
        """Сохраните код и требование перечитать дерево."""
        super().__init__(code)
        self.code = code
        self.resync = resync


def filesystem_error(error):
    """Переведите errno в безопасный прикладной код."""
    return FileError(
        {
            errno.ENOENT: "not_found",
            errno.ENOTDIR: "not_found",
            errno.EEXIST: "already_exists",
            errno.ENOTEMPTY: "not_empty",
            errno.ENOSPC: "quota_exceeded",
            errno.EDQUOT: "quota_exceeded",
            errno.EACCES: "permission_denied",
            errno.EPERM: "permission_denied",
            errno.ELOOP: "permission_denied",
            errno.EXDEV: "permission_denied",
        }.get(error.errno, "internal")
    )


def rename_noreplace(source_fd, source, target_fd, target):
    """Атомарно перенесите entry без перезаписи назначения."""
    libc = ctypes.CDLL(None, use_errno=True)
    if libc.renameat2(source_fd, source.encode(), target_fd, target.encode(), 1):
        number = ctypes.get_errno()
        raise OSError(number, os.strerror(number))


def identity(info):
    """Верните стабильную идентичность открытого inode."""
    return [info.st_dev, info.st_ino]


class Transfers:
    """Сериализуйте checkpoint и запись chunk, сохраняя подтверждённые данные."""

    def __init__(self, root, state_dir, generation):
        """Откройте data/staging и журнал текущего workspace. Владелец вызывает close."""
        self.generation = generation
        self.lock = threading.RLock()
        self.root = os.open(root / "data", os.O_DIRECTORY | os.O_NOFOLLOW | os.O_CLOEXEC)
        try:
            self.staging = os.open(root / "staging", os.O_DIRECTORY | os.O_NOFOLLOW | os.O_CLOEXEC)
        except BaseException:
            os.close(self.root)
            raise
        self.db = sqlite3.connect(
            state_dir / "files.sqlite3", check_same_thread=False, factory=LoggedConnection
        )
        self.db.row_factory = sqlite3.Row
        self.db.executescript("""
            PRAGMA journal_mode=WAL;
            PRAGMA synchronous=FULL;
            CREATE TABLE IF NOT EXISTS transfers (
                id TEXT PRIMARY KEY, generation TEXT NOT NULL, body TEXT NOT NULL
            );
            CREATE TABLE IF NOT EXISTS chunks (
                id TEXT NOT NULL, offset INTEGER NOT NULL, size INTEGER NOT NULL,
                digest TEXT NOT NULL, PRIMARY KEY(id,offset)
            );
        """)
        self.closed = False
        self.cleaned_staging = 0
        self.downloads = {}
        self.hashers = {}
        self.recover()

    def close(self):
        """Закройте дескрипторы и SQLite после завершения обслуживающих задач."""
        with self.lock:
            for fd in self.downloads.values():
                os.close(fd)
            self.downloads.clear()
            self.db.close()
            os.close(self.root)
            os.close(self.staging)

    def save(self, transfer):
        """Сохраните transfer checkpoint синхронной транзакцией."""
        with self.db:
            self.db.execute(
                "INSERT OR REPLACE INTO transfers VALUES (?,?,?)",
                (transfer["transfer_id"], self.generation, json.dumps(transfer)),
            )

    def get(self, transfer_id):
        """Верните transfer только текущей generation и проверьте TTL."""
        with self.lock:
            try:
                transfer_id = str(UUID(transfer_id))
            except (ValueError, TypeError) as exc:
                raise FileError("not_found") from exc
            row = self.db.execute(
                "SELECT body FROM transfers WHERE id=? AND generation=?",
                (transfer_id, self.generation),
            ).fetchone()
            if row is None:
                raise FileError("not_found")
            transfer = json.loads(row[0])
            if transfer["state"] == "active" and time.time() - transfer["updated"] > TTL:
                self.cancel(transfer_id, "expired")
                raise FileError("transfer_expired")
            return transfer

    def recover(self):
        """Отбросьте неподтверждённый хвост staging после сбоя процесса."""
        with self.lock:
            rows = self.db.execute(
                "SELECT body FROM transfers WHERE generation=?", (self.generation,)
            ).fetchall()
            for row in rows:
                item = json.loads(row[0])
                if item["state"] != "active" or item["direction"] != "upload":
                    continue
                try:
                    fd = os.open(
                        item["transfer_id"], os.O_RDWR | os.O_NOFOLLOW, dir_fd=self.staging
                    )
                except FileNotFoundError:
                    # Commit мог перенести inode до фиксации результата SQLite.
                    item["state"] = "unknown"
                    self.save(item)
                    continue
                try:
                    if os.fstat(fd).st_size < item["offset"]:
                        item["state"] = "failed"
                        self.save(item)
                    else:
                        os.ftruncate(fd, item["offset"])
                        os.fdatasync(fd)
                        os.lseek(fd, 0, os.SEEK_SET)
                        with os.fdopen(os.dup(fd), "rb") as source:
                            self.hashers[item["transfer_id"]] = hashlib.file_digest(
                                source, "sha256"
                            )
                finally:
                    os.close(fd)
            self.expire()

    def expire(self):
        """Удалите истёкшие передачи и ограничьте историю терминальных записей."""
        with self.lock:
            for row in self.db.execute(
                "SELECT body FROM transfers WHERE generation=?", (self.generation,)
            ).fetchall():
                item = json.loads(row[0])
                if item["state"] == "active" and time.time() - item["updated"] > TTL:
                    self.cancel(item["transfer_id"], "expired")
                elif item["state"] != "active" and time.time() - item["updated"] > TTL:
                    with self.db:
                        self.db.execute("DELETE FROM chunks WHERE id=?", (item["transfer_id"],))
                        self.db.execute("DELETE FROM transfers WHERE id=?", (item["transfer_id"],))

    def active_count(self):
        """Верните число активных transfers для метрик без пользовательских labels."""
        with self.lock:
            rows = self.db.execute(
                "SELECT body FROM transfers WHERE generation=?", (self.generation,)
            ).fetchall()
            return sum(json.loads(row[0])["state"] == "active" for row in rows)

    def quota(self):
        """Верните фактическую ёмкость и доступное непривилегированному коду место."""
        info = os.fstatvfs(self.root)
        return {
            "capacity_bytes": info.f_blocks * info.f_frsize,
            "used_bytes": (info.f_blocks - info.f_bfree) * info.f_frsize,
            "available_bytes": info.f_bavail * info.f_frsize,
            "source": "filesystem",
        }

    def begin(self, direction, path, size=None, sha256=None):
        """Проверьте назначение и создайте ограниченную запись передачи."""
        validate_path(path)
        if not path:
            raise FileError("invalid_path")
        if size is not None and not 0 <= size <= MAX_FILE:
            raise FileError("file_too_large")
        with self.lock:
            if self.closed:
                raise FileError("workspace_closed")
            self.expire()
            rows = self.db.execute(
                "SELECT body FROM transfers WHERE generation=?", (self.generation,)
            ).fetchall()
            if sum(json.loads(row[0])["state"] == "active" for row in rows) >= 2:
                raise FileError("rate_limited")
            tid = str(uuid4())
            parent, _, name = path.rpartition("/")
            fd = open_entry(self.root, parent, os.O_RDONLY | os.O_DIRECTORY)
            try:
                parent_identity = identity(os.fstat(fd))
                if direction == "upload":
                    try:
                        os.stat(name, dir_fd=fd, follow_symlinks=False)
                    except FileNotFoundError:
                        pass
                    else:
                        raise FileError("already_exists")
                    if size > self.quota()["available_bytes"]:
                        raise FileError("quota_exceeded")
                    version = None
                else:
                    source = open_entry(fd, name, os.O_RDONLY | os.O_NOFOLLOW)
                    try:
                        info = os.fstat(source)
                        if not stat.S_ISREG(info.st_mode):
                            raise FileError("permission_denied")
                        size, version = info.st_size, entry_version(info)
                        if size > MAX_FILE:
                            raise FileError("file_too_large")
                    finally:
                        os.close(source)
                item = {
                    "transfer_id": tid,
                    "direction": direction,
                    "path": path,
                    "size_bytes": size,
                    "sha256": sha256,
                    "version": version,
                    "parent_identity": parent_identity,
                    "offset": 0,
                    "state": "active",
                    "updated": time.time(),
                    "workspace_generation": self.generation,
                    "chunk_bytes": CHUNK,
                    "credit_bytes": CREDIT,
                }
                self.save(item)
                if direction == "upload":
                    try:
                        stage = os.open(
                            tid,
                            os.O_CREAT | os.O_EXCL | os.O_RDWR | os.O_NOFOLLOW,
                            0o600,
                            dir_fd=self.staging,
                        )
                        try:
                            os.fsync(stage)
                        finally:
                            os.close(stage)
                        os.fsync(self.staging)
                    except OSError:
                        self.cancel(tid, "failed")
                        raise
                    self.hashers[tid] = hashlib.sha256()
                return item
            finally:
                os.close(fd)

    def chunk(self, tid, offset, payload):
        """Подтвердите chunk только после fdatasync и durable записи его digest."""
        with self.lock:
            item = self.get(tid)
            if self.closed or item["state"] != "active" or item["direction"] != "upload":
                raise FileError("workspace_closed" if self.closed else "conflict")
            if not payload or len(payload) > CHUNK or offset + len(payload) > item["size_bytes"]:
                raise FileError("conflict")
            digest = hashlib.sha256(payload).hexdigest()
            if offset < item["offset"]:
                previous = self.db.execute(
                    "SELECT size,digest FROM chunks WHERE id=? AND offset=?", (tid, offset)
                ).fetchone()
                if previous is None or tuple(previous) != (len(payload), digest):
                    raise FileError("conflict")
                return item
            if offset != item["offset"]:
                raise FileError("conflict")
            fd = os.open(tid, os.O_WRONLY | os.O_NOFOLLOW, dir_fd=self.staging)
            try:
                view = memoryview(payload)
                written = 0
                while written < len(view):
                    count = os.pwrite(fd, view[written:], offset + written)
                    if not count:
                        raise OSError(errno.EIO, "Short workspace write")
                    written += count
                os.fdatasync(fd)
            except BaseException:
                os.ftruncate(fd, item["offset"])
                raise
            finally:
                os.close(fd)
            item["offset"] += len(payload)
            item["updated"] = time.time()
            with self.db:
                self.db.execute(
                    "INSERT INTO chunks VALUES (?,?,?,?)", (tid, offset, len(payload), digest)
                )
                self.db.execute("UPDATE transfers SET body=? WHERE id=?", (json.dumps(item), tid))
            self.hashers[tid].update(payload)
            return item

    def commit(self, tid):
        """Проверьте SHA-256 и атомарно опубликуйте единственный staging inode."""
        with self.lock:
            item = self.get(tid)
            if item["state"] == "completed":
                return item
            if self.closed or item["state"] != "active" or item["direction"] != "upload":
                raise FileError(
                    "operation_outcome_unknown" if item["state"] == "unknown" else "conflict",
                    resync=True,
                )
            if item["offset"] != item["size_bytes"]:
                raise FileError("conflict")
            fd = os.open(tid, os.O_RDONLY | os.O_NOFOLLOW, dir_fd=self.staging)
            try:
                digest = self.hashers[tid].hexdigest()
                if item["sha256"] is not None and digest != item["sha256"]:
                    self.cancel(tid, "failed")
                    raise FileError("checksum_mismatch")
                os.fchown(fd, -1, os.fstat(self.root).st_gid)
                os.fchmod(fd, 0o660)
                os.fsync(fd)
            finally:
                os.close(fd)
            parent, _, name = item["path"].rpartition("/")
            target = open_entry(self.root, parent, os.O_RDONLY | os.O_DIRECTORY)
            try:
                if identity(os.fstat(target)) != item["parent_identity"]:
                    raise FileError("conflict", resync=True)
                rename_noreplace(self.staging, tid, target, name)
                os.fsync(target)
                os.fsync(self.staging)
                item["version"] = entry_version(os.stat(name, dir_fd=target, follow_symlinks=False))
            finally:
                os.close(target)
            self.hashers.pop(tid, None)
            item.update(state="completed", sha256=digest, updated=time.time())
            self.save(item)
            return item

    def cancel(self, tid, state="cancelled"):
        """Прервите чтение и удалите staging, подтвердив очистку на диске."""
        with self.lock:
            row = self.db.execute(
                "SELECT body FROM transfers WHERE id=? AND generation=?", (tid, self.generation)
            ).fetchone()
            if row is None:
                raise FileError("not_found")
            item = json.loads(row[0])
            if item["state"] == "completed":
                return item
            if item["direction"] == "upload":
                try:
                    os.unlink(tid, dir_fd=self.staging)
                    self.cleaned_staging += 1
                except FileNotFoundError:
                    pass
                os.fsync(self.staging)
            self.hashers.pop(tid, None)
            active = self.downloads.pop(tid, None)
            if active is not None:
                os.close(active)
            item.update(state=state, updated=time.time())
            self.save(item)
            return item

    def open_download(self, tid):
        """Откройте стабильный regular file, удерживая descriptor до конца HTTP."""
        with self.lock:
            item = self.get(tid)
            if (
                self.closed
                or item["direction"] != "download"
                or item["state"] not in ("active", "completed")
            ):
                raise FileError("conflict")
            if tid in self.downloads:
                raise FileError("conflict")
            if item["state"] == "completed":
                active = self.db.execute(
                    "SELECT body FROM transfers WHERE generation=?", (self.generation,)
                ).fetchall()
                if sum(json.loads(row[0])["state"] == "active" for row in active) >= 2:
                    raise FileError("rate_limited")
            fd = open_entry(self.root, item["path"], os.O_RDONLY | os.O_NOFOLLOW)
            if (
                not stat.S_ISREG(os.fstat(fd).st_mode)
                or entry_version(os.fstat(fd)) != item["version"]
            ):
                os.close(fd)
                raise FileError("file_changed", resync=True)
            item.update(state="active", updated=time.time())
            self.save(item)
            self.downloads[tid] = fd
            return item

    def read(self, tid, offset, length):
        """Прочитайте ограниченный блок из удерживаемого descriptor."""
        with self.lock:
            item = self.get(tid)
            fd = self.downloads.get(tid)
            if self.closed or fd is None:
                raise FileError("workspace_closed" if self.closed else "conflict")
            if entry_version(os.fstat(fd)) != item["version"]:
                raise FileError("file_changed", resync=True)
            data = os.pread(fd, min(length, CHUNK), offset)
            if entry_version(os.fstat(fd)) != item["version"]:
                raise FileError("file_changed", resync=True)
            return data

    def acknowledge_download(self, tid, offset):
        """Зафиксируйте прогресс после передачи блока в HTTP, а не при его чтении."""
        with self.lock:
            item = self.get(tid)
            if self.closed or tid not in self.downloads or item["state"] != "active":
                raise FileError("workspace_closed" if self.closed else "conflict")
            item.update(offset=offset, updated=time.time())
            self.save(item)
            return item

    def finish_download(self, tid, complete):
        """Освободите descriptor, завершение означает чтение всего HTTP-потока."""
        with self.lock:
            fd = self.downloads.pop(tid, None)
            if fd is not None:
                os.close(fd)
            item = self.get(tid)
            if complete and item["state"] == "active":
                item.update(state="completed", updated=time.time())
                self.save(item)
            return item
