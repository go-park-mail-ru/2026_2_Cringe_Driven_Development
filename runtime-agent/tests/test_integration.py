"""Real protocol checks; enable explicitly so unit tests need no local daemon."""

import asyncio
import os
import signal
import socket
import sys
from dataclasses import replace
from pathlib import Path

import pytest

from runtime_agent.api import create_app
from runtime_agent.containers import Containers
from runtime_agent.jupyter import Jupyter

from .conftest import command, new_session


async def completed(agent, sid, eid):
    async with asyncio.timeout(30):
        while True:
            result = agent.journal.execution(sid, eid)
            if result["state"] not in ("QUEUED", "RUNNING"):
                return result
            await asyncio.sleep(0.02)


def all_events(agent, sid):
    events = []
    after = 0
    while batch := agent.journal.events(sid, after):
        events.extend(batch)
        after = batch[-1]["seq"]
    return events


class LocalJupyter:
    """Exercise the production transport against a real server without Docker."""

    def __init__(self, settings, instance_id):
        self.settings = settings
        self.processes = {}
        self.logs = {}

    async def ping(self):
        return True

    async def recover(self):
        pass

    async def start(self):
        sid = "kernel"
        directory = self.settings.state_dir.parent / "local-jupyter" / sid
        directory.mkdir(parents=True)
        config = Path(__file__).parents[1] / "images/jupyter_server_config.py"
        log = self.logs[sid] = (directory / "server.log").open("w+")
        environment = {
            **os.environ,
            "JUPYTER_TOKEN": "local-test-only",
            "JUPYTER_RUNTIME_DIR": str(directory / "runtime"),
            "JUPYTER_CONFIG_DIR": str(directory / "config"),
        }
        with socket.socket() as listener:
            listener.bind(("127.0.0.1", 0))
            port = listener.getsockname()[1]
        self.processes[sid] = await asyncio.create_subprocess_exec(
            sys.executable,
            "-m",
            "jupyter_server",
            f"--config={config}",
            "--ServerApp.ip=127.0.0.1",
            f"--ServerApp.port={port}",
            f"--ServerApp.root_dir={directory}",
            "--ServerApp.allow_root=True",
            env=environment,
            stdout=log,
            stderr=asyncio.subprocess.STDOUT,
            start_new_session=True,
        )
        transport = Jupyter(f"http://127.0.0.1:{port}", "local-test-only")
        try:
            await transport.start(30)
            return transport
        except BaseException:
            await transport.close()
            log.seek(0)
            print(log.read())
            await self.remove()
            raise

    async def alive(self):
        return "kernel" in self.processes and self.processes["kernel"].returncode is None

    async def remove(self, sid="kernel"):
        process = self.processes.pop(sid, None)
        if process and process.returncode is None:
            os.killpg(process.pid, signal.SIGTERM)
            try:
                await asyncio.wait_for(process.wait(), 5)
            except TimeoutError:
                os.killpg(process.pid, signal.SIGKILL)
                await process.wait()
        if sid in self.logs:
            self.logs.pop(sid).close()

    async def close(self):
        for sid in list(self.processes):
            await self.remove(sid)


@pytest.fixture(params=["local", "docker"])
async def real_agent(settings, request):
    backend = request.param
    if backend == "local":
        if os.environ.get("RUN_JUPYTER_TESTS") != "1":
            pytest.skip("Set RUN_JUPYTER_TESTS=1 for local Jupyter integration")
        factory = LocalJupyter
    else:
        if os.environ.get("RUN_DOCKER_TESTS") != "1":
            pytest.skip("Set RUN_DOCKER_TESTS=1 for Docker integration")
        factory = Containers
    if backend == "docker":
        settings = request.getfixturevalue("docker_settings")
    settings = replace(settings, interrupt_seconds=5, monitor_seconds=0.1)
    app = create_app(settings, factory)
    async with app.router.lifespan_context(app):
        yield app.state.agent, backend


