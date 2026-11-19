"""Проверяйте ограниченный replay и обязательный resync после потери истории."""

import struct

from runtime_agent.workspace_transfers import Transfers
from runtime_agent.workspace_watch import WorkspaceWatch


async def test_event_budget_and_agent_restart_require_resync(tmp_path):
    """Очередь ограничена, а диск сохраняет границу seq между процессами."""
    (tmp_path / "data").mkdir()
    (tmp_path / "staging").mkdir()
    transfers = Transfers(tmp_path, tmp_path, "generation")
    watch = WorkspaceWatch(transfers)
    for _ in range(5000):
        watch.emit("quota_changed", quota={"used_bytes": 1})
    assert len(watch.events) <= 4096
    assert watch.bytes <= 4 * 1024**2
    assert watch.replay(0)[0]["type"] == "resync_required"
    page = watch.replay(1000)
    assert page[0]["seq"] == 1001 and len(page) <= 256
    before = watch.seq
    await watch.close()
    transfers.close()
    transfers = Transfers(tmp_path, tmp_path, "generation")
    watch = WorkspaceWatch(transfers)
    try:
        assert watch.replay(before)[0]["type"] == "resync_required"
    finally:
        await watch.close()
        transfers.close()


async def test_inotify_overflow_is_explicit(tmp_path, monkeypatch):
    """Kernel overflow не выдаётся за актуальное дерево."""
    (tmp_path / "data").mkdir()
    (tmp_path / "staging").mkdir()
    transfers = Transfers(tmp_path, tmp_path, "generation")
    watch = WorkspaceWatch(transfers)
    try:
        monkeypatch.setattr(
            "runtime_agent.workspace_watch.os.read",
            lambda *_: struct.pack("iIII", -1, 0x4000, 0, 0),
        )
        watch.read()
        monkeypatch.undo()
        assert watch.overflows == 1
        assert watch.replay(0)[0]["type"] == "resync_required"
    finally:
        await watch.close()
        transfers.close()
