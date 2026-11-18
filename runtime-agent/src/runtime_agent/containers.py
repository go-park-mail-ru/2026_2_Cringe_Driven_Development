"""Управляйте контейнером kernel в закрытой сети Docker."""

import asyncio
import ipaddress
import logging
import os
import secrets

import docker
from docker.errors import NotFound
from docker.types import LogConfig

from .jupyter import Jupyter
from .lifecycle import complete_before_cancel

log = logging.getLogger(__name__)
LABEL = "ru.cellestial.runtime-agent"
_ROOT_ERROR = "Run the agent as a dedicated non-root user in the docker group"


class Containers:
    """Управляйте ресурсами Docker, помеченными ID этого агента."""

    def __init__(self, settings, instance_id):
        """Настройте владение ресурсами и откройте Docker-клиент.

        Args:
            settings: Проверенные параметры хоста и образа kernel.
            instance_id: Постоянный ID агента для меток владения.
        """
        if os.getuid() == 0:
            raise RuntimeError(_ROOT_ERROR)
        self.settings = settings
        self.instance_id = instance_id
        self.client = docker.from_env(timeout=10)

    async def ping(self):
        """Проверьте доступность Docker без изменения сети."""
        return await asyncio.to_thread(self.client.ping)

    def name(self):
        """Верните стабильное имя единственного контейнера агента."""
        return f"cellestial-{self.instance_id}-kernel"

    async def network(self):
        """Проверьте предварительно подготовленную сеть IPv4 bridge."""
        network = await asyncio.to_thread(self.client.networks.get, self.settings.kernel_network)
        attrs = network.attrs
        if (
            attrs.get("Driver") != "bridge"
            or attrs.get("Name") == "bridge"
            or attrs.get("Options", {}).get("com.docker.network.bridge.default_bridge") == "true"
            or not attrs.get("EnableIPv4", True)
            or attrs.get("EnableIPv6", False)
            or attrs.get("Internal", False)
        ):
            raise RuntimeError("KERNEL_NETWORK must be a dedicated IPv4 bridge with egress")
        return network

    async def recover(self):
        """Проверьте сеть и удалите устаревшие контейнеры этого агента."""
        await self.network()
        containers = await asyncio.to_thread(
            self.client.containers.list,
            all=True,
            filters={"label": f"{LABEL}={self.instance_id}"},
        )
        for container in containers:
            await asyncio.to_thread(container.remove, force=True, v=True)

    async def start(self):
        """Запустите ограниченный Python-контейнер и подключитесь к Jupyter."""
        network = await self.network()
        token = secrets.token_urlsafe(32)
        try:
            container = await complete_before_cancel(
                asyncio.to_thread(
                    lambda: self.client.containers.run(
                        self.settings.python_image,
                        name=self.name(),
                        detach=True,
                        user="65532:65532",
                        network=network.id,
                        dns=[self.settings.kernel_dns],
                        read_only=True,
                        cap_drop=["ALL"],
                        security_opt=["no-new-privileges:true"],
                        sysctls={
                            "net.ipv6.conf.all.disable_ipv6": "1",
                            "net.ipv6.conf.default.disable_ipv6": "1",
                        },
                        init=True,
                        mem_limit="1g",
                        memswap_limit="1g",
                        nano_cpus=1_000_000_000,
                        pids_limit=128,
                        labels={LABEL: self.instance_id},
                        tmpfs={
                            "/workspace": "rw,nosuid,nodev,size=256m,mode=1777",
                            "/tmp": "rw,nosuid,nodev,size=256m,mode=1777",
                            # Docker монтирует tmpfs с noexec по умолчанию.
                            # Расширения C, включая numpy, требуют mmap с правом выполнения.
                            "/packages": "rw,exec,nosuid,nodev,size=512m,mode=1777",
                        },
                        environment={
                            "JUPYTER_TOKEN": token,
                            "HOME": "/tmp",
                            "PYTHONUSERBASE": "/packages",
                            "PIP_NO_CACHE_DIR": "1",
                            "JUPYTER_RUNTIME_DIR": "/tmp/jupyter",
                            "JUPYTER_CONFIG_DIR": "/tmp/config",
                        },
                        working_dir="/workspace",
                        log_config=LogConfig(
                            type="local", config={"max-size": "1m", "max-file": "2"}
                        ),
                    ),
                )
            )
            await asyncio.to_thread(container.reload)
            address = container.attrs["NetworkSettings"]["Networks"][network.name]["IPAddress"]
            address = str(ipaddress.IPv4Address(address))
            transport = Jupyter(f"http://{address}:8888", token)
            try:
                await transport.start(self.settings.startup_seconds)
            except BaseException:
                await complete_before_cancel(transport.close())
                raise
            return transport
        except BaseException as startup_error:
            try:
                await complete_before_cancel(self.remove())
            except Exception:
                if not isinstance(startup_error, asyncio.CancelledError):
                    raise
                # Сохраняем отмену; владеющий runtime повторяет очистку и
                # записывает cleanup_failed до закрытия журнала.
                log.exception("Container cleanup failed after cancelled startup")
            raise startup_error

    async def alive(self):
        """Проверьте, работает ли контейнер kernel."""
        try:
            container = await asyncio.to_thread(self.client.containers.get, self.name())
            return container.status == "running"
        except NotFound:
            return False

    async def remove(self):
        """Удалите принадлежащий агенту контейнер вместе с временными данными."""
        try:
            container = await asyncio.to_thread(self.client.containers.get, self.name())
            if container.labels.get(LABEL) != self.instance_id:
                raise RuntimeError("Refusing to remove a foreign container")
            await asyncio.to_thread(container.remove, force=True, v=True)
        except NotFound:
            pass

    async def close(self):
        """Освободите пул соединений Docker-клиента."""
        await asyncio.to_thread(self.client.close)
