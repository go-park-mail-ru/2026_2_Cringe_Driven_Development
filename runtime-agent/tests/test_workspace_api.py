"""Проверяйте файловые сценарии через авторизованную границу агента."""

from dataclasses import replace
from uuid import uuid4

import httpx
import pytest

from runtime_agent.api import create_app

from .conftest import AUTH, FakeContainers, assignment, until


@pytest.fixture
async def files(settings, tmp_path):
    """Запустите агент с fake kernel и настоящим временным файловым деревом."""
    root = tmp_path / "workspace"
    (root / "data").mkdir(parents=True)
    (root / "staging").mkdir(mode=0o700)
    app = create_app(replace(settings, workspace_path=root), FakeContainers)
    async with app.router.lifespan_context(app):
        agent = app.state.agent
        await until(lambda: agent.warm is not None)
        async with httpx.AsyncClient(
            transport=httpx.ASGITransport(app=app), base_url="http://agent", headers=AUTH
        ) as client:
            sid = assignment()
            assert (await client.post("/assign", json=sid)).status_code == 200
            client.headers["x-workspace-socket"] = "test"
            yield client, sid["session_id"], root, agent


async def invoke(client, sid, kind, **parameters):
    """Выполните команду с envelope текущего протокола."""
    response = await client.post(
        "/workspace/command",
        json={
            "protocol_version": 1,
            "type": kind,
            "request_id": "test",
            "session_id": sid,
            "workspace_generation": sid,
            **parameters,
        },
    )
    assert response.status_code == 200, response.text
    return response.json()


async def test_file_operations_and_transfer_http(files):
    """Выполните mkdir/upload/rename/download/delete и сохраните идемпотентность."""
    client, sid, root, _ = files
    operation = str(uuid4())
    first = await invoke(client, sid, "mkdir", path="folder", operation_id=operation)
    assert "error" not in first
    assert await invoke(client, sid, "mkdir", path="folder", operation_id=operation) == first
    upload = (
        await invoke(
            client, sid, "upload_begin", path="folder/файл", size_bytes=6, operation_id=str(uuid4())
        )
    )["result"]
    tid = upload["transfer_id"]
    assert (
        await client.post(f"/workspace/transfers/{tid}/chunks?offset=0", content=b"abcdef")
    ).json()["offset"] == 6
    assert "error" not in await invoke(client, sid, "upload_commit", transfer_id=tid)
    listing = (await invoke(client, sid, "list", path="folder"))["result"]
    version = listing["items"][0]["version"]
    assert "error" not in await invoke(
        client,
        sid,
        "rename",
        path="folder/файл",
        name="renamed",
        version=version,
        operation_id=str(uuid4()),
    )
    assert (root / "data/folder/renamed").read_bytes() == b"abcdef"
    download = (
        await invoke(
            client, sid, "download_begin", path="folder/renamed", operation_id=str(uuid4())
        )
    )["result"]
    response = await client.get(
        f"/workspace/transfers/{download['transfer_id']}/download", headers={"Range": "bytes=2-4"}
    )
    assert response.status_code == 206
    assert response.content == b"cde"
    assert response.headers["content-range"] == "bytes 2-4/6"
    listing = (await invoke(client, sid, "list", path="folder"))["result"]
    assert "error" not in await invoke(
        client,
        sid,
        "delete",
        path="folder/renamed",
        version=listing["items"][0]["version"],
        operation_id=str(uuid4()),
    )
    assert not (root / "data/folder/renamed").exists()


async def test_subscriptions_observe_python_and_stop_cleans(files):
    """Изменение Python попадает в журнал, stop очищает оба дерева."""
    client, sid, root, agent = files
    result = await invoke(client, sid, "subscribe", paths=[""])
    assert "error" not in result
    (root / "data/python").write_text("python")
    await until(lambda: agent.workspace.watch.seq > result["result"]["seq"])
    events = (await client.get("/workspace/events?after=0")).json()["events"]
    assert any(event["type"] in ("created", "updated", "directory_invalidated") for event in events)
    await invoke(
        client, sid, "upload_begin", path="unfinished", size_bytes=1, operation_id=str(uuid4())
    )
    await client.post("/runtime/stop")
    assert list((root / "data").iterdir()) == []
    assert list((root / "staging").iterdir()) == []


async def test_validation_and_generation(files):
    """Отклоните чужую generation, traversal и лишние поля."""
    client, sid, _, _ = files
    for parameters in ({"path": "../outside"}, {"path": "", "recursive": True}):
        result = await invoke(client, sid, "list", **parameters)
        assert result["error"]["code"] == "invalid_path"
    result = await invoke(client, str(uuid4()), "list")
    assert result["error"]["code"] == "workspace_closed"


