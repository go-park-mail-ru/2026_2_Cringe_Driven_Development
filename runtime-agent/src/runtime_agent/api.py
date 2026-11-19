"""Обслуживайте команды агента и передавайте события через WebSocket."""

import asyncio
import hmac
from contextlib import asynccontextmanager

import anyio
from fastapi import FastAPI, Query, WebSocket, WebSocketDisconnect
from fastapi.responses import JSONResponse

from .config import Settings
from .containers import Containers
from .journal import Journal
from .lifecycle import complete_before_cancel
from .models import AgentError, Assign, Execute, Inspect, StopExecutions
from .observability import AccessLog
from .runtime import Agent
from .workspace_api import install_workspace


class HTTPGuard:
    """Проверьте доступ и размер тела до разбора JSON.

    Назначение защищает mTLS-прокси. До назначения GET /health сообщает
    готовность, остальные команды получают 503.
    """

    def __init__(self, app, token):
        """Подключите ASGI-приложение и источник токена.

        Args:
            app: Следующее приложение ASGI.
            token: Функция чтения токена или None до назначения.
        """
        self.app = app
        self.token = token

    async def __call__(self, scope, receive, send):
        """Проверьте Bearer и ограничьте HTTP-тело двумя МиБ.

        Args:
            scope: Структура запроса ASGI.
            receive: Получатель событий запроса.
            send: Отправитель ответа.
        """
        if scope["type"] != "http":
            return await self.app(scope, receive, send)
        token = self.token()
        path = scope["path"]
        if path != "/assign" and not (token is None and path == "/health"):
            if token is None:
                return await JSONResponse({"detail": "Runtime is not assigned"}, 503)(
                    scope, receive, send
                )
            expected = ("Bearer " + token).encode("ascii")
            if not hmac.compare_digest(dict(scope["headers"]).get(b"authorization", b""), expected):
                return await JSONResponse({"detail": "Invalid runtime token"}, 401)(
                    scope, receive, send
                )
        body = bytearray()
        while True:
            message = await receive()
            if message["type"] == "http.disconnect":
                return
            body.extend(message.get("body", b""))
            if len(body) > 2 * 1024 * 1024:
                return await JSONResponse({"detail": "Request body too large"}, 413)(
                    scope, receive, send
                )
            if not message.get("more_body", False):
                break
        consumed = False

        async def buffered_receive():
            """Отдайте накопленное тело один раз, затем читайте исходный канал."""
            nonlocal consumed
            if not consumed:
                consumed = True
                return {"type": "http.request", "body": bytes(body), "more_body": False}
            return await receive()

        await self.app(scope, buffered_receive, send)


