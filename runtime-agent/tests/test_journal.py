import pytest

from runtime_agent.journal import Journal

from .conftest import command


def test_recovery_does_not_replay_and_lock_is_exclusive(tmp_path):
    journal = Journal(tmp_path)
    with pytest.raises(RuntimeError, match="Another agent"):
        Journal(tmp_path)
    journal.create("session", "python", "t" * 32)
    journal.accept("session", command())
    journal.accept("session", command("queued"))
    journal.transition("session", "run-1", "RUNNING")
    instance = journal.instance_id
    journal.close()
    reopened = Journal(tmp_path)
    try:
        reopened.recover()
        assert reopened.instance_id == instance
        assert reopened.execution("session", "run-1")["state"] == "UNKNOWN"
        assert reopened.execution("session", "queued")["state"] == "UNKNOWN"
        assert reopened.session("session")["reason"] == "agent_restarted"
        events = reopened.events("session", 0)
        assert events[-2]["state"] == "UNKNOWN"
        assert events[-1]["state"] == "STOPPED"
        assert reopened.existing("session", command())
    finally:
        reopened.close()


def test_event_page_limits_bytes_without_losing_large_first_event(tmp_path):
    """Проверяет целостность большого первого события и чтение следующей страницы."""
    journal = Journal(tmp_path)
    try:
        journal.create("session", "python", "t" * 32)
        journal.event("session", "large", text="x" * (17 * 1024 * 1024))
        journal.event("session", "tail", text="tail")
        initial = journal.events("session", 0)
        page = journal.events("session", initial[-1]["seq"])
        assert len(page) == 1
        assert page[0]["kind"] == "large"
        tail = journal.events("session", page[-1]["seq"])
        assert tail[0]["kind"] == "tail"
    finally:
        journal.close()
