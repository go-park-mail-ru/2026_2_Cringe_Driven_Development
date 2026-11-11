"""Expose authenticated HTTP commands and WebSocket event streams."""

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
from .models import AgentError, Assign, Execute, Inspect
from .runtime import Agent


class HTTPGuard:
    """Authorize before reading a body, and bound memory before JSON validation.

    POST /assign is authorized by the mTLS proxy only: Go API sends the session
    token in its body. Before the assignment GET /health is open as well, and
    other requests get 503 because no session token exists yet.

    Attributes:
        app (Callable): Downstream ASGI application.
        token (Callable[[], str | None]): Current session token, None before the
            assignment.
    """

    def __init__(self, app, token):
        """Configure the ASGI application and the session token source.

        Args:
            app (Callable): Downstream ASGI application.
            token (Callable[[], str | None]): Returns the current session token.
        """
        self.app = app
        self.token = token

    async def __call__(self, scope, receive, send):
        """Authorize HTTP requests and enforce a 2 MiB request body limit.

        Non-HTTP scopes pass through unchanged; WebSocket authentication is handled by its
        endpoint.

        Args:
            scope (dict): ASGI connection scope.
            receive (Callable): ASGI event receiver.
            send (Callable): ASGI response sender.
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
            """Deliver the buffered body once, then use the original receiver.

            Returns:
                dict: Next ASGI request or disconnect event.
            """
            nonlocal consumed
            if not consumed:
                consumed = True
                return {"type": "http.request", "body": bytes(body), "more_body": False}
            return await receive()

        await self.app(scope, buffered_receive, send)


def create_app(settings=None, containers_factory=Containers):
    """Build the private agent API with lifecycle-managed infrastructure.

    Args:
        settings (Settings | None): Configuration, or None to load environment
            variables.
        containers_factory (Callable): Factory accepting settings and an instance ID to
            create a container backend.

    Returns:
        FastAPI: Application with HTTP routes, middleware, and a WebSocket event
        endpoint.

    Raises:
        ValueError: Environment configuration is invalid.
    """
    settings = settings or Settings.from_env()

    @asynccontextmanager
    async def lifespan(app):
        """Acquire persistent state and manage agent startup and shutdown.

        Finish resource cleanup before closing the journal, even when startup
        or shutdown is cancelled repeatedly.

        Args:
            app (FastAPI): Application receiving its agent through app.state.

        Yields:
            None: Control while the started application serves requests.
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
        """Return the session token, or None before the assignment or startup."""
        agent = getattr(app.state, "agent", None)
        return agent.token if agent is not None else None

    app.add_middleware(HTTPGuard, token=current_token)

    @app.exception_handler(AgentError)
    async def agent_error(request, exc):
        """Translate an application error into its public JSON response.

        Args:
            request (Request): Request that raised the error; unused by this handler.
            exc (AgentError): Application error containing status and detail.

        Returns:
            JSONResponse: Error detail with the supplied HTTP status.
        """
        return JSONResponse({"detail": exc.detail}, status_code=exc.status)

    @app.post("/assign")
    async def assign(body: Assign):
        """Assign the agent to a session once; answer after the session kernel is ready.

        Args:
            body (Assign): Session ID, language and session token.

        Returns:
            JSONResponse: Session snapshot; HTTP 200 when ready, otherwise HTTP 503.
        """
        snapshot = await app.state.agent.assign(str(body.session_id), body.language, body.token)
        return JSONResponse(snapshot, status_code=200 if snapshot["ready"] else 503)

    @app.get("/health")
    async def health():
        """Report readiness and execution status for the assigned runtime.

        Returns:
            JSONResponse: Assignment snapshot including generation, active command
            and queue size; HTTP 200 when ready, otherwise HTTP 503.
        """
        snapshot = app.state.agent.snapshot()
        return JSONResponse(snapshot, status_code=200 if snapshot["ready"] else 503)

    @app.post("/runtime/activity")
    async def activity():
        """Refresh the activity clock of a live session.

        Returns:
            dict: Updated session snapshot.
        """
        app.state.agent.live().touch()
        return app.state.agent.snapshot()

    @app.post("/executions", status_code=202)
    async def execute(body: Execute):
        """Accept a code execution request or retrieve its idempotent result.

        Args:
            body (Execute): Validated code, execution ID, and timeout.

        Returns:
            dict: Execution ID, state, and decoded result, if available.
        """
        return app.state.agent.accept({**body.model_dump(), "kind": "execute"})

    @app.get("/executions/{execution_id:path}")
    async def execution(execution_id: str):
        """Read an execution state and its available result.

        Args:
            execution_id (str): Command identifier within the session.

        Returns:
            dict: Execution ID, state, and decoded result, if available.
        """
        sid = app.state.agent.assigned().id
        app.state.agent.journal.session(sid)
        return app.state.agent.journal.execution(sid, execution_id)

    @app.post("/variables", status_code=202)
    async def variables(body: Inspect):
        """Queue variable inspection with a five-second execution timeout.

        Args:
            body (Inspect): Validated inspection execution ID.

        Returns:
            dict: Execution ID, state, and decoded result, if available.
        """
        return app.state.agent.accept(
            {
                "execution_id": body.execution_id,
                "kind": "inspect",
                "timeout_seconds": 5.0,
            },
        )

    @app.post("/kernel/interrupt")
    async def interrupt():
        """Request interruption of the active execution.

        Returns:
            dict: Acknowledgement that interruption was requested.
        """
        await app.state.agent.live().interrupt()
        return {"interrupt_requested": True}

    @app.post("/kernel/restart")
    async def restart():
        """Restart a ready, idle kernel and reset its memory.

        Returns:
            dict: New kernel generation UUID.
        """
        runtime = app.state.agent.live()
        await runtime.restart()
        return {"generation": runtime.generation}

    @app.post("/runtime/drain")
    async def drain():
        """Stop accepting work and interrupt the active execution.

        Returns:
            dict: Acknowledgement that draining was requested.
        """
        await app.state.agent.live().drain()
        return {"draining": True}

    @app.post("/runtime/stop")
    async def stop():
        """Stop a session and report whether resource cleanup completed.

        Returns:
            JSONResponse: Stopped flag and generation; HTTP 503 while cleanup remains
            pending.
        """
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
        """Read a page of events after an exclusive sequence cursor.

        Args:
            after (int): Last received sequence number, or zero for the first page.

        Returns:
            dict: An events list containing up to 100 journal records.
        """
        sid = app.state.agent.assigned().id
        app.state.agent.journal.session(sid)
        return {"events": app.state.agent.journal.events(sid, after)}

    @app.websocket("/events/ws")
    async def stream(ws: WebSocket):
        """Authenticate a WebSocket and stream replayed and new session events.

        Invalid credentials, cursors, or missing sessions reject the handshake. The stream
        sends heartbeats while idle and bounds send time for slow clients.

        Args:
            ws (WebSocket): Connection carrying the bearer header and optional after cursor.
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
            """Send journal pages and subsequent events, updating the connection cursor.

            Raises:
                TimeoutError: Sending an event or heartbeat exceeds ten seconds.
            """
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
            """Read incoming WebSocket events until the client disconnects."""
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

    return app
