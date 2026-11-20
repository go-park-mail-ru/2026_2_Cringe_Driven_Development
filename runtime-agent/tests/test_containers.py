"""Container recovery must not require access to user-controlled files."""

from dataclasses import replace
from types import SimpleNamespace
from uuid import uuid4

import pytest
from docker.errors import NotFound

from runtime_agent.containers import Containers


async def test_recovery_only_removes_owned_containers(settings, monkeypatch):
    removed = []
    instance = str(uuid4())

    class Docker:
        containers = None
        networks = None

        def list(self, **kwargs):
            assert kwargs["filters"] == {"label": f"ru.cellestial.runtime-agent={instance}"}
            return [SimpleNamespace(remove=lambda **_: removed.append("owned"))]

        def get(self, _):
            return SimpleNamespace(
                attrs={"Driver": "bridge", "EnableIPv6": False, "Internal": False}
            )

    client = Docker()
    client.containers = client.networks = client
    monkeypatch.setattr("runtime_agent.containers.docker.from_env", lambda **_: client)
    monkeypatch.setattr("runtime_agent.containers.os.getuid", lambda: 1000)
    backend = Containers(settings, instance)
    foreign = settings.state_dir.parent / "another-service"
    foreign.mkdir()
    (foreign / "keep").write_text("unrelated data")
    await backend.recover()
    assert removed == ["owned"]
    assert (foreign / "keep").read_text() == "unrelated data"


@pytest.mark.parametrize(
    "attrs",
    [
        None,
        {"Driver": "host"},
        {"Driver": "bridge", "EnableIPv6": True},
        {"Driver": "bridge", "Internal": True},
        {"Name": "bridge", "Driver": "bridge"},
        {"Driver": "bridge", "Options": {"com.docker.network.bridge.default_bridge": "true"}},
        {"Driver": "bridge", "EnableIPv4": False},
    ],
)
async def test_unprepared_network_is_rejected(settings, monkeypatch, attrs):
    class Networks:
        def get(self, _):
            if attrs is None:
                raise NotFound("missing")
            return SimpleNamespace(attrs=attrs)

    client = SimpleNamespace(networks=Networks())
    monkeypatch.setattr("runtime_agent.containers.docker.from_env", lambda **_: client)
    monkeypatch.setattr("runtime_agent.containers.os.getuid", lambda: 1000)
    backend = Containers(settings, str(uuid4()))
    with pytest.raises((RuntimeError, NotFound)):
        await backend.network()


def test_disk_workspace_mount_uses_only_data(settings, tmp_path, monkeypatch):
    """Подключите к kernel только пользовательское дерево ext4."""
    root = tmp_path / "workspace"
    root.mkdir()
    (root / "data").mkdir()
    (root / "staging").mkdir(mode=0o700)
    monkeypatch.setattr("runtime_agent.containers.os.path.ismount", lambda path: path == root)
    configured = replace(settings, workspace_path=root)
    backend = object.__new__(Containers)
    backend.settings = configured

    mounts = backend.workspace_mounts()
    assert len(mounts) == 1
    assert mounts[0]["Source"] == str(root / "data")
    assert mounts[0]["Target"] == "/workspace"
    assert mounts[0]["ReadOnly"] is False


def test_unmounted_disk_workspace_fails_closed(settings, tmp_path):
    """Отсутствующий mount не должен записывать файлы на системный диск."""
    backend = object.__new__(Containers)
    backend.settings = replace(settings, workspace_path=tmp_path)
    with pytest.raises(RuntimeError, match="workspace mount"):
        backend.workspace_mounts()
