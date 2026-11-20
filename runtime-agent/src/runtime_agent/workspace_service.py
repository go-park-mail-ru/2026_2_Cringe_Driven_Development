"""Свяжите файловые команды, durable transfers и события одной runtime-сессии."""

import asyncio
import errno
import os
import time
from uuid import UUID

from pydantic import ConfigDict, Field, StrictInt, ValidationError, model_validator

from .lifecycle import complete_before_cancel
from .models import AgentError, Command
from .observability import log
from .workspace_listing import DirectoryChanged, list_workspace
from .workspace_mutations import EntryChanged, make_directory, mutate_entry
from .workspace_paths import validate_path
from .workspace_transfers import FileError, filesystem_error, public_transfer
from .workspace_watch import WorkspaceWatch

_FIELDS = {
    "list": {"path", "limit", "cursor"},
    "subscribe": {"paths", "after"},
    "unsubscribe": {"paths"},
    "mkdir": {"path", "operation_id"},
    "rename": {"path", "name", "version", "operation_id"},
    "delete": {"path", "version", "recursive", "operation_id"},
    "upload_begin": {"path", "size_bytes", "sha256", "operation_id"},
    "download_begin": {"path", "operation_id"},
    "upload_commit": {"transfer_id"},
    "upload_status": {"transfer_id"},
    "transfer_status": {"transfer_id"},
    "transfer_cancel": {"transfer_id"},
}


class FileCommand(Command):
    """Проверяйте envelope и поля каждой команды версионированного протокола."""

    model_config = ConfigDict(extra="forbid", strict=True)
    protocol_version: StrictInt = Field(ge=1, le=1)
    type: str = Field(max_length=32)
    request_id: str = Field(min_length=1, max_length=64, pattern="^[a-zA-Z0-9._-]+$")
    session_id: str = Field(max_length=36)
    workspace_generation: str = Field(max_length=36)
    operation_id: str | None = Field(default=None, max_length=36)
    path: str = Field(default="", max_length=4095)
    name: str | None = Field(default=None, max_length=255)
    version: str | None = Field(default=None, max_length=64)
    limit: StrictInt = Field(default=100, ge=1, le=500)
    cursor: str | None = Field(default=None, max_length=36000)
    paths: list[str] = Field(default_factory=list, max_length=64)
    after: StrictInt | None = Field(default=None, ge=0, le=2**63 - 1)
    recursive: bool = False
    size_bytes: StrictInt | None = Field(default=None, ge=0)
    sha256: str | None = Field(default=None, pattern="^[a-fA-F0-9]{64}$")
    transfer_id: str | None = Field(default=None, max_length=36)

    @model_validator(mode="after")
    def validate_command(self):
        """Отклоните лишние и отсутствующие критические параметры."""
        common = {"protocol_version", "type", "request_id", "session_id", "workspace_generation"}
        if self.type not in _FIELDS or self.model_fields_set - common - _FIELDS[self.type]:
            raise ValueError("Invalid command fields")
        UUID(self.session_id)
        UUID(self.workspace_generation)
        validate_path(self.path)
        for path in self.paths:
            validate_path(path)
        if self.type in ("mkdir", "rename", "delete", "upload_begin", "download_begin"):
            if not self.operation_id or not self.path:
                raise ValueError("Operation ID and path required")
            UUID(self.operation_id)
        if self.type in ("rename", "delete") and not self.version:
            raise ValueError("Expected version required")
        if self.type == "rename" and (not self.name or "/" in self.name):
            raise ValueError("Destination name required")
        if self.type == "rename":
            assert self.name is not None
            validate_path(self.name)
        if self.type == "upload_begin" and self.size_bytes is None:
            raise ValueError("File size required")
        if "transfer_id" in _FIELDS[self.type]:
            if not self.transfer_id:
                raise ValueError("Transfer ID required")
            UUID(self.transfer_id)
        return self