@pytest.mark.jupyter
@pytest.mark.docker
async def test_real_execution_inspection_interrupt_restart(real_agent):
    agent, backend = real_agent
    sid, session = await new_session(agent)
    assert session.ready, agent.snapshot()
    code = "x = 42\nprint(x)\nx * 2"
    agent.accept(command(code=code))
    assert (await completed(agent, sid, "run-1"))["state"] == "SUCCEEDED"
    assert agent.accept(command(code=code))["state"] == "SUCCEEDED"
    agent.accept({"execution_id": "inspect", "kind": "inspect", "timeout_seconds": 5.0})
    variables = await completed(agent, sid, "inspect")
    assert variables["state"] == "SUCCEEDED", variables
    x = next(v for v in variables["result"]["variables"] if v["name"] == "x")
    assert x["preview"] == "42"
    agent.accept(command("next", "x + 1"))
    assert (await completed(agent, sid, "next"))["state"] == "SUCCEEDED"
    error_code = "raise ValueError('expected')"
    agent.accept(command("error", error_code))
    assert (await completed(agent, sid, "error"))["state"] == "FAILED"
    # Large real Python exceptions must not bypass the persistent budget via reply.
    previous = session.settings
    session.settings = replace(previous, max_output_bytes=1024)
    large_error = "raise ValueError('x' * 200000)"
    agent.accept(command("large-error", large_error))
    large_result = await completed(agent, sid, "large-error")
    assert large_result["state"] == "FAILED"
    assert large_result["result"]["output_truncated"]
    assert all(
        len(str(event)) < 3000
        for event in all_events(agent, sid)
        if event.get("execution_id") == "large-error"
    )
    session.settings = previous
    rich = "from IPython.display import display\ndisplay({'text/html': '<b>hi</b>'}, raw=True)"
    agent.accept(command("rich", rich))
    assert (await completed(agent, sid, "rich"))["state"] == "SUCCEEDED"
    assert any(
        e.get("content", {}).get("data", {}).get("text/html") == "<b>hi</b>"
        for e in all_events(agent, sid)
    )
    sleep = "import time; time.sleep(20)"
    agent.accept(command("timeout", sleep, 1.0))
    assert (await completed(agent, sid, "timeout"))["state"] == "TIMED_OUT"
    agent.accept(command("interrupt", sleep))
    async with asyncio.timeout(5):
        while session.active is None:
            await asyncio.sleep(0.01)
    await asyncio.sleep(0.2)
    await session.interrupt()
    assert (await completed(agent, sid, "interrupt"))["state"] == "INTERRUPTED"
    generation = session.generation
    await session.restart()
    assert generation != session.generation
    agent.accept(command("cleared", "x"))
    assert (await completed(agent, sid, "cleared"))["state"] == "FAILED"
    # Kill the process directly to exercise kernel death before an execute_reply.
    agent.accept(command("crash", "import os; os._exit(1)"))
    assert (await completed(agent, sid, "crash"))["state"] == "UNKNOWN"
    async with asyncio.timeout(10):
        while not session.closed:
            await asyncio.sleep(0.01)
    assert session.closed
    assert agent.snapshot()["reason"] == "kernel_died"
    assert all_events(agent, sid)[-1]["kind"] == "runtime_stopped"


@pytest.fixture
def docker_settings(settings):
    """Protocol-test network only: this does not install or prove production firewall."""
    if os.environ.get("RUN_DOCKER_TESTS") != "1":
        pytest.skip("Set RUN_DOCKER_TESTS=1")
    from uuid import uuid4

    import docker

    client = docker.from_env()
    network = client.networks.create(
        "cellestial-test-" + uuid4().hex[:12],
        driver="bridge",
        enable_ipv6=False,
        labels={"ru.cellestial.test": "protocol"},
    )
    try:
        yield replace(settings, kernel_network=network.name)
    finally:
        network.remove()
        client.close()


