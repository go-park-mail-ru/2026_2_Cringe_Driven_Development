"""Opt-in firewall acceptance on a disposable infra-prepared Linux VPS only."""

import asyncio
import json
import os
import socket
from dataclasses import replace
from ipaddress import IPv4Address

import pytest

from runtime_agent.api import create_app

from .conftest import command, new_session
from .test_integration import completed


@pytest.mark.network
@pytest.mark.docker
async def test_infra_firewall(settings):
    if os.environ.get("RUN_NETWORK_TESTS") != "1":
        pytest.skip("Run explicitly on a disposable VPS with infra firewall")
    # Every endpoint must be a live listener reachable from the host, otherwise
    # a closed port or broken route would give a false positive for isolation.
    endpoints = json.loads(os.environ["NETWORK_BLOCKED_ENDPOINTS"])
    assert set(endpoints) == {"host", "backend", "other_vps", "metadata", "public_infra"}
    for endpoint in endpoints.values():
        endpoint["ip"] = str(IPv4Address(endpoint["ip"]))
        assert 1 <= endpoint["port"] <= 65535
        with socket.create_connection((endpoint["ip"], endpoint["port"]), timeout=3):
            pass
    settings = replace(
        settings,
        kernel_network=os.environ["KERNEL_NETWORK"],
        kernel_dns=os.environ["KERNEL_DNS"],
        startup_seconds=60,
    )
    app = create_app(settings)
    async with app.router.lifespan_context(app):
        agent = app.state.agent
        sid, session = await new_session(agent)
        assert session.ready, agent.snapshot()
        code = f"""import socket, subprocess, sys
from pathlib import Path
socket.getaddrinfo('pypi.org', 443, family=socket.AF_INET)
subprocess.run([sys.executable, '-m', 'pip', 'install', '--no-cache-dir',
                '--no-deps', '--target', '/tmp/package-probe', 'six==1.17.0'], check=True)
for endpoint in {endpoints!r}.values():
    try:
        connection = socket.create_connection((endpoint['ip'], endpoint['port']), timeout=2)
    except OSError:
        pass
    else:
        connection.close()
        raise AssertionError('forbidden endpoint reachable: ' + repr(endpoint))
assert Path('/proc/sys/net/ipv6/conf/all/disable_ipv6').read_text().strip() == '1'
"""
        agent.accept(command("policy", code, 120))
        result = await completed_with_deadline(agent, "policy", 150)
        assert result["state"] == "SUCCEEDED", result
        # A peer on the bridge must not reach Jupyter or a user listener either.
        container = await asyncio.to_thread(
            agent.containers.client.containers.get, agent.containers.name()
        )
        address = container.attrs["NetworkSettings"]["Networks"][settings.kernel_network][
            "IPAddress"
        ]
        agent.accept(
            command(
                "listener",
                "import socket\nlistener = socket.socket()\n"
                "listener.bind(('0.0.0.0', 9999))\nlistener.listen()",
            )
        )
        assert (await completed(agent, sid, "listener"))["state"] == "SUCCEEDED"
        # Jupyter is the sole management exception for host -> container.
        with socket.create_connection((address, 8888), timeout=3):
            pass
        peer_code = f"""import socket
for port in (8888, 9999):
    try:
        connection = socket.create_connection(({address!r}, port), timeout=2)
    except OSError:
        continue
    connection.close()
    raise SystemExit('unsolicited inbound connection allowed')
"""
        await asyncio.to_thread(
            agent.containers.client.containers.run,
            settings.python_image,
            ["python", "-c", peer_code],
            network=settings.kernel_network,
            remove=True,
            read_only=True,
            cap_drop=["ALL"],
            user="65532:65532",
        )


async def completed_with_deadline(agent, execution_id, seconds):
    async with asyncio.timeout(seconds):
        while True:
            result = agent.journal.execution(agent.session.id, execution_id)
            if result["state"] not in ("QUEUED", "RUNNING"):
                return result
            await asyncio.sleep(0.05)
