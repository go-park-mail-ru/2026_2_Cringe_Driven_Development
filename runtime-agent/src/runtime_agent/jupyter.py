"""Работайте с JSON и бинарными кадрами Jupyter по закрытому TCP."""

import asyncio
import base64
import json
import struct
import uuid
from datetime import UTC, datetime

import aiohttp


async def reject_redirect(session, context, params):
    """Отклоните перенаправление до обращения к адресу вне контейнера.

    Args:
        session: HTTP-клиент, вызвавший обработчик.
        context: Контекст трассировки aiohttp.
        params: Данные отклоняемого перенаправления.
    """
    raise ValueError("Jupyter redirect is not allowed")


def decode_message(data):
    """Декодируйте кадр Jupyter и представьте бинарные буферы в base64.

    Args:
        data: JSON-текст или бинарный кадр с таблицей смещений.
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
    """Управляйте одним kernel через REST и WebSocket.

    Транспорт работает по закрытому TCP. Только одна корутина читает канал.
    Вызывающий закрывает транспорт и при сбое запуска.
    """

    def __init__(self, base_url, token):
        """Настройте транспорт без подключения.

        Args:
            base_url: Закрытый HTTP-адрес Jupyter Server в контейнере.
            token: Токен Jupyter Server, отдельный от токена API агента.
        """
        self.base_url = base_url.rstrip("/")
        self.token = token
        self.kernel_name = "python3"
        self.http = None
        self.ws = None
        self.kernel_id = None
        self.client_id = str(uuid.uuid4())

    async def request(self, method, path, **kwargs):
        """Отправьте авторизованный HTTP-запрос.

        Args:
            method: Метод HTTP.
            path: Путь API, начинающийся с косой черты.
            **kwargs: Дополнительные аргументы aiohttp.
        """
        assert self.http is not None
        async with self.http.request(method, self.base_url + path, **kwargs) as response:
            response.raise_for_status()
            return await response.json() if response.status != 204 else None

    async def start(self, startup_seconds):
        """Создайте kernel и дождитесь его готовности.

        Args:
            startup_seconds: Бюджет запуска в секундах.
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
        """Откройте WebSocket для каналов созданного kernel."""
        assert self.http is not None
        self.ws = await self.http.ws_connect(
            f"{self.base_url}/api/kernels/{self.kernel_id}/channels?session_id={self.client_id}",
            heartbeat=10,
            max_msg_size=32 * 1024 * 1024,
        )

    async def send(self, msg_type, content, msg_id=None):
        """Отправьте сообщение по каналу Jupyter и верните его ID.

        Args:
            msg_type: Тип сообщения протокола Jupyter.
            content: Поля содержимого сообщения.
            msg_id: Идентификатор для сопоставления ответа или None для нового UUID.
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
        """Прочитайте и декодируйте следующее сообщение kernel."""
        assert self.ws is not None
        message = await self.ws.receive()
        if message.type in (aiohttp.WSMsgType.TEXT, aiohttp.WSMsgType.BINARY):
            return decode_message(message.data)
        raise ConnectionError("Jupyter channel closed")

    async def wait_ready(self):
        """Дождитесь подтверждения готовности по протоколу Jupyter."""
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
        """Запросите прерывание kernel через REST API."""
        await self.request("POST", f"/api/kernels/{self.kernel_id}/interrupt")

    async def restart(self, startup_seconds):
        """Перезапустите kernel и заново откройте его каналы.

        Args:
            startup_seconds: Бюджет ожидания готовности в секундах.
        """
        assert self.ws is not None
        async with asyncio.timeout(startup_seconds):
            await self.ws.close()
            await self.request("POST", f"/api/kernels/{self.kernel_id}/restart")
            await self.connect()
            await self.wait_ready()

    async def close(self):
        """Закройте WebSocket и HTTP-клиент после работы или сбоя запуска."""
        if self.ws is not None:
            await self.ws.close()
        if self.http is not None:
            await self.http.close()
