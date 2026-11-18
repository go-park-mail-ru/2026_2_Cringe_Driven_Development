"""Связывайте журналы HTTP и SQLite идентификатором запроса."""

import asyncio
import json
import logging
import re
import sqlite3
import time
from contextvars import ContextVar, copy_context
from uuid import uuid4

log = logging.getLogger(__name__)
request_id: ContextVar[str | None] = ContextVar("request_id", default=None)

session_id: ContextVar[str | None] = ContextVar("session_id", default=None)


class EventFormatter(logging.Formatter):
    """Сериализуйте события наблюдаемости в однострочный JSON."""

    def format(self, record):
        """Добавьте время, уровень и имя события к его полям."""
        return json.dumps(
            {
                "time": self.formatTime(record),
                "level": record.levelname,
                "event": record.getMessage(),
                **getattr(record, "event_fields", {}),
            }
        )


def configure_logging(level):
    """Подключите вывод JSON с порогом вывода level для HTTP и SQLite."""
    handler = logging.StreamHandler()
    handler.setFormatter(EventFormatter())
    log.handlers[:] = [handler]
    log.propagate = False
    log.setLevel(level.upper())


def session_task(operation, *, name, identifier):
    """Запустите фоновую задачу сессии с собственным контекстом.

    Args:
        operation: Корутина, которой владеет сессия.
        name: Имя задачи для диагностики asyncio.
        identifier: UUID сессии для связывания записей journal.
    """
    context = copy_context()
    context.run(request_id.set, None)
    context.run(session_id.set, identifier)
    return asyncio.create_task(operation, name=name, context=context)


class AccessLog:
    """Пишите одну сводку для каждого HTTP-запроса, включая отказы guard."""

    def __init__(self, app):
        """Сохраните ASGI-приложение, вызываемое внутри контекста запроса."""
        self.app = app

    async def __call__(self, scope, receive, send):
        """Получите status из ASGI-события, а маршрут из сопоставленного шаблона."""
        if scope["type"] != "http":
            return await self.app(scope, receive, send)
        candidate = dict(scope["headers"]).get(b"x-request-id", b"")
        identifier = (
            candidate.decode("ascii")
            if re.fullmatch(rb"[a-zA-Z0-9._-]{1,64}", candidate)
            else uuid4().hex
        )
        token = request_id.set(identifier)
        started = time.monotonic()
        status = 500

        async def track_status(message):
            """Запомните код и передайте ответ исходному ASGI-серверу."""
            nonlocal status
            if message["type"] == "http.response.start":
                status = message["status"]
            await send(message)

        try:
            return await self.app(scope, receive, track_status)
        finally:
            request_id.reset(token)
            log.info(
                "http.request",
                extra={
                    "event_fields": {
                        "method": scope["method"],
                        "route": getattr(scope.get("route"), "path", "unmatched"),
                        "status": status,
                        "duration_ms": (time.monotonic() - started) * 1000,
                        "request_id": identifier,
                    }
                },
            )


class LoggedConnection(sqlite3.Connection):
    """Измеряйте операции SQLite и сохраняйте исходные ошибки для journal."""

    @staticmethod
    def _run(operation, call, *args):
        """Запишите результат вызова SQLite без текста SQL и параметров."""
        started = time.monotonic()
        outcome = "error"
        try:
            result = call(*args)
            outcome = "success"
            return result
        finally:
            log.debug(
                "storage.operation",
                extra={
                    "event_fields": {
                        "storage": "sqlite",
                        "operation": operation,
                        "outcome": outcome,
                        "duration_ms": (time.monotonic() - started) * 1000,
                        "request_id": request_id.get(),
                        "session_id": session_id.get(),
                    }
                },
            )

    def execute(self, sql, parameters=(), /):
        """Выполните SQL с привязанными параметрами и верните курсор."""
        return self._run("execute", super().execute, sql, parameters)

    def executemany(self, sql, parameters, /):
        """Выполните SQL для каждого набора параметров и верните курсор."""
        return self._run("executemany", super().executemany, sql, parameters)

    def executescript(self, sql_script, /):
        """Выполните скрипт инициализации SQLite и верните курсор."""
        return self._run("executescript", super().executescript, sql_script)

    def __exit__(self, exc_type, exc_value, exc_tb, /):
        """Завершите транзакцию по правилам SQLite и запишите её исход."""
        operation = "commit" if exc_type is None else "rollback"
        return self._run(operation, super().__exit__, exc_type, exc_value, exc_tb)
