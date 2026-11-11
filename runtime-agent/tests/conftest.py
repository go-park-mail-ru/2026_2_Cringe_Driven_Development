import asyncio
from uuid import uuid4

import httpx
import pytest

from runtime_agent.api import create_app
from runtime_agent.config import Settings

TOKEN = "test-token-" + "x" * 32
AUTH = {"Authorization": "Bearer " + TOKEN}


class FakeKernel:
    def __init__(self):
        self.messages = asyncio.Queue()
        self.sent = []
        self.interrupts = 0
        self.respond_to_interrupt = True
        self.closed = False

    def emit(self, msg_id, kind, content, channel="iopub", **extra):
        self.messages.put_nowait(
            {
                "parent_header": {"msg_id": msg_id},
                "header": {"msg_type": kind},
                "channel": channel,
                "content": content,
                **extra,
            }
        )

    def complete(self, msg_id=None, status="ok", idle_first=False, **reply):
        msg_id = msg_id or self.sent[-1][2]
        if idle_first:
            self.emit(msg_id, "status", {"execution_state": "idle"})
        self.emit(msg_id, "execute_reply", {"status": status, **reply}, "shell")
        if not idle_first:
            self.emit(msg_id, "status", {"execution_state": "idle"})

    async def send(self, kind, content, msg_id):
        self.sent.append((kind, content, msg_id))

    async def receive(self):
        msg = await self.messages.get()
        if isinstance(msg, Exception):
            raise msg
        return msg

    async def interrupt(self):
        self.interrupts += 1
        if self.respond_to_interrupt:
            self.complete(status="error")

    async def restart(self, startup_seconds):
        self.messages = asyncio.Queue()

    async def close(self):
        self.closed = True


class FakeContainers:
    def __init__(self, settings=None, instance_id=None):
        self.kernel = None
        self.started = 0
        self.removed = 0
        self.recovered = False
        self.fail_remove = False
        self.fail_start = False
        self.start_gate = None

    async def ping(self):
        return True

    async def recover(self):
        self.recovered = True

    async def start(self):
        if self.start_gate:
            await self.start_gate.wait()
        if self.fail_start:
            raise RuntimeError("start failed")
        self.started += 1
        self.kernel = FakeKernel()
        return self.kernel

    async def alive(self):
        return self.kernel is not None

    async def remove(self):
        if self.fail_remove:
            raise RuntimeError("Docker is down")
        self.removed += 1
        self.kernel = None

    async def close(self):
        pass


@pytest.fixture
def settings(tmp_path):
    return Settings(
        state_dir=tmp_path / "state",
        kernel_network="cellestial-kernel-test",
        kernel_dns="1.1.1.1",
        monitor_seconds=60,
        interrupt_seconds=0.05,
    )


def assignment(session_id=None, language="python", token=TOKEN):
    return {"session_id": session_id or str(uuid4()), "language": language, "token": token}


@pytest.fixture
async def waiting(settings):
    """Started agent before the assignment, with a ready waiting kernel."""
    backend = FakeContainers()
    app = create_app(settings, lambda *_: backend)
    async with app.router.lifespan_context(app):
        async with httpx.AsyncClient(
            transport=httpx.ASGITransport(app=app), base_url="http://test"
        ) as client:
            await until(lambda: app.state.agent.warm is not None)
            yield app.state.agent, backend, client


@pytest.fixture
async def env(waiting):
    agent, backend, client = waiting
    response = await client.post("/assign", json=assignment())
    assert response.status_code == 200, response.text
    client.headers.update(AUTH)
    yield agent, backend, client


async def until(predicate):
    async with asyncio.timeout(2):
        while not predicate():
            await asyncio.sleep(0.001)


async def new_session(agent):
    """Return the assigned session, assigning the waiting kernel first if needed."""
    if agent.session is None:
        async with asyncio.timeout(60):
            while agent.warm is None:
                await asyncio.sleep(0.01)
        await agent.assign(str(uuid4()), "python", TOKEN)
    return agent.session.id, agent.session


def command(eid="run-1", code="print(42)", timeout=300.0):
    return {"execution_id": eid, "kind": "execute", "code": code, "timeout_seconds": timeout}