class WorkspaceService:
    """Обслуживайте один workspace, не удерживая файлы в памяти приложения."""

    def __init__(self, agent, session, transfers):
        """Откройте transfer state текущей сессии и запустите ограниченную очистку."""
        self.agent = agent
        self.session = session
        self.transfers = transfers
        self.watch = WorkspaceWatch(self.transfers)
        self.mutations = asyncio.Lock()
        self.clients = {}
        self.active_progress = {}
        self.closed = False
        self.cleaned = False
        self.bytes = {"upload": 0, "download": 0}
        self.errors = {}
        self.operations = {}
        self.duration = {}
        self.last_quota = None
        self.cleanup_count = 0
        self.maintenance = asyncio.create_task(self.maintain(), name="workspace-maintenance")

    def check(self):
        """Проверьте актуальную сессию на каждой команде и порции байтов."""
        current = self.agent.session
        if self.closed or current is not self.session or current.closed or self.agent.stopping:
            raise FileError("workspace_closed")
        if not self.agent.ready or not current.ready or current.draining:
            raise FileError("runtime_not_ready")

    def drop_client(self, owner):
        """Освободите подписки отключившегося клиента."""
        client = self.clients.pop(owner, None)
        if client:
            for path in client["paths"]:
                self.watch.unsubscribe(path)

    def progress(self, tid, count=0, direction="upload"):
        """Продлите активность только после реального переноса байтов."""
        self.bytes[direction] += count
        self.active_progress[tid] = time.monotonic()
        self.session.touch()

    def busy(self):
        """Удерживайте idle lease только при недавнем прогрессе активной передачи."""
        return any(time.monotonic() - timestamp < 60 for timestamp in self.active_progress.values())

    async def maintain(self):
        """Очищайте staging TTL и явно сигнализируйте деградацию watcher."""
        while True:
            await asyncio.sleep(2)
            await asyncio.to_thread(self.transfers.expire)
            for owner, client in list(self.clients.items()):
                if asyncio.get_running_loop().time() - client["updated"] > 60:
                    self.drop_client(owner)
            quota = await asyncio.to_thread(self.transfers.quota)
            if quota != self.last_quota:
                self.last_quota = quota
                self.watch.emit("quota_changed", quota=quota)
            if self.watch.degraded:
                restored = [self.watch.refresh(path) for path in self.watch.paths]
                self.watch.degraded = self.watch.fd < 0 or not all(restored)
                for path in self.watch.paths:
                    self.watch.emit(
                        "directory_invalidated",
                        path=path,
                        synchronization="degraded" if self.watch.degraded else "live",
                    )

    async def close_workspace(self):
        """Закройте доступ, прервите transfers и очистите данные после остановки kernel."""
        if self.cleaned:
            return
        if not self.closed:
            self.closed = True
            self.watch.emit("workspace_closed")
        self.maintenance.cancel()
        await asyncio.gather(self.maintenance, return_exceptions=True)
        await self.watch.close()
        self.clients.clear()
        async with self.mutations:
            await asyncio.to_thread(self._cleanup)
            with self.agent.journal.db:
                self.agent.journal.db.execute(
                    "DELETE FROM workspace_operations WHERE session_id=?", (self.session.id,)
                )
            self.cleaned = True

    def _cleanup(self):
        """Удалите файлы относительно открытых дескрипторов без обхода ссылок."""
        import stat

        from .workspace_mutations import _remove_tree

        with self.transfers.lock:
            self.transfers.closed = True
            self.cleanup_count += 1
            for tid in list(self.transfers.downloads):
                self.transfers.cancel(tid)
            for fd in (self.transfers.root, self.transfers.staging):
                os.lseek(fd, 0, os.SEEK_SET)
                with os.scandir(fd) as entries:
                    for child in entries:
                        if stat.S_ISDIR(child.stat(follow_symlinks=False).st_mode):
                            _remove_tree(fd, child.name)
                        else:
                            os.unlink(child.name, dir_fd=fd)
                os.fsync(fd)
            with self.transfers.db:
                self.transfers.db.execute("DELETE FROM chunks")
                self.transfers.db.execute("DELETE FROM transfers")

    async def dispose(self):
        """Освободите файловые ресурсы при завершении процесса."""
        self.maintenance.cancel()
        await asyncio.gather(self.maintenance, return_exceptions=True)
        await self.watch.close()
        await asyncio.to_thread(self.transfers.close)

    async def command(self, value, subscribed):
        """Проверьте envelope, выполните команду и верните коррелированный ответ."""
        started = time.monotonic()
        request_id = value.get("request_id") if isinstance(value, dict) else None
        if not isinstance(request_id, str) or len(request_id) > 64:
            request_id = None
        try:
            command = FileCommand.model_validate(value)
            self.check()
            if (
                command.session_id != self.session.id
                or command.workspace_generation != self.session.id
            ):
                raise FileError("workspace_closed", resync=True)
            result = await self.execute(command, subscribed)
            self.operations[command.type] = self.operations.get(command.type, 0) + 1
            self.duration[command.type] = (
                self.duration.get(command.type, 0.0) + time.monotonic() - started
            )
            return dict(protocol_version=1, type="response", request_id=request_id, result=result)
        except (ValidationError, ValueError):
            error = FileError("invalid_path")
        except DirectoryChanged:
            error = FileError("resync_required", resync=True)
        except EntryChanged:
            error = FileError("conflict", resync=True)
        except OSError as exc:
            error = filesystem_error(exc)
            error.resync = True
        except FileError as exc:
            error = exc
        except AgentError as exc:
            error = FileError(
                exc.detail, resync=exc.detail in ("conflict", "operation_outcome_unknown")
            )
        except Exception:
            log.error(
                "workspace.command.failed", extra={"event_fields": {"request_id": request_id}}
            )
            error = FileError("internal", resync=True)
        self.errors[error.code] = self.errors.get(error.code, 0) + 1
        identifiers = {}
        if isinstance(value, dict):
            for key in ("operation_id", "transfer_id"):
                candidate = value.get(key)
                if isinstance(candidate, str) and len(candidate) <= 36:
                    try:
                        identifiers[key] = str(UUID(candidate))
                    except ValueError:
                        pass
        return dict(
            protocol_version=1,
            type="response",
            request_id=request_id,
            error={
                "code": error.code,
                "message": error.code,
                "retryable": error.code in ("rate_limited", "runtime_not_ready"),
                "resync_required": error.resync,
                **identifiers,
            },
        )

    async def execute(self, command, subscribed):
        """Выполните проверенную команду текущего workspace."""
        kind = command.type
        if kind == "list":
            snapshot_seq = self.watch.seq
            try:
                result = await asyncio.to_thread(
                    list_workspace,
                    self.agent.settings.workspace_path,
                    self.session.id,
                    self.agent.token,
                    command.path,
                    command.limit,
                    command.cursor,
                )
            except OSError as exc:
                if exc.errno in (errno.ENOENT, errno.ENOTDIR):
                    raise FileError("directory_invalidated", resync=True) from exc
                raise
            result["seq"] = snapshot_seq
            result["quota"] = await asyncio.to_thread(self.transfers.quota)
            return result
        if kind == "subscribe":
            added = set(command.paths) - subscribed
            if len(subscribed | added) > 64:
                raise FileError("rate_limited")
            registered = []
            try:
                for path in added:
                    self.watch.subscribe(path)
                    subscribed.add(path)
                    registered.append(path)
                for path in command.paths:
                    self.watch.refresh(path)
                snapshot_seq = self.watch.seq
                snapshots = []
                for path in command.paths:
                    snapshot = await self.execute(
                        FileCommand(
                            protocol_version=1,
                            type="list",
                            request_id=command.request_id,
                            session_id=self.session.id,
                            workspace_generation=self.session.id,
                            path=path,
                            limit=1,
                        ),
                        subscribed,
                    )
                    snapshots.append(
                        {
                            "path": path,
                            "revision": snapshot["revision"],
                            "workspace_generation": self.session.id,
                        }
                    )
                return {
                    "snapshots": snapshots,
                    "seq": snapshot_seq,
                    "events": self.watch.replay(
                        command.after if command.after is not None else snapshot_seq
                    ),
                    "synchronization": "degraded" if self.watch.degraded else "live",
                }
            except (OSError, DirectoryChanged, FileError, ValueError):
                for path in registered:
                    self.watch.unsubscribe(path)
                    subscribed.discard(path)
                raise

        if kind == "unsubscribe":
            for path in set(command.paths) & subscribed:
                self.watch.unsubscribe(path)
                subscribed.discard(path)
            return {}
        if kind in ("upload_status", "transfer_status"):
            return public_transfer(await asyncio.to_thread(self.transfers.get, command.transfer_id))
        if kind in ("upload_commit", "transfer_cancel"):
            self.session.touch()
            call = self.transfers.commit if kind == "upload_commit" else self.transfers.cancel
            result = await asyncio.to_thread(call, command.transfer_id)
            self.active_progress.pop(command.transfer_id, None)
            self.watch.emit("transfer_updated", transfer=public_transfer(result))
            self.watch.emit("quota_changed", quota=await asyncio.to_thread(self.transfers.quota))
            if kind == "upload_commit":
                self.watch.emit("directory_invalidated", path=result["path"].rpartition("/")[0])
            return public_transfer(result)
        async with self.mutations:
            self.check()
            parameters = command.model_dump(mode="json", exclude={"request_id", "operation_id"})
            result = self.agent.journal.reserve_workspace_operation(
                self.session.id, command.operation_id, parameters
            )
            if result is not None:
                if "_error" in result:
                    raise FileError(result["_error"], resync=result.get("_resync", True))
                return result
            self.session.touch()
            try:
                if kind in ("upload_begin", "download_begin"):
                    result = await complete_before_cancel(
                        asyncio.to_thread(
                            self.transfers.begin,
                            "upload" if kind == "upload_begin" else "download",
                            command.path,
                            command.size_bytes,
                            command.sha256.lower() if command.sha256 else None,
                        )
                    )
                elif kind == "mkdir":
                    await complete_before_cancel(
                        asyncio.to_thread(make_directory, self.transfers.root, command.path)
                    )
                    result = {"operation_id": command.operation_id}
                else:
                    await complete_before_cancel(
                        asyncio.to_thread(
                            mutate_entry,
                            self.transfers.root,
                            self.transfers.staging,
                            command.operation_id,
                            command.path,
                            command.version,
                            command.name if kind == "rename" else None,
                            command.recursive,
                        )
                    )
                    result = {"operation_id": command.operation_id}
            except (OSError, EntryChanged, FileError) as exc:
                code = (
                    filesystem_error(exc).code
                    if isinstance(exc, OSError)
                    else "conflict"
                    if isinstance(exc, EntryChanged)
                    else exc.code
                )
                self.agent.journal.finish_workspace_operation(
                    self.session.id,
                    command.operation_id,
                    {
                        "_error": code,
                        "_resync": isinstance(exc, (OSError, EntryChanged))
                        or getattr(exc, "resync", False),
                    },
                )
                self.watch.emit("directory_invalidated", path=command.path.rpartition("/")[0])
                raise
            if kind in ("upload_begin", "download_begin"):
                result = public_transfer(result)
            if kind in ("mkdir", "rename", "delete"):
                try:
                    page = await self.execute(
                        FileCommand(
                            protocol_version=1,
                            type="list",
                            request_id=command.request_id,
                            session_id=self.session.id,
                            workspace_generation=self.session.id,
                            path=command.path.rpartition("/")[0],
                            limit=1,
                        ),
                        subscribed,
                    )
                    result["revision"] = page["revision"]
                    result["resulting_seq"] = page["seq"]
                except (OSError, DirectoryChanged):
                    result["resync_required"] = True
            self.agent.journal.finish_workspace_operation(
                self.session.id, command.operation_id, result
            )
            self.watch.emit(
                "directory_invalidated",
                path=command.path.rpartition("/")[0],
                operation_id=command.operation_id,
            )
            self.watch.emit("quota_changed", quota=await asyncio.to_thread(self.transfers.quota))
            return result