def create_app(settings=None, containers_factory=Containers):
    """Соберите API и привяжите ресурсы к жизненному циклу.

    Args:
        settings: Настройки хоста или None для чтения окружения.
        containers_factory: Фабрика Docker-адаптера по настройкам и ID агента.
    """
    settings = settings or Settings.from_env()

    @asynccontextmanager
    async def lifespan(app):
        """Откройте journal, запустите агент и дождитесь очистки при остановке.

        Args:
            app: Приложение, в котором публикуется готовый агент.
        """
        journal = Journal(settings.state_dir)
        agent = None
        try:
            agent = Agent(settings, journal, containers_factory(settings, journal.instance_id))
            app.state.agent = agent
            await agent.start()
            yield
        finally:
            try:
                if agent:
                    await complete_before_cancel(agent.close())
            finally:
                journal.close()

    app = FastAPI(
        title="Cellestial runtime agent", lifespan=lifespan, docs_url=None, redoc_url=None
    )

    def current_token():
        """Верните текущий токен или None до назначения."""
        agent = getattr(app.state, "agent", None)
        return agent.token if agent is not None else None

    app.add_middleware(HTTPGuard, token=current_token)
    app.add_middleware(AccessLog)

    @app.exception_handler(AgentError)
    async def agent_error(request, exc):
        """Отправьте прикладную ошибку в формате API.

        Args:
            request: Входной HTTP-запрос FastAPI.
            exc: Ошибка с публичным сообщением и статусом.
        """
        return JSONResponse({"detail": exc.detail}, status_code=exc.status)

    @app.post("/assign")
    async def assign(body: Assign):
        """Назначьте готовый kernel запросившей сессии.

        Args:
            body: Проверенные UUID сессии, язык и токен.
        """
        snapshot = await app.state.agent.assign(str(body.session_id), body.language, body.token)
        return JSONResponse(snapshot, status_code=200 if snapshot["ready"] else 503)

    @app.get("/health")
    async def health():
        """Верните готовность, поколение kernel и состояние очереди."""
        snapshot = app.state.agent.snapshot()
        return JSONResponse(snapshot, status_code=200 if snapshot["ready"] else 503)

    @app.post("/runtime/activity")
    async def activity():
        """Обновите время последнего действия в активной сессии."""
        app.state.agent.live().touch()
        return app.state.agent.snapshot()

    @app.post("/executions", status_code=202)
    async def execute(body: Execute):
        """Примите команду или верните результат идемпотентного повтора.

        Args:
            body: Код, стабильный ID команды и лимит времени.
        """
        return app.state.agent.accept({**body.model_dump(), "kind": "execute"})

    @app.get("/executions/{execution_id:path}")
    async def execution(execution_id: str):
        """Верните состояние и доступный результат команды.

        Args:
            execution_id: Идентификатор команды в текущей сессии.
        """
        sid = app.state.agent.assigned().id
        app.state.agent.journal.session(sid)
        return app.state.agent.journal.execution(sid, execution_id)

    @app.post("/variables", status_code=202)
    async def variables(body: Inspect):
        """Поставьте инспекцию в очередь с лимитом исполнения пять секунд.

        Args:
            body: Стабильный ID команды инспекции.
        """
        return app.state.agent.accept(
            {
                "execution_id": body.execution_id,
                "kind": "inspect",
                "timeout_seconds": 5.0,
            },
        )

    @app.post("/executions/stop")
    async def stop_executions(body: StopExecutions):
        """Остановите принятые команды и сохраните работающий kernel.

        Args:
            body: Идентификатор операции и известные backend команды.
        """
        await app.state.agent.live().stop_executions(body.operation_id, body.execution_ids)
        return {"stopped": True}

    @app.post("/kernel/interrupt")
    async def interrupt():
        """Запросите прерывание выполняемой команды."""
        await app.state.agent.live().interrupt()
        return {"interrupt_requested": True}

    @app.post("/kernel/restart")
    async def restart():
        """Перезапустите свободный kernel и очистите его память."""
        runtime = app.state.agent.live()
        await runtime.restart()
        return {"generation": runtime.generation}

    @app.post("/runtime/drain")
    async def drain():
        """Запретите новые команды и прервите текущее исполнение."""
        await app.state.agent.live().drain()
        return {"draining": True}

    @app.post("/runtime/stop")
    async def stop():
        """Остановите сессию и верните результат очистки ресурсов."""
        snapshot = await app.state.agent.stop_session()
        return JSONResponse(
            {
                "stopped": snapshot["state"] in ("STOPPED", "FAILED"),
                "generation": snapshot["generation"],
            },
            status_code=503 if snapshot["state"] == "DRAINING" else 200,
        )

    @app.get("/events")
    async def events(after: int = Query(default=0, ge=0, le=2**63 - 1)):
        """Верните страницу событий после курсора.

        Args:
            after: Исключающий номер последнего прочитанного события.
        """
        sid = app.state.agent.assigned().id
        app.state.agent.journal.session(sid)
        return {"events": app.state.agent.journal.events(sid, after)}

    @app.websocket("/events/ws")
    async def stream(ws: WebSocket):
        """Проверьте доступ WebSocket и передайте журнал и новые события.

        Args:
            ws: Соединение клиента с заголовком токена и курсором.
        """
        agent = app.state.agent
        if agent.token is None or not hmac.compare_digest(
            ws.headers.get("authorization", "").encode(), ("Bearer " + agent.token).encode()
        ):
            await ws.close(code=1008)
            return
        sid = agent.assigned().id
        try:
            after = int(ws.query_params.get("after", "0"))
            if not 0 <= after <= 2**63 - 1:
                raise ValueError()
            agent.journal.session(sid)
        except (ValueError, AgentError):
            await ws.close(code=1008)
            return
        await ws.accept()
        wakeup = asyncio.Event()
        agent.subscribers.add(wakeup)

        async def replay():
            """Отправляйте страницы журнала, продвигая курсор соединения."""
            nonlocal after
            while True:
                wakeup.clear()
                batch = agent.journal.events(sid, after)
                if batch:
                    for event in batch:
                        await asyncio.wait_for(ws.send_json(event), 10)
                        after = event["seq"]
                    # Отдаём управление циклу, даже если in-process клиент пишет мгновенно.
                    await asyncio.sleep(0)
                else:
                    try:
                        await asyncio.wait_for(wakeup.wait(), 15)
                    except TimeoutError:
                        await asyncio.wait_for(
                            ws.send_json({"kind": "heartbeat", "after": after}), 10
                        )

        async def disconnected():
            """Дождитесь отключения клиента WebSocket."""
            while (await ws.receive())["type"] != "websocket.disconnect":
                pass

        tasks = [asyncio.create_task(replay()), asyncio.create_task(disconnected())]
        try:
            done, _ = await asyncio.wait(tasks, return_when=asyncio.FIRST_COMPLETED)
            for task in done:
                task.result()
        except (WebSocketDisconnect, TimeoutError, RuntimeError, OSError):
            pass
        finally:
            agent.subscribers.discard(wakeup)
            for task in tasks:
                task.cancel()
            # ASGI-серверы могут отменить обработчик во время закрытия. Защищаем
            # очистку от повторной отмены, пока обе дочерние задачи не завершатся.
            with anyio.CancelScope(shield=True):
                await asyncio.gather(*tasks, return_exceptions=True)

    install_workspace(app)
    return app
