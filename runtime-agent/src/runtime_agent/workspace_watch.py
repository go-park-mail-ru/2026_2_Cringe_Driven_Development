"""Доставляйте ограниченный журнал событий Linux workspace."""

import asyncio
import ctypes
import json
import os
import struct
from collections import Counter, deque

from .lifecycle import complete_before_cancel
from .workspace_listing import DirectoryChanged, describe_entry, list_directory
from .workspace_paths import open_entry


class WorkspaceWatch:
    """Владейте inotify descriptors и ограниченным журналом текущего процесса."""

    def __init__(self, transfers):
        """Создайте watcher. Подписки устанавливаются до начального snapshot."""
        self.transfers = transfers
        self.closed = False
        self.overflows = 0
        self.events = deque()
        self.bytes = 0
        with transfers.lock, transfers.db:
            transfers.db.execute(
                "CREATE TABLE IF NOT EXISTS watch_sequence "
                "(id INTEGER PRIMARY KEY, ceiling INTEGER NOT NULL)"
            )
            row = transfers.db.execute("SELECT ceiling FROM watch_sequence WHERE id=1").fetchone()
            self.seq = row[0] if row else 0
            self.floor = self.seq
            self.ceiling = self.seq + 4096
            transfers.db.execute(
                "INSERT OR REPLACE INTO watch_sequence VALUES (1,?)", (self.ceiling,)
            )
        self.wakeup = asyncio.Event()
        self.paths = {}
        self.pending = {}
        self.pending_size = 0
        self.pending_count = 0
        self.burst_resync = False
        self.counts = Counter()
        self.changed = asyncio.Event()
        self.publisher = asyncio.create_task(self.publish(), name="workspace-events")
        self.libc = ctypes.CDLL(None, use_errno=True)
        self.fd = self.libc.inotify_init1(os.O_NONBLOCK | os.O_CLOEXEC)
        self.degraded = self.fd < 0
        if self.fd >= 0:
            asyncio.get_running_loop().add_reader(self.fd, self.read)

    def emit(self, kind, **fields):
        """Добавьте событие и вытесните старые записи при достижении бюджета."""
        if self.closed:
            return None
        if self.seq == self.ceiling:
            self.ceiling += 4096
            with self.transfers.lock, self.transfers.db:
                self.transfers.db.execute(
                    "UPDATE watch_sequence SET ceiling=? WHERE id=1", (self.ceiling,)
                )
        self.counts[kind] += 1
        self.seq += 1
        item = dict(
            protocol_version=1,
            type=kind,
            seq=self.seq,
            session_id=self.transfers.generation,
            workspace_generation=self.transfers.generation,
            **fields,
        )
        size = len(json.dumps(item).encode())
        self.events.append((item, size))
        self.bytes += size
        while len(self.events) > 4096 or self.bytes > 4 * 1024**2:
            self.bytes -= self.events.popleft()[1]
        self.wakeup.set()
        return item

    def replay(self, after):
        """Верните непрерывную историю либо запросите resync."""
        if (
            after < self.floor
            or after > self.seq
            or (self.events and after < self.events[0][0]["seq"] - 1)
        ):
            return [
                dict(
                    protocol_version=1,
                    session_id=self.transfers.generation,
                    type="resync_required",
                    seq=self.seq,
                    workspace_generation=self.transfers.generation,
                )
            ]
        result = []
        size = 0
        for event, length in self.events:
            if event["seq"] <= after:
                continue
            if result and (len(result) >= 256 or size + length > 512 * 1024):
                break
            result.append(event)
            size += length
        return result

    def subscribe(self, path):
        """Установите watch на открытый каталог и увеличьте число подписчиков."""
        if path in self.paths:
            self.paths[path][2] += 1
            return
        if len(self.paths) >= 256:
            from .workspace_transfers import FileError

            raise FileError("rate_limited")
        descriptor = open_entry(self.transfers.root, path, os.O_RDONLY | os.O_DIRECTORY)
        watch = -1
        if self.fd >= 0:
            watch = self.libc.inotify_add_watch(
                self.fd, f"/proc/self/fd/{descriptor}".encode(), 0x00000FCE | 0x01000000
            )
        if watch < 0:
            self.degraded = True
            self.emit("resync_required", path=path, synchronization="degraded")
        self.paths[path] = [descriptor, watch, 1]

    def refresh(self, path):
        """Переустановите watch, если pathname теперь обозначает другой каталог."""
        entry = self.paths[path]
        try:
            descriptor = open_entry(self.transfers.root, path, os.O_RDONLY | os.O_DIRECTORY)
        except OSError:
            self.degraded = True
            return False
        if entry[0] >= 0 and entry[1] >= 0:
            before, current = os.fstat(entry[0]), os.fstat(descriptor)
            if (before.st_dev, before.st_ino) == (current.st_dev, current.st_ino):
                os.close(descriptor)
                return True
        if entry[0] >= 0:
            os.close(entry[0])
        if self.fd >= 0 and entry[1] >= 0:
            self.libc.inotify_rm_watch(self.fd, entry[1])
        entry[0] = descriptor
        entry[1] = (
            self.libc.inotify_add_watch(
                self.fd, f"/proc/self/fd/{descriptor}".encode(), 0x00000FCE | 0x01000000
            )
            if self.fd >= 0
            else -1
        )
        if entry[1] < 0:
            self.degraded = True
            return False
        return True

    def unsubscribe(self, path):
        """Освободите watch после отключения последнего подписчика."""
        entry = self.paths.get(path)
        if entry is None:
            return
        entry[2] -= 1
        if entry[2]:
            return
        self.paths.pop(path)
        if self.fd >= 0 and entry[1] >= 0:
            self.libc.inotify_rm_watch(self.fd, entry[1])
        if entry[0] >= 0:
            os.close(entry[0])

    def read(self):
        """Соберите bounded изменения, не читая filesystem на event loop."""
        try:
            data = os.read(self.fd, 65536)
        except BlockingIOError:
            return
        position = 0
        while position + 16 <= len(data):
            watch, mask, _, length = struct.unpack_from("iIII", data, position)
            raw = data[position + 16 : position + 16 + length].split(b"\0", 1)[0]
            position += 16 + length
            if mask & 0x4000:
                self.overflows += 1
                self.emit("resync_required", reason="watcher_overflow")
            for path, (_, identifier, _) in self.paths.items():
                if identifier != watch:
                    continue
                changes = self.pending.setdefault(path, {})
                try:
                    name = raw.decode("utf-8")
                except UnicodeError:
                    name = ""
                if name in changes:
                    changes[name] |= mask
                elif self.pending_count < 4096 and self.pending_size + len(raw) < 1024**2:
                    changes[name] = mask
                    self.pending_count += 1
                    self.pending_size += len(raw)
                elif not self.burst_resync:
                    self.burst_resync = True
                    self.emit("resync_required", reason="watcher_burst")
                if mask & (0x400 | 0x800 | 0x8000):
                    self.degraded = True
                    entry = self.paths[path]
                    if entry[0] >= 0:
                        os.close(entry[0])
                    if self.fd >= 0 and entry[1] >= 0:
                        self.libc.inotify_rm_watch(self.fd, entry[1])
                    entry[0], entry[1] = -1, -1
                    for child in self.paths:
                        if child == path or child.startswith(path + "/"):
                            self.emit("directory_invalidated", path=child)
        if self.pending:
            self.changed.set()

    def describe_changes(self, path, changes):
        """Сверьте итоговую revision и метаданные изменившихся entries."""
        revision = list_directory(self.transfers.root, path, 1)["revision"]
        fd = open_entry(self.transfers.root, path, os.O_RDONLY | os.O_DIRECTORY)
        try:
            events = []
            for name, mask in changes.items():
                if not name:
                    events.append(("directory_invalidated", {"path": path, "revision": revision}))
                    continue
                entry_path = f"{path}/{name}" if path else name
                try:
                    info = os.stat(name, dir_fd=fd, follow_symlinks=False)
                except FileNotFoundError:
                    events.append(
                        ("removed", {"path": entry_path, "directory": path, "revision": revision})
                    )
                    continue
                kind = "created" if mask & (0x100 | 0x80) else "updated"
                events.append(
                    (
                        kind,
                        {
                            "path": entry_path,
                            "directory": path,
                            "revision": revision,
                            "entry": describe_entry(path, name, info, fd),
                        },
                    )
                )
            return events
        finally:
            os.close(fd)

    async def publish(self):
        """Объединяйте burst 150 мс и публикуйте итоговые данные вне передачи bytes."""
        while True:
            await self.changed.wait()
            await asyncio.sleep(0.15)
            self.changed.clear()
            pending, self.pending = self.pending, {}
            self.pending_count, self.pending_size = 0, 0
            self.burst_resync = False
            for path, changes in pending.items():
                try:
                    events = await complete_before_cancel(
                        asyncio.to_thread(self.describe_changes, path, changes)
                    )
                except (OSError, DirectoryChanged):
                    self.emit("directory_invalidated", path=path)
                    continue
                for kind, fields in events:
                    self.emit(kind, **fields)

    async def close(self):
        """Освободите reader и дождитесь завершения файлового чтения publisher."""
        if self.closed:
            return
        self.closed = True
        self.publisher.cancel()
        await asyncio.gather(self.publisher, return_exceptions=True)
        for descriptor, _, _ in self.paths.values():
            if descriptor >= 0:
                os.close(descriptor)
        self.paths.clear()
        if self.fd >= 0:
            asyncio.get_running_loop().remove_reader(self.fd)
            os.close(self.fd)
