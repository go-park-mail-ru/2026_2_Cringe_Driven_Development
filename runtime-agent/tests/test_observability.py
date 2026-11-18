import json
import logging
import sqlite3
from types import SimpleNamespace

import pytest

from runtime_agent.observability import (
    AccessLog,
    EventFormatter,
    LoggedConnection,
    request_id,
    session_task,
)


@pytest.mark.asyncio
async def test_http_and_journal_share_request_id_without_logging_inputs(caplog):
    messages = []
    caplog.set_level(logging.DEBUG, logger="runtime_agent.observability")

    async def application(scope, receive, send):
        scope["route"] = SimpleNamespace(path="/execution/{id}")
        db = sqlite3.connect(":memory:", factory=LoggedConnection)
        try:
            with db:
                assert db.execute("SELECT ?", ("SECRET-value",)).fetchone()[0] == "SECRET-value"

            async def background():
                assert request_id.get() is None
                db.execute("SELECT 1")

            await session_task(background(), name="test", identifier="session-test")
        finally:
            db.close()
        await send({"type": "http.response.start", "status": 200})

    async def send(message):
        messages.append(message)

    scope = {
        "type": "http",
        "method": "GET",
        "headers": [(b"x-request-id", b"req-test"), (b"authorization", b"SECRET-token")],
        "path": "/execution/SECRET-path",
    }
    await AccessLog(application)(scope, None, send)

    assert messages == [{"type": "http.response.start", "status": 200}]
    assert request_id.get() is None
    encoded = [EventFormatter().format(record) for record in caplog.records]
    events = [json.loads(line) for line in encoded]
    access = next(event for event in events if event["event"] == "http.request")
    assert access["route"] == "/execution/{id}"
    assert access["status"] == 200
    assert sum(event["request_id"] == "req-test" for event in events) == 3
    background = next(event for event in events if event.get("session_id") == "session-test")
    assert background["request_id"] is None
    assert "SECRET" not in "".join(encoded)
