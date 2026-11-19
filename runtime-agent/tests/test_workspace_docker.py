"""Проверяйте общий дисковый workspace настоящего kernel и файлового API."""

import os
from dataclasses import replace
from pathlib import Path
from uuid import uuid4

import docker
import httpx
import pytest

from runtime_agent.api import create_app
from runtime_agent.containers import Containers

from .conftest import AUTH, command, new_session, until
from .test_integration import completed
from .test_workspace_api import invoke


class DiskTestContainers(Containers):
    """Согласуйте временный volume в Docker-outside-Docker runner с host kernel."""

    def workspace_mounts(self):
        """Используйте общий data subpath только при явной настройке test runner."""
        mounts = super().workspace_mounts()
        volume = os.environ.get("TEST_WORKSPACE_VOLUME")
        if volume:
            mount = docker.types.Mount(target="/workspace", source=volume, type="volume")
            mount["VolumeOptions"] = {"Subpath": "data", "NoCopy": True}
            return [mount]
        return mounts


@pytest.mark.docker
async def test_kernel_and_agent_share_disk_workspace(settings):
    """Проверьте UID/GID, публикацию, restart и очистку через настоящий kernel."""
    location = os.environ.get("TEST_WORKSPACE_PATH")
    if os.environ.get("RUN_DOCKER_TESTS") != "1" or not location:
        pytest.skip("Requires an empty disposable mount in TEST_WORKSPACE_PATH")
    root = Path(location)
    assert not list((root / "data").iterdir())
    assert not list((root / "staging").iterdir())
    client = docker.from_env()
    network = client.networks.create(
        "cellestial-workspace-" + uuid4().hex[:10], driver="bridge", enable_ipv6=False
    )
    try:
        app = create_app(
            replace(settings, workspace_path=root, kernel_network=network.name), DiskTestContainers
        )
        async with app.router.lifespan_context(app):
            agent = app.state.agent
            sid, session = await new_session(agent)
            async with httpx.AsyncClient(
                transport=httpx.ASGITransport(app=app),
                base_url="http://agent",
                headers={**AUTH, "x-workspace-socket": "disk-test"},
            ) as http:
                subscribed = await invoke(http, sid, "subscribe", paths=[""])
                assert "error" not in subscribed
                agent.accept(
                    command(
                        "python-file",
                        "from pathlib import Path\nimport os\n"
                        "assert os.access('/workspace', os.W_OK), repr((os.getuid(), os.getgid(), "
                        "os.getgroups(), os.stat('/workspace')))\n"
                        "Path('/workspace/sub').mkdir()\n"
                        "Path('/workspace/sub/python').write_bytes(b'python')",
                    )
                )
                outcome = await completed(agent, sid, "python-file")
                assert outcome["state"] == "SUCCEEDED", {
                    key: (outcome["result"] or {}).get(key) for key in ("ename", "evalue", "reason")
                }
                await until(lambda: agent.workspace.watch.seq > subscribed["result"]["seq"])
                assert (root / "data/sub/python").stat().st_uid == 65532
                assert (root / "data/sub/python").stat().st_gid == 65532
                upload = (
                    await invoke(
                        http,
                        sid,
                        "upload_begin",
                        path="sub/upload",
                        size_bytes=4,
                        operation_id=str(uuid4()),
                    )
                )["result"]
                tid = upload["transfer_id"]
                assert (
                    await http.post(
                        f"/workspace/transfers/{tid}/chunks?offset=0", content=b"\x00\xffok"
                    )
                ).status_code == 200
                assert "error" not in await invoke(http, sid, "upload_commit", transfer_id=tid)
                assert (root / "data/sub/upload").stat().st_gid == 65532
                await session.restart()
                agent.accept(
                    command(
                        "read-upload",
                        "from pathlib import Path\n"
                        "assert Path('/workspace/sub/upload').read_bytes() == b'\\x00\\xffok'\n"
                        "assert Path('/workspace/sub/python').read_bytes() == b'python'",
                    )
                )
                outcome = await completed(agent, sid, "read-upload")
                assert outcome["state"] == "SUCCEEDED", outcome["result"]
                assert agent.workspace.session.id == sid
                download = (
                    await invoke(
                        http, sid, "download_begin", path="sub/python", operation_id=str(uuid4())
                    )
                )["result"]
                result = await http.get(f"/workspace/transfers/{download['transfer_id']}/download")
                assert result.content == b"python"
                await session.finish()
                assert not list((root / "data").iterdir())
                assert not list((root / "staging").iterdir())
    finally:
        network.remove()
        client.close()
