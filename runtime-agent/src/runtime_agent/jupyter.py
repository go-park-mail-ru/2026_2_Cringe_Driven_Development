"""Jupyter's default JSON/binary WebSocket protocol over private TCP."""

import asyncio
import base64
import json
import struct
import uuid
from datetime import UTC, datetime

import aiohttp


async def reject_redirect(session, context, params):
    """Reject kernel-controlled HTTP and WebSocket handshake redirects.

    Prevents an untrusted Jupyter server from making the host agent follow
    redirects to addresses outside the container endpoint.

    Args:
        session (aiohttp.ClientSession): Client emitting the redirect trace.
        context (types.SimpleNamespace): Request trace context; unused.
        params (aiohttp.TraceRequestRedirectParams): Redirect response details;
            unused because every redirect is rejected.

    Raises:
        ValueError: Always, before aiohttp follows the redirect.
    """
    raise ValueError("Jupyter redirect is not allowed")


def decode_message(data):
    """Decode a frame from Jupyter's default WebSocket protocol.

    Args:
        data (str | bytes): JSON text or a binary frame containing a JSON
            message followed by buffers and their offset table.

    Returns:
        dict: Decoded message. Binary buffers are exposed as base64 strings
        in ``buffers_base64``.

    Raises:
        ValueError: The buffer count, offsets, or JSON payload are invalid.
        UnicodeDecodeError: The JSON payload cannot be decoded as Unicode.
    """
    if isinstance(data, str):
        return json.loads(data)
    if len(data) < 8:
        raise ValueError("Invalid Jupyter binary frame")
    count = struct.unpack_from("!I", data)[0]
    if count < 1 or count > (len(data) // 4) - 1:
        raise ValueError("Invalid Jupyter buffer count")
    offsets = list(struct.unpack_from(f"!{count}I", data, 4)) + [len(data)]
    if offsets[0] != 4 * (count + 1) or any(
        a > b for a, b in zip(offsets, offsets[1:], strict=False)
    ):
        raise ValueError("Invalid Jupyter buffer offsets")
    msg = json.loads(data[offsets[0] : offsets[1]])
    msg["buffers_base64"] = [
        base64.b64encode(data[a:b]).decode("ascii")
        for a, b in zip(offsets[1:-1], offsets[2:], strict=True)
    ]
    return msg


class Jupyter:
    """Manage one kernel through Jupyter Server's REST and WebSocket APIs.

    All connections use a private TCP connection. Call ``start`` before sending
    execution requests and ``close`` to release the transport, including after
    a failed startup. Only one coroutine may receive messages at a time.

    Attributes:
        base_url (str): Private HTTP endpoint of the container's Jupyter server.
        token (str): Container-specific token, separate from the agent API token.
        kernel_name (str): Registered kernelspec name, ``python3`` or ``ir``.
        kernel_id (str | None): Server-assigned ID after kernel creation.
        client_id (str): UUID identifying this client's protocol session.
        http (aiohttp.ClientSession | None): Authenticated HTTP client.
        ws (aiohttp.ClientWebSocketResponse | None): Kernel channels connection.
    """

    def __init__(self, base_url, token):
        """Configure the transport without opening connections.

        Args:
            base_url (str): Private HTTP endpoint of Jupyter Server.
            token (str): Authentication token for this session's server.
        """
        self.base_url = base_url.rstrip("/")
        self.token = token
        self.kernel_name = "python3"
        self.http = None
        self.ws = None
        self.kernel_id = None
        self.client_id = str(uuid.uuid4())

    async def request(self, method, path, **kwargs):
        """Send an authenticated request through the initialized HTTP client.

        Args:
            method (str): HTTP method.
            path (str): API path beginning with a slash.
            **kwargs: Additional arguments for ``aiohttp.ClientSession.request``.

        Returns:
            object | None: Decoded JSON body, or None for HTTP 204.

        Raises:
            aiohttp.ClientError: The connection, HTTP response, or content type
                is invalid.
            TimeoutError: The HTTP request exceeds its deadline.
            ValueError: The response body is not valid JSON.
        """
        assert self.http is not None
        async with self.http.request(method, self.base_url + path, **kwargs) as response:
            response.raise_for_status()
            return await response.json() if response.status != 204 else None

    async def start(self, startup_seconds):
        """Wait for the server, create a kernel, and confirm its readiness.

        Connection failures while polling kernelspecs are retried within the
        startup deadline. The caller must close the transport on failure.

        Args:
            startup_seconds (float): Overall startup deadline in seconds.

        Raises:
            TimeoutError: The server or kernel does not become ready in time.
            aiohttp.ClientError: Kernel creation or channel connection fails.
            ConnectionError: The channel closes before readiness is confirmed.
            ValueError: A readiness message cannot be decoded.
        """
        trace = aiohttp.TraceConfig()
        trace.on_request_redirect.append(reject_redirect)
        self.http = aiohttp.ClientSession(
            trace_configs=[trace],
            headers={"Authorization": "token " + self.token},
            timeout=aiohttp.ClientTimeout(total=10),
        )
        async with asyncio.timeout(startup_seconds):
            while True:
                try:
                    await self.request("GET", "/api/kernelspecs")
                    break
                except (aiohttp.ClientError, OSError):
                    await asyncio.sleep(0.1)
            kernel = await self.request(
                "POST", "/api/kernels", json={"name": self.kernel_name, "path": ""}
            )
            if not isinstance(kernel, dict):
                raise ValueError("Jupyter returned an invalid kernel descriptor")
            self.kernel_id = kernel["id"]
            await self.connect()
            await self.wait_ready()

    async def connect(self):
        """Open the channels WebSocket for the current kernel ID.

        Requires an initialized HTTP client and kernel ID. Enables heartbeat
        checks and limits incoming frames to 32 MiB.

        Raises:
            aiohttp.ClientError: The connection or WebSocket handshake fails.
            TimeoutError: The connection deadline expires.
        """
        assert self.http is not None
        self.ws = await self.http.ws_connect(
            f"{self.base_url}/api/kernels/{self.kernel_id}/channels?session_id={self.client_id}",
            heartbeat=10,
            max_msg_size=32 * 1024 * 1024,
        )

    async def send(self, msg_type, content, msg_id=None):
        """Send a Jupyter protocol message on the shell channel.

        Args:
            msg_type (str): Protocol message type, such as ``execute_request``.
            content (dict): JSON-serializable message content.
            msg_id (str | None): Correlation ID; a UUID is generated if omitted.

        Returns:
            str: Message ID used to correlate subsequent replies and output.

        Raises:
            aiohttp.ClientError: The WebSocket cannot send the message.
        """
        msg_id = msg_id or str(uuid.uuid4())
        assert self.ws is not None
        await self.ws.send_json(
            {
                "header": {
                    "msg_id": msg_id,
                    "session": self.client_id,
                    "username": "agent",
                    "msg_type": msg_type,
                    "version": "5.3",
                    "date": datetime.now(UTC).isoformat(),
                },
                "parent_header": {},
                "metadata": {},
                "content": content,
                "channel": "shell",
            }
        )
        return msg_id

    async def receive(self):
        """Receive and decode the next text or binary kernel message.

        Returns:
            dict: Message with binary buffers encoded as base64, when present.

        Raises:
            ConnectionError: The WebSocket closes or returns a non-data frame.
            ValueError: The frame contains invalid JSON or binary framing.
        """
        assert self.ws is not None
        message = await self.ws.receive()
        if message.type in (aiohttp.WSMsgType.TEXT, aiohttp.WSMsgType.BINARY):
            return decode_message(message.data)
        raise ConnectionError("Jupyter channel closed")

    async def wait_ready(self):
        """Wait for a kernel-info reply and its matching IOPub idle status.

        Messages with other parent IDs are consumed and ignored. The caller
        must enforce a deadline and ensure no other reader uses the WebSocket.

        Raises:
            ConnectionError: The channel closes while waiting for readiness.
            aiohttp.ClientError: Sending the kernel-info request fails.
            ValueError: A received frame cannot be decoded.
        """
        msg_id = await self.send("kernel_info_request", {})
        reply = idle = False
        while not (reply and idle):
            msg = await self.receive()
            if msg.get("parent_header", {}).get("msg_id") != msg_id:
                continue
            kind = msg["header"]["msg_type"]
            reply |= kind == "kernel_info_reply"
            idle |= kind == "status" and msg["content"].get("execution_state") == "idle"

    async def interrupt(self):
        """Request an interrupt without waiting for execution completion.

        Raises:
            aiohttp.ClientError: Jupyter Server rejects or cannot receive the request.
            TimeoutError: The HTTP request deadline expires.
        """
        await self.request("POST", f"/api/kernels/{self.kernel_id}/interrupt")

    async def restart(self, startup_seconds):
        """Restart the kernel, reconnect its channels, and confirm readiness.

        The caller must stop the existing channel reader before restarting.

        Args:
            startup_seconds (float): Overall restart deadline in seconds.

        Raises:
            TimeoutError: Restart or readiness confirmation exceeds the deadline.
            aiohttp.ClientError: The restart request or reconnection fails.
            ConnectionError: The channel closes before readiness is confirmed.
            ValueError: A readiness message cannot be decoded.
        """
        assert self.ws is not None
        async with asyncio.timeout(startup_seconds):
            await self.ws.close()
            await self.request("POST", f"/api/kernels/{self.kernel_id}/restart")
            await self.connect()
            await self.wait_ready()

    async def close(self):
        """Close opened WebSocket and HTTP resources without stopping the kernel."""
        if self.ws is not None:
            await self.ws.close()
        if self.http is not None:
            await self.http.close()
