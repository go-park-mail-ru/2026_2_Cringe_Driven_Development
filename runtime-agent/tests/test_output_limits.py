"""Regressions for kernel data bypassing the persistent output budget."""

import json
from dataclasses import replace

import pytest

from .conftest import command, new_session, until


@pytest.mark.parametrize(
    "kind,extra",
    [
        ("execute_reply", {"traceback": ["x" * 200_000]}),
        ("custom_message", {"data": "x" * 200_000}),
        ("status", {"metadata_blob": "x" * 200_000}),
    ],
)
async def test_large_non_output_message_is_not_persisted(env, kind, extra):
    agent, backend, _ = env
    agent.session.settings = replace(agent.settings, max_output_bytes=1024)
    sid, session = await new_session(agent)
    agent.accept(command())
    kernel = backend.kernel
    await until(lambda: kernel.sent)
    parent = kernel.sent[-1][2]
    if kind == "execute_reply":
        kernel.complete(status="error", **extra)
    else:
        kernel.emit(parent, kind, extra)
        kernel.complete()
    await until(lambda: session.active is None)
    result = agent.journal.execution(sid, "run-1")
    events = agent.journal.events(sid, 0)
    assert result["state"] == ("FAILED" if kind == "execute_reply" else "SUCCEEDED")
    assert "x" * 2000 not in json.dumps(events)
    assert "x" * 2000 not in json.dumps(result)
    assert sum(e["kind"] == "output_truncated" for e in events) == 1


async def test_reply_copies_share_budget(env):
    agent, backend, _ = env
    agent.session.settings = replace(agent.settings, max_output_bytes=1024)
    sid, session = await new_session(agent)
    agent.accept(command())
    kernel = backend.kernel
    await until(lambda: kernel.sent)
    kernel.complete(payload=[{"data": "é" * 200}])
    await until(lambda: session.active is None)
    events = agent.journal.events(sid, 0)
    result = agent.journal.execution(sid, "run-1")["result"]
    # Count actual stored copies, including the result duplicated in the state event.
    copies = [e["content"] for e in events if e["kind"] == "kernel_message"]
    copies += [e["result"] for e in events if e.get("result")]
    copies += [result]
    assert sum(len(json.dumps(v, ensure_ascii=False).encode()) for v in copies) <= 1024


@pytest.mark.parametrize(
    "extra",
    [
        {"metadata": {"blob": "x" * 200_000}},
        {"buffers_base64": ["eA==" * 50_000]},
    ],
)
async def test_metadata_and_buffers_are_bounded(env, extra):
    agent, backend, _ = env
    agent.session.settings = replace(agent.settings, max_output_bytes=1024)
    sid, runtime = await new_session(agent)
    agent.accept(command())
    kernel = backend.kernel
    await until(lambda: kernel.sent)
    kernel.emit(kernel.sent[-1][2], "display_data", {"data": {}}, **extra)
    kernel.complete()
    await until(lambda: runtime.active is None)
    events = agent.journal.events(sid, 0)
    assert all(len(json.dumps(event)) < 2000 for event in events)
    assert sum(e["kind"] == "output_truncated" for e in events) == 1
    assert agent.journal.execution(sid, "run-1")["state"] == "SUCCEEDED"


async def test_inspection_result_obeys_budget(env):
    agent, backend, _ = env
    agent.session.settings = replace(agent.settings, max_output_bytes=1024)
    sid, runtime = await new_session(agent)
    agent.accept({"execution_id": "inspect", "kind": "inspect", "timeout_seconds": 5.0})
    kernel = backend.kernel
    await until(lambda: kernel.sent)
    values = [{"name": "x" * 200_000, "type": "str", "preview": "tiny"}]
    kernel.complete(
        user_expressions={
            "variables": {"status": "ok", "data": {"text/plain": repr(json.dumps(values))}},
        }
    )
    await until(lambda: runtime.active is None)
    result = agent.journal.execution(sid, "inspect")
    assert result["state"] == "SUCCEEDED"
    assert result["result"]["output_truncated"]
    assert "x" * 2000 not in json.dumps(agent.journal.events(sid, 0))


async def test_unattributed_non_output_data_is_bounded(env):
    agent, backend, _ = env
    agent.session.settings = replace(agent.settings, max_output_bytes=1024)
    sid, _ = await new_session(agent)
    kernel = backend.kernel
    kernel.emit("unknown", "custom", {"blob": "x" * 200_000})
    kernel.emit("unknown", "custom", {"blob": "x" * 200_000})
    await until(lambda: agent.session.unattributed_truncated)
    events = agent.journal.events(sid, 0)
    assert sum(e["kind"] == "output_truncated" for e in events) == 1
    assert "x" * 2000 not in json.dumps(events)