@pytest.mark.docker
async def test_docker_limits_no_mounts_and_permission_tampering(docker_settings):
    app = create_app(docker_settings)
    async with app.router.lifespan_context(app):
        agent = app.state.agent
        sid, runtime = await new_session(agent)
        assert runtime.ready
        container = await asyncio.to_thread(
            agent.containers.client.containers.get, agent.containers.name()
        )
        config = container.attrs["HostConfig"]
        assert config["ReadonlyRootfs"]
        assert config["Memory"] == config["MemorySwap"] == 1024**3
        assert config["NanoCpus"] == 1_000_000_000
        assert config["PidsLimit"] == 128
        assert not config.get("Binds")
        assert not config.get("PortBindings")
        assert container.attrs["Config"]["User"] == "65532:65532"
        assert not any(mount.get("Type") == "bind" for mount in container.attrs["Mounts"])
        code = """import os
from pathlib import Path
assert 'RUNTIME_TOKEN' not in os.environ
assert not Path('/var/run/docker.sock').exists()
assert not Path('/control').exists()
assert not Path('/var/lib/cellestial-agent').exists()
for base in ('/workspace', '/tmp'):
    directory = Path(base) / 'hostile'
    directory.mkdir()
    os.chmod(directory, 0)
# The root-owned tmpfs mount itself is not owned by the kernel user.
try:
    os.chmod('/workspace', 0)
except PermissionError:
    pass
else:
    raise AssertionError('kernel must not own the workspace mount')
"""
        agent.accept(command(code=code))
        assert (await completed(agent, sid, "run-1"))["state"] == "SUCCEEDED"
        await runtime.finish()
        assert runtime.closed
        assert not await agent.containers.alive()
        assert agent.snapshot()["reason"] == "requested"


@pytest.mark.docker
async def test_docker_oom_stops_runtime(docker_settings):
    app = create_app(docker_settings)
    async with app.router.lifespan_context(app):
        agent = app.state.agent
        sid, runtime = await new_session(agent)
        agent.accept(command("oom", "blob = bytearray(2 * 1024**3)"))
        assert (await completed(agent, sid, "oom"))["state"] == "UNKNOWN"
        async with asyncio.timeout(30):
            while not runtime.closed:
                await asyncio.sleep(0.05)


@pytest.mark.docker
async def test_docker_recovery_removes_only_owned_containers(docker_settings):
    from runtime_agent.journal import Journal
    from runtime_agent.runtime import Agent

    settings = docker_settings
    journal = Journal(settings.state_dir)
    backend = Containers(settings, journal.instance_id)
    agent = Agent(settings, journal, backend)
    foreign = await asyncio.to_thread(
        backend.client.containers.run,
        settings.python_image,
        ["sleep", "120"],
        detach=True,
        network_mode="none",
        labels={"ru.cellestial.runtime-agent": "another-agent"},
    )
    try:
        await agent.start()
        sid, runtime = await new_session(agent)
        agent.accept(command(code="import time; time.sleep(60)"))
        async with asyncio.timeout(5):
            while not runtime.active:
                await asyncio.sleep(0.01)
        # Simulate process loss without graceful container removal or completed journal state.
        tasks = [agent.monitor, runtime.worker, runtime.reader]
        for task in tasks:
            task.cancel()
        await asyncio.gather(*tasks, return_exceptions=True)
        await runtime.transport.close()
        journal.transition(sid, "run-1", "RUNNING")
        await backend.close()
        journal.close()
        app = create_app(settings)
        async with app.router.lifespan_context(app):
            recovered = app.state.agent
            await asyncio.to_thread(foreign.reload)
            assert foreign.status == "running"
            assert not await recovered.containers.alive()
            assert recovered.journal.execution(sid, "run-1")["state"] == "UNKNOWN"
            assert recovered.snapshot()["reason"] == "agent_restarted"
            assert not recovered.session.ready
    finally:
        await asyncio.to_thread(foreign.remove, force=True, v=True)
        if not journal.lock.closed:
            await agent.close()
            journal.close()