async def test_graceful_agent_restart_retains_transfer_and_generation(settings, tmp_path):
    """Процесс агента восстанавливает offset и файлы живого workspace."""
    root = tmp_path / "workspace"
    (root / "data").mkdir(parents=True)
    (root / "staging").mkdir(mode=0o700)
    configured = replace(settings, workspace_path=root)
    app = create_app(configured, FakeContainers)
    sid = assignment()["session_id"]
    async with app.router.lifespan_context(app):
        await until(lambda: app.state.agent.warm is not None)
        async with httpx.AsyncClient(
            transport=httpx.ASGITransport(app=app),
            base_url="http://agent",
            headers={**AUTH, "x-workspace-socket": "test"},
        ) as client:
            await client.post("/assign", json=assignment(sid))
            item = (
                await invoke(
                    client,
                    sid,
                    "upload_begin",
                    path="file",
                    size_bytes=6,
                    operation_id=str(uuid4()),
                )
            )["result"]
            tid = item["transfer_id"]
            await client.post(f"/workspace/transfers/{tid}/chunks?offset=0", content=b"abc")
    assert (root / "staging" / tid).read_bytes() == b"abc"
    app = create_app(configured, FakeContainers)
    async with app.router.lifespan_context(app):
        async with httpx.AsyncClient(
            transport=httpx.ASGITransport(app=app),
            base_url="http://agent",
            headers={**AUTH, "x-workspace-socket": "test"},
        ) as client:
            events = app.state.agent.journal.events(sid, 0)
            assert not any(event.get("state") == "STOPPED" for event in events)
            result = await invoke(client, sid, "upload_status", transfer_id=tid)
            assert result["result"]["offset"] == 3
            await client.post(f"/workspace/transfers/{tid}/chunks?offset=3", content=b"def")
            assert "error" not in await invoke(client, sid, "upload_commit", transfer_id=tid)
            assert (root / "data/file").read_bytes() == b"abcdef"


async def test_resubscribe_rearms_replaced_directory(files):
    """Подписка по прежнему pathname наблюдает новый inode после resync."""
    client, sid, root, agent = files
    directory = root / "data/folder"
    directory.mkdir()
    await invoke(client, sid, "subscribe", paths=["folder"])
    directory.rename(root / "data/old")
    directory.mkdir()
    await until(lambda: agent.workspace.watch.degraded)
    await invoke(client, sid, "subscribe", paths=["folder"])
    seq = agent.workspace.watch.seq
    (directory / "new").write_bytes(b"new")
    await until(
        lambda: any(
            event["type"] == "created" and event.get("path") == "folder/new"
            for event in agent.workspace.watch.replay(seq)
        )
    )


async def test_restart_finishes_interrupted_cleanup(settings, tmp_path):
    """DRAINING после сбоя не становится STOPPED до подтверждённой очистки."""
    from runtime_agent.journal import Journal

    from .conftest import TOKEN

    root = tmp_path / "workspace"
    (root / "data").mkdir(parents=True)
    (root / "staging").mkdir(mode=0o700)
    (root / "data/file").write_bytes(b"remaining")
    (root / "staging/partial").write_bytes(b"partial")
    sid = assignment()["session_id"]
    journal = Journal(settings.state_dir)
    journal.create(sid, "python", TOKEN)
    journal.session_state(sid, "DRAINING", "cleanup_failed")
    journal.close()
    app = create_app(replace(settings, workspace_path=root), FakeContainers)
    async with app.router.lifespan_context(app):
        agent = app.state.agent
        assert agent.snapshot()["state"] == "STOPPED"
        assert agent.containers.started == 0
        assert not list((root / "data").iterdir())
        assert not list((root / "staging").iterdir())


async def test_http_range_and_if_range_semantics(files):
    """Поддержите один range, suffix, 416 и полный ответ при другом If-Range."""
    client, sid, root, _ = files
    (root / "data/имя.txt").write_bytes(b"abcdef")
    tid = (await invoke(client, sid, "download_begin", path="имя.txt", operation_id=str(uuid4())))[
        "result"
    ]["transfer_id"]
    path = f"/workspace/transfers/{tid}/download"
    for value, status, expected in [
        ("bytes=-3", 206, b"def"),
        ("bytes=2-", 206, b"cdef"),
        ("bytes=0-0", 206, b"a"),
        ("bytes=6-", 416, None),
        ("bytes=0-1,3-4", 416, None),
        ("bytes=+1-2", 416, None),
        ("bytes=-0", 416, None),
    ]:
        response = await client.get(path, headers={"Range": value})
        assert response.status_code == status
        if expected is not None:
            assert response.content == expected
    response = await client.get(path, headers={"Range": "bytes=2-", "If-Range": '"other"'})
    assert response.status_code == 200 and response.content == b"abcdef"
    assert "filename*=UTF-8''" in response.headers["content-disposition"]
    assert "%D0" in response.headers["content-disposition"]


async def test_recursive_delete_and_symlink_preserves_external_file(files):
    """Для непустой папки нужен явный recursive, ссылки не обходятся."""
    client, sid, root, _ = files
    (root / "data/folder").mkdir()
    outside = root / "outside"
    outside.write_bytes(b"keep")
    (root / "data/folder/link").symlink_to(outside)
    version = (await invoke(client, sid, "list"))["result"]["items"][0]["version"]
    denied = await invoke(
        client, sid, "delete", path="folder", version=version, operation_id=str(uuid4())
    )
    assert denied["error"]["code"] == "not_empty"
    deleted = await invoke(
        client,
        sid,
        "delete",
        path="folder",
        version=version,
        recursive=True,
        operation_id=str(uuid4()),
    )
    assert "error" not in deleted
    assert not (root / "data/folder").exists()
    assert outside.read_bytes() == b"keep"


async def test_list_and_watch_do_not_extend_idle(files):
    """Фоновое чтение и подписки не удерживают runtime без работы пользователя."""
    import time

    client, sid, _, agent = files
    agent.session.last_activity = time.monotonic() - agent.settings.idle_seconds - 1
    await invoke(client, sid, "list")
    await invoke(client, sid, "subscribe", paths=[""])
    await agent.check_session()
    assert agent.snapshot()["state"] == "STOPPED"
