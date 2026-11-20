"""Обслуживайте внутренние команды и потоки workspace через закрытый HTTP API."""

import asyncio
from urllib.parse import quote

from fastapi import Query, Request
from fastapi.responses import JSONResponse, Response, StreamingResponse

from .models import AgentError
from .workspace_service import WorkspaceService
from .workspace_transfers import CHUNK, FileError, Transfers, filesystem_error, public_transfer


def byte_range(value, size):
    """Разберите один HTTP byte range, включая suffix и offsets больше 2 ГиБ."""
    if not value.startswith("bytes=") or "," in value:
        raise ValueError("Invalid range")
    first, last = value[6:].split("-", 1)
    if (first and (not first.isascii() or not first.isdigit())) or (
        last and (not last.isascii() or not last.isdigit())
    ):
        raise ValueError("Invalid range digits")
    if not first:
        suffix = int(last)
        if suffix <= 0 or size == 0:
            raise ValueError("Invalid suffix range")
        return max(0, size - suffix), size - 1
    start = int(first)
    end = int(last) if last else size - 1
    if start < 0 or start >= size or end < start:
        raise ValueError("Unsatisfiable range")
    return start, min(end, size - 1)


def install_workspace(app):
    """Подключите маршруты к существующему Bearer/mTLS guard приложения."""

    @app.exception_handler(FileError)
    async def workspace_error(_request: Request, error: FileError):
        """Верните безопасную ошибку файлового входа до начала HTTP-потока."""
        status = (
            410
            if error.code == "workspace_closed"
            else 503
            if error.code == "runtime_not_ready"
            else 409
        )
        return JSONResponse(
            {
                "error": {
                    "code": error.code,
                    "message": error.code,
                    "retryable": error.code in ("runtime_not_ready", "rate_limited"),
                    "resync_required": error.resync,
                }
            },
            status_code=status,
        )

    async def service():
        """Верните файловый сервис только работающей назначенной сессии."""
        agent = app.state.agent
        session = agent.live()
        if agent.settings.workspace_path is None:
            raise AgentError(503, "runtime_not_ready")
        async with agent.file_start:
            if agent.workspace is None:
                transfers = await asyncio.to_thread(
                    Transfers, agent.settings.workspace_path, agent.settings.state_dir, session.id
                )
                agent.workspace = WorkspaceService(agent, session, transfers)
        agent.workspace.check()
        return agent.workspace

    @app.get("/workspace/metrics")
    async def metrics():
        """Верните Prometheus-метрики файлов без пользовательских labels."""
        svc = app.state.agent.workspace
        if svc is None:
            return Response("cellestial_files_active_sockets 0\n", media_type="text/plain")
        active = await asyncio.to_thread(svc.transfers.active_count)
        rows = [
            f"cellestial_files_active_transfers {active}",
            f"cellestial_files_resync_total {svc.watch.counts['resync_required']}",
            f"cellestial_files_watcher_pending_bytes {svc.watch.pending_size}",
            f"cellestial_files_active_sockets {len(svc.clients)}",
            f"cellestial_files_event_queue_bytes {svc.watch.bytes}",
            f"cellestial_files_event_queue_length {len(svc.watch.events)}",
            f"cellestial_files_cleanup_total {svc.cleanup_count}",
        ]
        for direction, size in svc.bytes.items():
            rows.append(f'cellestial_files_bytes_total{{direction="{direction}"}} {size}')
        for code, count in svc.errors.items():
            rows.append(f'cellestial_files_errors_total{{code="{code}"}} {count}')
        for kind, count in svc.operations.items():
            rows.append(f'cellestial_files_operations_total{{operation="{kind}"}} {count}')
            rows.append(
                f'cellestial_files_operation_seconds_sum{{operation="{kind}"}} {svc.duration[kind]}'
            )
        return Response("\n".join(rows) + "\n", media_type="text/plain; version=0.0.4")

    @app.get("/workspace/check")
    async def check():
        """Проверьте доступность без чтения дерева и продления активности."""
        svc = await service()
        return {"workspace_generation": svc.session.id, "ready": True}

    @app.post("/workspace/command")
    async def command(request: Request):
        """Выполните одну управляющую команду без удержания файлового payload."""
        try:
            body = await request.json()
        except (ValueError, UnicodeError):
            return JSONResponse({"error": {"code": "invalid_path"}}, status_code=400)
        if len(await request.body()) > 65536:
            return Response(status_code=413)
        # HTTP subscribe хранит набор на стороне BFF через отдельный идентификатор socket.
        owner = request.headers.get("x-workspace-socket", "")
        svc = await service()
        if not owner or len(owner) > 64:
            return Response(status_code=400)
        state = svc.clients.get(owner)
        tracks_socket = isinstance(body, dict) and body.get("type") not in (
            "upload_status",
            "transfer_status",
            "transfer_cancel",
            "upload_commit",
        )
        if state is None and tracks_socket:
            if len(svc.clients) >= 4:
                return JSONResponse({"error": {"code": "rate_limited"}}, status_code=429)
            state = svc.clients[owner] = {
                "paths": set(),
                "updated": asyncio.get_running_loop().time(),
            }
        if state is not None:
            state["updated"] = asyncio.get_running_loop().time()
        result = await svc.command(body, state["paths"] if state else set())
        return JSONResponse(result)

    @app.get("/workspace/events")
    async def events(
        request: Request,
        after: int = Query(default=0, ge=0, le=2**63 - 1),
    ):
        """Дождитесь событий максимум 15 секунд, не продлевая idle-timeout."""
        svc = await service()
        owner = request.headers.get("x-workspace-socket", "")
        client = svc.clients.get(owner)
        if client is None:
            return JSONResponse(
                {
                    "error": {
                        "code": "resync_required",
                        "message": "Subscription state lost",
                        "retryable": True,
                        "resync_required": True,
                    }
                },
                status_code=409,
            )
        client["updated"] = asyncio.get_running_loop().time()
        result = svc.watch.replay(after)
        if not result:
            svc.watch.wakeup.clear()
            try:
                await asyncio.wait_for(svc.watch.wakeup.wait(), 15)
            except TimeoutError:
                pass
            result = svc.watch.replay(after)
        return {"events": result, "seq": result[-1]["seq"] if result else after}

    @app.delete("/workspace/socket")
    async def disconnect(request: Request):
        """Освободите подписки закрытого управляющего соединения."""
        svc = await service()
        svc.drop_client(request.headers.get("x-workspace-socket", ""))
        return Response(status_code=204)

    @app.post("/workspace/transfers/{tid}/chunks")
    async def chunk(request: Request, tid: str, offset: int = Query(ge=0, le=3 * 1024**3)):
        """Примите один бинарный chunk с ограниченным размером тела."""
        svc = await service()
        payload = bytearray()
        async for part in request.stream():
            if len(payload) + len(part) > CHUNK:
                return Response(status_code=413)
            payload.extend(part)
        try:
            result = await asyncio.to_thread(svc.transfers.chunk, tid, offset, bytes(payload))
            svc.progress(tid, len(payload))
            svc.watch.emit("transfer_updated", transfer=public_transfer(result))
            return JSONResponse(public_transfer(result))
        except FileError as exc:
            svc.errors[exc.code] = svc.errors.get(exc.code, 0) + 1
            return JSONResponse({"error": {"code": exc.code}}, status_code=409)
        except OSError as exc:
            return JSONResponse({"error": {"code": filesystem_error(exc).code}}, status_code=409)

    @app.get("/workspace/transfers/{tid}/download")
    async def download(request: Request, tid: str):
        """Передайте файл из удерживаемого descriptor в ограниченных порциях."""
        svc = await service()
        try:
            item = await asyncio.to_thread(svc.transfers.open_download, tid)
        except (OSError, FileError) as exc:
            code = exc.code if isinstance(exc, FileError) else filesystem_error(exc).code
            return JSONResponse({"error": {"code": code}}, status_code=409)
        size = item["size_bytes"]
        start, end, status = 0, size - 1, 200
        etag = '"' + item["version"] + '"'
        value = request.headers.get("range")
        if value and request.headers.get("if-range", etag) == etag:
            try:
                start, end = byte_range(value, size)
            except (ValueError, OverflowError):
                await asyncio.to_thread(svc.transfers.finish_download, tid, False)
                return Response(status_code=416, headers={"Content-Range": f"bytes */{size}"})
            status = 206
        name = item["path"].rpartition("/")[2]
        ascii_name = "".join(c if 32 <= ord(c) < 127 and c not in '\\"' else "_" for c in name)
        headers = {
            "Content-Type": "application/octet-stream",
            "X-Content-Type-Options": "nosniff",
            "Content-Disposition": (
                f"attachment; filename=\"{ascii_name}\"; filename*=UTF-8''{quote(name, safe='')}"
            ),
            "Content-Length": str(end - start + 1),
            "Accept-Ranges": "bytes",
            "ETag": etag,
            "Cache-Control": "private, no-store",
        }
        if status == 206:
            headers["Content-Range"] = f"bytes {start}-{end}/{size}"

        async def stream():
            """Применяйте backpressure ASGI и закройте descriptor при отмене."""
            offset, complete = start, False
            try:
                while offset <= end:
                    svc.check()
                    body = await asyncio.to_thread(
                        svc.transfers.read, tid, offset, min(CHUNK, end - offset + 1)
                    )
                    if not body:
                        raise FileError("file_changed", resync=True)
                    yield body
                    offset += len(body)
                    await asyncio.to_thread(svc.transfers.acknowledge_download, tid, offset)
                    svc.progress(tid, len(body), "download")
                    svc.watch.emit(
                        "transfer_updated", transfer=await asyncio.to_thread(svc.transfers.get, tid)
                    )
                complete = True
            except FileError as exc:
                svc.watch.emit(
                    "transfer_error", transfer_id=tid, code=exc.code, resync_required=exc.resync
                )
                raise
            finally:
                svc.active_progress.pop(tid, None)
                with __import__("contextlib").suppress(FileError):
                    item = await asyncio.shield(
                        asyncio.to_thread(svc.transfers.finish_download, tid, complete)
                    )
                    svc.watch.emit("transfer_updated", transfer=public_transfer(item))

        return StreamingResponse(stream(), status_code=status, headers=headers)
