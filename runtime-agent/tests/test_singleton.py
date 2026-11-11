"""A VPS waits with a ready kernel, is assigned once, and reboot never replays code."""

from dataclasses import replace

import pytest

from runtime_agent.api import create_app
from runtime_agent.config import Settings

from .conftest import AUTH, TOKEN, FakeContainers, assignment, command, until


async def test_waiting_agent_reports_ready_kernel_without_session(waiting):
    agent, backend, client = waiting
    health = await client.get("/health")
    assert health.status_code == 200
    assert health.json()["state"] == "WAITING"
    assert health.json()["session_id"] is None
    assert backend.started == 1
    for method, path in (("POST", "/executions"), ("GET", "/events"), ("POST", "/runtime/stop")):
        assert (await client.request(method, path, headers=AUTH)).status_code == 503


async def test_waiting_agent_does_not_expire_on_idle(settings):
    backend = FakeContainers()
    app = create_app(replace(settings, idle_seconds=0.001), lambda *_: backend)
    async with app.router.lifespan_context(app):
        agent = app.state.agent
        await until(lambda: agent.warm is not None)
        await agent.check_session()
        assert agent.warm is not None and backend.removed == 0


async def test_assignment_takes_the_waiting_kernel(waiting):
    agent, backend, client = waiting
    kernel = backend.kernel
    body = assignment()
    response = await client.post("/assign", json=body)
    assert response.status_code == 200
    assert response.json()["session_id"] == body["session_id"]
    assert response.json()["state"] == "READY"
    assert backend.started == 1 and agent.session.transport is kernel
    assert (await client.get("/health")).status_code == 401
    assert (await client.get("/health", headers=AUTH)).json()["session_id"] == body["session_id"]


async def test_assignment_rejects_other_languages(waiting):
    agent, backend, client = waiting
    response = await client.post("/assign", json=assignment(language="r"))
    assert response.status_code == 400
    assert agent.session is None and agent.warm is not None


async def test_repeated_and_conflicting_assignment(waiting):
    agent, backend, client = waiting
    body = assignment()
    first = await client.post("/assign", json=body)
    again = await client.post("/assign", json=body)
    assert again.status_code == 200 and again.json() == first.json()
    assert (await client.post("/assign", json=assignment())).status_code == 409
    other_token = assignment(body["session_id"], token="y" * 40)
    assert (await client.post("/assign", json=other_token)).status_code == 409
    assert backend.started == 1


async def test_assignment_waits_for_the_kernel(settings):
    backend = FakeContainers()
    backend.fail_start = True
    app = create_app(settings, lambda *_: backend)
    async with app.router.lifespan_context(app):
        agent = app.state.agent
        await until(lambda: agent.warming.done())
        with pytest.raises(Exception, match="Kernel is not ready"):
            await agent.assign(assignment()["session_id"], "python", TOKEN)
        backend.fail_start = False
        await agent.check_session()
        await until(lambda: agent.warm is not None)
        snapshot = await agent.assign(assignment()["session_id"], "python", TOKEN)
        assert snapshot["ready"]


async def test_dead_waiting_kernel_is_replaced(waiting):
    agent, backend, client = waiting
    backend.kernel = None
    await agent.check_session()
    await until(lambda: agent.warm is not None)
    assert backend.started == 2 and backend.removed == 1


async def test_unprefixed_api_and_no_session_creation(env):
    agent, backend, client = env
    assert (await client.get("/health")).json()["session_id"] == agent.session.id
    assert (await client.post("/sessions", json={})).status_code == 404
    assert (
        await client.post("/executions", json={"execution_id": "a", "code": "1"})
    ).status_code == 202
    kernel = backend.kernel
    await until(lambda: kernel.sent)
    kernel.complete()
    await until(lambda: agent.session.active is None)
    assert (await client.get("/executions/a")).json()["state"] == "SUCCEEDED"
    assert (await client.post("/runtime/stop")).json()["stopped"]
    assert (await client.get("/health")).status_code == 503
    assert (await client.get("/executions/a")).json()["state"] == "SUCCEEDED"


async def test_reboot_restores_assignment_without_starting_kernel(settings):
    first = FakeContainers()
    app = create_app(settings, lambda *_: first)
    body = assignment()
    async with app.router.lifespan_context(app):
        await until(lambda: app.state.agent.warm is not None)
        await app.state.agent.assign(body["session_id"], "python", TOKEN)
        app.state.agent.accept(command())
        await until(lambda: app.state.agent.session.active)
    second = FakeContainers()
    app = create_app(settings, lambda *_: second)
    async with app.router.lifespan_context(app):
        agent = app.state.agent
        assert second.started == 0 and agent.token == TOKEN
        assert agent.snapshot()["state"] == "STOPPED"
        assert agent.journal.execution(body["session_id"], "run-1")["state"] == "UNKNOWN"
        again = await agent.assign(body["session_id"], "python", TOKEN)
        assert again["session_id"] == body["session_id"]
        with pytest.raises(Exception, match="another session"):
            await agent.assign(assignment()["session_id"], "python", TOKEN)


def test_session_is_not_configured_by_environment(monkeypatch):
    monkeypatch.setenv("KERNEL_NETWORK", "cellestial-kernel")
    monkeypatch.setenv("KERNEL_DNS", "1.1.1.1")
    for name in ("SESSION_ID", "KERNEL_LANGUAGE", "RUNTIME_TOKEN"):
        monkeypatch.delenv(name, raising=False)
    assert not hasattr(Settings.from_env(), "session_id")


async def test_failed_kernel_start_keeps_agent_waiting_not_ready(settings):
    backend = FakeContainers()
    backend.fail_start = True
    app = create_app(settings, lambda *_: backend)
    async with app.router.lifespan_context(app):
        await until(lambda: app.state.agent.warming.done())
        snapshot = app.state.agent.snapshot()
        assert snapshot["state"] == "WAITING"
        assert not snapshot["ready"]


@pytest.mark.parametrize(
    "changes",
    [
        {"kernel_network": "bridge"},
        {"kernel_network": "host"},
        {"kernel_network": "none"},
        {"kernel_dns": "::1"},
        {"max_output_bytes": 0},
    ],
)
def test_invalid_configuration_is_rejected(settings, changes):
    with pytest.raises(ValueError):
        replace(settings, **changes)


@pytest.mark.parametrize(
    "body",
    [
        {**assignment(), "session_id": "invalid"},
        {**assignment(), "token": "short"},
        {**assignment(), "extra": 1},
    ],
)
async def test_invalid_assignment_is_rejected(waiting, body):
    agent, backend, client = waiting
    assert (await client.post("/assign", json=body)).status_code == 422
    assert agent.session is None


async def test_waiting_kernel_channel_is_read(waiting):
    # Jupyter закрывает непрочитанный канал (нет pong на ping): он читается до назначения
    agent, backend, client = waiting
    kernel = backend.kernel
    kernel.emit("x", "status", {"execution_state": "idle"})
    await until(lambda: kernel.messages.empty())
    assert agent.warm is kernel
    response = await client.post("/assign", json=assignment())
    assert response.status_code == 200 and agent.session.transport is kernel


async def test_closed_waiting_channel_is_replaced(waiting):
    agent, backend, client = waiting
    backend.kernel.messages.put_nowait(ConnectionError())
    await until(lambda: agent.warm is None)
    await agent.check_session()
    await until(lambda: agent.warm is not None)
    assert backend.started == 2
