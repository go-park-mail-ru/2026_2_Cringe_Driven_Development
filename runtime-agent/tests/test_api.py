import pytest
from fastapi.testclient import TestClient
from starlette.websockets import WebSocketDisconnect

from runtime_agent.api import create_app

from .conftest import AUTH, FakeContainers, assignment, new_session


async def test_authentication_validation_and_body_limit(env):
    agent, _, client = env
    assert (await client.get("/health", headers={"Authorization": "Bearer bad"})).status_code == 401
    assert (await client.get("/missing", headers={"Authorization": ""})).status_code == 401
    assert (await client.get("/health")).json()["ready"]
    path = ""
    assert (
        await client.post(
            path + "/executions", json={"execution_id": "a", "code": "", "timeout_seconds": 0}
        )
    ).status_code == 422
    assert (
        await client.post(
            path + "/executions", json={"execution_id": "a", "code": "", "timeout_seconds": "NaN"}
        )
    ).status_code == 422
    assert (
        await client.post(path + "/executions", content=b"x" * (2 * 1024 * 1024 + 1))
    ).status_code == 413
    assert (await client.get(path + "/events?after=-1")).status_code == 422
    assert (await client.get(path + "/events?after=999999999999999999999")).status_code == 422
    assert (await client.get(path + "/executions/missing")).status_code == 404
    assert (await client.post("/runtime/activity")).status_code == 200
    assert (await client.post(path + "/runtime/stop")).json()["stopped"]
    assert (await client.post(path + "/runtime/stop")).json()["stopped"]
    assert (await client.get(path + "/health")).status_code == 503
    assert (await client.get(path + "/events")).status_code == 200


async def test_event_pagination_is_scoped(env):
    agent, _, client = env
    sid, session = await new_session(agent)
    for i in range(110):
        agent.journal.event(sid, "test", value=i)
    await session.finish()
    response = await client.get("/events")
    first = response.json()["events"]
    assert len(first) == 100
    second = await client.get(f"/events?after={first[-1]['seq']}")
    assert second.json()["events"][0]["seq"] > first[-1]["seq"]
    assert all(e["session_id"] == sid for e in first + second.json()["events"])
    assert all(e["session_id"] == sid for e in second.json()["events"])


def test_websocket_auth_replay_and_multiple_subscribers(settings):
    app = create_app(settings, FakeContainers)
    with TestClient(app, headers=AUTH) as client:
        while client.get("/health").status_code != 200:
            pass
        assert client.post("/assign", json=assignment()).status_code == 200
        path = "/events/ws"
        for suffix, headers in [
            ("", {"Authorization": "bad"}),
            ("?after=-1", AUTH),
            ("?after=invalid", AUTH),
        ]:
            with pytest.raises(WebSocketDisconnect):
                with client.websocket_connect(path + suffix, headers=headers):
                    pass
        with client.websocket_connect(path, headers=AUTH) as first:
            first_event = first.receive_json()
            assert first_event["seq"] > 0
            with client.websocket_connect(path, headers=AUTH) as second:
                assert second.receive_json() == first_event
        cursor = client.get("/events").json()["events"][-1]["seq"]
        with client.websocket_connect(path + f"?after={cursor}", headers=AUTH) as ws:
            client.post("/runtime/drain")
            assert ws.receive_json()["state"] == "DRAINING"
