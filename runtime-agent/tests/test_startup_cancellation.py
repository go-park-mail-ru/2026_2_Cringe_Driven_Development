"""Shutdown during the waiting kernel startup must not outlive creation or cleanup."""

import asyncio
import sqlite3
import threading
from contextlib import asynccontextmanager, suppress
from types import SimpleNamespace

import pytest
from docker.errors import APIError, NotFound

from runtime_agent.api import create_app
from runtime_agent.journal import Journal


class ControlledDocker:
    """Delay external Docker operations while exercising real lifecycle code."""

    def __init__(self, settings):
        self.settings = settings
        self.containers = self
        self.networks = SimpleNamespace(
            get=lambda _: SimpleNamespace(
                id="network-id",
                name=settings.kernel_network,
                attrs={"Name": settings.kernel_network, "Driver": "bridge"},
            )
        )
        self.create_entered = threading.Event()
        self.allow_create = threading.Event()
        self.create_finished = threading.Event()
        self.remove_entered = threading.Event()
        self.allow_remove = threading.Event()
        self.allow_remove.set()
        self.close_entered = threading.Event()
        self.allow_close = threading.Event()
        self.allow_close.set()
        self.created = False
        self.creation_error = None
        self.removal_error = None
        self.labels = {}
        self.order = []
        self.run_calls = 0

    def ping(self):
        return True

    def run(self, *args, **kwargs):
        self.run_calls += 1
        self.labels = kwargs["labels"]
        self.create_entered.set()
        try:
            if not self.allow_create.wait(5):
                raise TimeoutError("test creation barrier was not released")
            if self.creation_error:
                raise self.creation_error
            self.created = True
            return self.container()
        finally:
            self.order.append("creation_finished")
            self.create_finished.set()

    def container(self):
        return SimpleNamespace(labels=self.labels, remove=self.remove)

    def get(self, name):
        if not self.created:
            raise NotFound("container does not exist yet")
        return self.container()

    def list(self, **kwargs):
        return [self.container()] if self.created else []

    def remove(self, **kwargs):
        self.remove_entered.set()
        if not self.allow_remove.wait(5):
            raise TimeoutError("test removal barrier was not released")
        if self.removal_error:
            raise self.removal_error
        self.created = False
        self.order.append("removed")

    def close(self):
        self.close_entered.set()
        if not self.allow_close.wait(5):
            raise TimeoutError("test client close barrier was not released")
        self.order.append("client_closed")


@pytest.fixture
def docker_lifecycle(settings, monkeypatch):
    client = ControlledDocker(settings)
    monkeypatch.setattr("runtime_agent.containers.docker.from_env", lambda **_: client)
    return client


async def entered(event):
    assert await asyncio.to_thread(event.wait, 2), "Docker operation did not reach its barrier"


async def assert_waiting(task):
    # A deadline asserts that cancellation cannot finish while the controlled
    # operation is blocked; the barriers, not sleeps, determine operation order.
    with pytest.raises(TimeoutError):
        await asyncio.wait_for(asyncio.shield(task), 0.03)


@asynccontextmanager
async def starting(app, client):
    async def serve():
        async with app.router.lifespan_context(app):
            await asyncio.Event().wait()

    task = asyncio.create_task(serve())
    try:
        await entered(client.create_entered)
        yield task
    finally:
        client.allow_create.set()
        client.allow_remove.set()
        client.allow_close.set()
        if not task.done():
            task.cancel()
        with suppress(asyncio.CancelledError, Exception):
            await asyncio.wait_for(asyncio.shield(task), 5)
        await entered(client.create_finished)


@pytest.mark.parametrize("repeat_during", [None, "creation", "removal", "shutdown"])
async def test_cancelled_start_waits_for_creation_and_cleanup(
    settings, docker_lifecycle, repeat_during
):
    client = docker_lifecycle
    if repeat_during == "removal":
        client.allow_remove.clear()
    if repeat_during == "shutdown":
        client.allow_close.clear()
    app = create_app(settings)
    async with starting(app, client) as task:
        task.cancel()
        await assert_waiting(task)
        assert "client_closed" not in client.order
        if repeat_during == "creation":
            task.cancel()
            await assert_waiting(task)
        client.allow_create.set()
        if repeat_during == "removal":
            await entered(client.remove_entered)
            task.cancel()
            await assert_waiting(task)
            assert "client_closed" not in client.order
            client.allow_remove.set()
        if repeat_during == "shutdown":
            await entered(client.close_entered)
            task.cancel()
            await assert_waiting(task)
            # The journal must stay open until the Docker client finishes closing.
            app.state.agent.journal.db.execute("SELECT 1")
            client.allow_close.set()
        with pytest.raises(asyncio.CancelledError):
            await task
        assert not client.created
        assert client.order.index("creation_finished") < client.order.index("removed")
        assert client.order.index("removed") < client.order.index("client_closed")
        assert app.state.agent.warm is None and app.state.agent.session is None
        with pytest.raises(sqlite3.ProgrammingError):
            app.state.agent.journal.db.execute("SELECT 1")


async def test_creation_failure_after_cancel_is_observed(settings, docker_lifecycle):
    client = docker_lifecycle
    client.creation_error = APIError("controlled Docker failure")
    app = create_app(settings)
    async with starting(app, client) as task:
        task.cancel()
        await assert_waiting(task)
        client.allow_create.set()
        with pytest.raises(asyncio.CancelledError):
            await task
        assert not client.created
        assert client.order.index("creation_finished") < client.order.index("client_closed")
        assert app.state.agent.warm is None


async def test_cleanup_failure_after_cancel_is_recovered(settings, docker_lifecycle):
    client = docker_lifecycle
    client.removal_error = APIError("controlled removal failure")
    app = create_app(settings)
    async with starting(app, client) as task:
        task.cancel()
        await assert_waiting(task)
        client.allow_create.set()
        with pytest.raises(asyncio.CancelledError):
            await task
        assert client.created
    journal = Journal(settings.state_dir)
    try:
        assert journal.assignment() is None
    finally:
        journal.close()
    client.removal_error = None
    recovered = create_app(settings)
    async with recovered.router.lifespan_context(recovered):
        # recover() removed the stale container before a new waiting kernel starts
        assert client.order.count("removed") == 1
        assert recovered.state.agent.snapshot()["state"] == "WAITING"


async def test_creation_failure_without_cancel_keeps_agent_waiting(settings, docker_lifecycle):
    client = docker_lifecycle
    client.creation_error = APIError("controlled creation failure")
    client.allow_create.set()
    app = create_app(settings)
    async with app.router.lifespan_context(app):
        await asyncio.wait_for(asyncio.shield(app.state.agent.warming), 2)
        state = app.state.agent.snapshot()
        assert state["state"] == "WAITING"
        assert not state["ready"]
    assert not client.created
    assert client.run_calls == 1
    assert client.order.index("creation_finished") < client.order.index("client_closed")
