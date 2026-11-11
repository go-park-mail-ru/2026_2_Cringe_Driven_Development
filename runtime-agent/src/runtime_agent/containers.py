"""Manage one mount-free kernel container on an infra-provisioned bridge."""

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
    """Manage only this agent's labelled Docker resources.

    Blocking Docker calls run in worker threads. Containers have no host mounts;
    infra owns the bridge network, DNS and firewall policy.

    Attributes:
        settings (Settings): Immutable runtime and network configuration.
        instance_id (str): Persistent agent UUID used for ownership labels.
        client (docker.DockerClient): Docker API client owned by this backend.
    """

    def __init__(self, settings, instance_id):
        """Configure container ownership and open the Docker client.

        Args:
            settings (Settings): Validated assignment and network configuration.
            instance_id (str): Persistent agent UUID used for names and labels.

        Raises:
            RuntimeError: The agent is running as root.
            docker.errors.DockerException: The Docker client cannot connect.
        """
        if os.getuid() == 0:
            raise RuntimeError(_ROOT_ERROR)
        self.settings = settings
        self.instance_id = instance_id
        self.client = docker.from_env(timeout=10)

    async def ping(self):
        """Check Docker without changing host or network policy.

        Returns:
            bool: True when the Docker daemon responds successfully.

        Raises:
            docker.errors.DockerException: The daemon cannot be reached or rejects
                the request.
        """
        return await asyncio.to_thread(self.client.ping)

    def name(self):
        """Build the deterministic name of this agent's only kernel container.

        The container starts before the session is assigned, so its name does not
        contain the session.

        Returns:
            str: Docker container name containing the agent UUID.
        """
        return f"cellestial-{self.instance_id}-kernel"

    async def network(self):
        """Validate the infra-provisioned IPv4 bridge network.

        Accepts a network name or ID, but rejects the resolved default bridge,
        non-bridge drivers, IPv6, disabled IPv4 and networks without egress.
        This check does not verify that infra installed the required firewall.

        Returns:
            docker.models.networks.Network: Existing dedicated bridge network.

        Raises:
            docker.errors.NotFound: The configured network does not exist.
            docker.errors.APIError: Docker cannot inspect the network.
            RuntimeError: The resolved network does not satisfy the contract.
        """
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
        """Validate the network and remove this instance's stale containers.

        Only containers matching the persistent ownership label are removed.
        No host directories are created or recursively deleted.

        Raises:
            docker.errors.DockerException: Docker cannot inspect the network, list
                owned containers or remove them.
            RuntimeError: The configured network does not satisfy the contract.
        """
        await self.network()
        containers = await asyncio.to_thread(
            self.client.containers.list,
            all=True,
            filters={"label": f"{LABEL}={self.instance_id}"},
        )
        for container in containers:
            await asyncio.to_thread(container.remove, force=True, v=True)

    async def start(self):
        """Start one restricted Python kernel container and connect to its Jupyter server.

        Jupyter uses private TCP without published ports. A fresh container token
        is generated, and startup failure triggers container cleanup. Cancellation
        waits for container creation and cleanup to finish, including repeated
        cancellation, before propagating to the caller.

        Returns:
            Jupyter: Open transport whose kernel has confirmed readiness.

        Raises:
            asyncio.CancelledError: Startup was cancelled, after creation and
                container cleanup have completed or cleanup has failed.
            docker.errors.DockerException: Network inspection or a container
                operation fails.
            RuntimeError: The network is invalid or cleanup finds a foreign container.
            TimeoutError: Jupyter does not become ready before the startup deadline.
            ConnectionError: Jupyter closes a channel before confirming readiness.
            ValueError: The container IPv4 address or a Jupyter response is invalid,
                or Jupyter attempts an HTTP redirect.
        """
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
                            "/tmp": "rw,nosuid,nodev,size=64m,mode=1777",
                        },
                        environment={
                            "JUPYTER_TOKEN": token,
                            "HOME": "/tmp",
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
        """Check whether the kernel container is running.

        Returns:
            bool: True for a running container; False if it is stopped or missing.

        Raises:
            docker.errors.APIError: Docker cannot inspect the container.
        """
        try:
            container = await asyncio.to_thread(self.client.containers.get, self.name())
            return container.status == "running"
        except NotFound:
            return False

    async def remove(self):
        """Remove the owned container and all of its temporary data.

        Missing containers are treated as already removed. Cleanup never traverses
        user-controlled directories on the host.

        Raises:
            docker.errors.APIError: Docker cannot inspect or remove the container.
            RuntimeError: The named container has another ownership label.
        """
        try:
            container = await asyncio.to_thread(self.client.containers.get, self.name())
            if container.labels.get(LABEL) != self.instance_id:
                raise RuntimeError("Refusing to remove a foreign container")
            await asyncio.to_thread(container.remove, force=True, v=True)
        except NotFound:
            pass

    async def close(self):
        """Release the Docker client's connection pool."""
        await asyncio.to_thread(self.client.close)
