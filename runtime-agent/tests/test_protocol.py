import base64
import json
import struct

import pytest

from runtime_agent.jupyter import decode_message


def test_default_binary_protocol_preserves_buffers():
    content = {"header": {"msg_type": "display_data"}, "content": {"data": {"text/plain": "x"}}}
    parts = [json.dumps(content).encode(), b"\x00\xff", b""]
    offsets = [4 * (len(parts) + 1)]
    for part in parts[:-1]:
        offsets.append(offsets[-1] + len(part))
    data = struct.pack("!4I", len(parts), *offsets) + b"".join(parts)
    assert decode_message(data) == {
        **content,
        "buffers_base64": [base64.b64encode(parts[1]).decode(), ""],
    }
    assert decode_message(json.dumps(content)) == content


@pytest.mark.parametrize("frame", [b"", b"00000000", struct.pack("!2I", 1, 99999)])
def test_invalid_binary_frames(frame):
    with pytest.raises(ValueError):
        decode_message(frame)


async def test_jupyter_does_not_follow_redirects_to_host_services():
    """A kernel-controlled Jupyter response must not turn the host agent into a proxy."""
    from aiohttp import web

    from runtime_agent.jupyter import Jupyter

    visited = []

    async def redirect(request):
        raise web.HTTPFound("/host-service")

    async def target(request):
        visited.append(True)
        return web.json_response({})

    app = web.Application()
    app.router.add_get("/api/kernelspecs", redirect)
    app.router.add_get("/host-service", target)
    runner = web.AppRunner(app)
    await runner.setup()
    site = web.TCPSite(runner, "127.0.0.1", 0)
    await site.start()
    port = runner.addresses[0][1]
    transport = Jupyter(f"http://127.0.0.1:{port}", "test")
    try:
        with pytest.raises(ValueError, match="redirect"):
            await transport.start(0.2)
        assert not visited
    finally:
        await transport.close()
        await runner.cleanup()
