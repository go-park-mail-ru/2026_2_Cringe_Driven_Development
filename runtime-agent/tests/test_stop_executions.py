import asyncio

import pytest

from runtime_agent.models import AgentError

from .conftest import command, new_session, until


async def test_stop_cancels_queue_and_keeps_kernel(env):
    agent, backend, _ = env
    sid, session = await new_session(agent)
    generation, transport = session.generation, session.transport
    agent.accept(command())
    await until(lambda: session.active)
    agent.accept(command("queued"))
    await session.stop_executions("stop-1")
    assert agent.journal.execution(sid, "run-1")["state"] == "INTERRUPTED"
    assert agent.journal.execution(sid, "queued")["state"] == "CANCELLED"
    assert session.ready and not session.draining and not session.closed
    assert session.generation == generation and session.transport is transport
    assert session.queued() == 0
    agent.accept(command("new"))
    await until(lambda: len(backend.kernel.sent) == 2)
    await session.stop_executions("stop-1")
    assert backend.kernel.interrupts == 1
    backend.kernel.complete()
    await until(lambda: session.active is None)
    assert agent.journal.execution(sid, "new")["state"] == "SUCCEEDED"
    await session.stop_executions("stop-2")


async def test_stop_cancels_dequeued_command_before_dispatch(env):
    agent, backend, _ = env
    sid, session = await new_session(agent)
    async with session.lock:
        agent.accept(command())
        stopping = asyncio.create_task(session.stop_executions("stop"))
        await until(lambda: session.pending is not None)
    await stopping
    assert agent.journal.execution(sid, "run-1")["state"] == "CANCELLED"
    assert backend.kernel.sent == []


async def test_stop_rejects_new_work_and_parallel_stop(env):
    agent, backend, _ = env
    _, session = await new_session(agent)
    agent.accept(command())
    await until(lambda: session.active)
    entered, release = asyncio.Event(), asyncio.Event()
    original = backend.kernel.interrupt

    async def delayed():
        entered.set()
        await release.wait()
        await original()

    backend.kernel.interrupt = delayed
    task = asyncio.create_task(session.stop_executions("stop"))
    await entered.wait()
    with pytest.raises(AgentError) as err:
        agent.accept(command("new"))
    assert err.value.status == 409
    with pytest.raises(AgentError) as err:
        await session.stop_executions("other")
    assert err.value.status == 409
    release.set()
    await task


async def test_stop_failure_is_not_success(env):
    agent, backend, _ = env
    _, session = await new_session(agent)
    agent.accept(command())
    await until(lambda: session.active)
    backend.kernel.respond_to_interrupt = False
    with pytest.raises(AgentError) as err:
        await session.stop_executions("stop")
    assert err.value.status == 503
    assert agent.snapshot()["reason"] == "interrupt_unconfirmed"


async def test_interrupt_keeps_queue_running(env):
    agent, backend, _ = env
    sid, session = await new_session(agent)
    agent.accept(command())
    await until(lambda: session.active)
    agent.accept(command("queued"))
    await session.interrupt()
    await until(lambda: len(backend.kernel.sent) == 2)
    backend.kernel.complete()
    await until(lambda: session.active is None)
    assert agent.journal.execution(sid, "run-1")["state"] == "INTERRUPTED"
    assert agent.journal.execution(sid, "queued")["state"] == "SUCCEEDED"


async def test_stop_preserves_completed_active_result(env):
    agent, backend, _ = env
    sid, session = await new_session(agent)
    agent.accept(command())
    await until(lambda: session.active)
    async with session.lock:
        backend.kernel.complete()
        await until(lambda: session.active is None or session.active.done.is_set())
        task = asyncio.create_task(session.stop_executions("stop"))
    await task
    assert agent.journal.execution(sid, "run-1")["state"] == "SUCCEEDED"
    assert backend.kernel.interrupts == 0


async def test_stop_http_requires_id_and_returns_confirmation(env):
    agent, _, client = env
    await new_session(agent)
    assert (await client.post("/executions/stop", json={})).status_code == 422
    response = await client.post("/executions/stop", json={"operation_id": "stop"})
    assert response.status_code == 200
    assert response.json() == {"stopped": True}


async def test_stop_waits_for_idle_without_interrupting_finished_code(env):
    agent, backend, _ = env
    sid, session = await new_session(agent)
    agent.accept(command())
    await until(lambda: session.active)
    parent = backend.kernel.sent[-1][2]
    backend.kernel.emit(parent, "execute_reply", {"status": "ok"}, "shell")
    await until(lambda: session.active.reply is not None)
    task = asyncio.create_task(session.stop_executions("stop"))
    await until(lambda: session.stopping_executions)
    backend.kernel.emit(parent, "status", {"execution_state": "idle"})
    await task
    assert agent.journal.execution(sid, "run-1")["state"] == "SUCCEEDED"
    assert backend.kernel.interrupts == 0


async def test_stop_can_finalize_when_timeout_waits_for_control_lock(env):
    agent, _, _ = env
    sid, session = await new_session(agent)
    worker_waiting = asyncio.Event()

    class ControlLock(asyncio.Lock):
        async def acquire(self):
            if self.locked() and asyncio.current_task() is session.worker:
                worker_waiting.set()
            return await super().acquire()

    session.lock = ControlLock()
    agent.accept(command(timeout=0.01))
    await until(lambda: session.active)
    async with session.lock:
        stopping = asyncio.create_task(session.stop_executions("stop"))
        await until(lambda: session.stopping_executions)
        await worker_waiting.wait()
    await stopping
    assert agent.journal.execution(sid, "run-1")["state"] == "TIMED_OUT"
    assert session.ready and not session.closed


async def test_stop_fences_commands_delivered_after_stop(env):
    agent, backend, _ = env
    sid, session = await new_session(agent)
    await session.stop_executions("stop", ["late"])
    assert agent.accept(command("late"))["state"] == "CANCELLED"
    assert agent.accept(command("late"))["state"] == "CANCELLED"
    agent.accept(command("new"))
    await until(lambda: session.active)
    assert len(backend.kernel.sent) == 1
    assert agent.journal.execution(sid, "late")["state"] == "CANCELLED"
    backend.kernel.complete()
    await until(lambda: session.active is None)
