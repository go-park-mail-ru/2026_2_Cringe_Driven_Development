"""Проверяйте дедупликацию файловых операций после рестарта."""

from uuid import uuid4

import pytest

from runtime_agent.journal import Journal
from runtime_agent.models import AgentError


def test_workspace_operation_survives_restart(tmp_path):
    """Повтор известной команды возвращает исход, неизвестная не применяется снова."""
    session = str(uuid4())
    journal = Journal(tmp_path)
    try:
        journal.create(session, "python", "test-token")
        command = {"type": "mkdir", "path": "target"}
        assert journal.reserve_workspace_operation(session, "done", command) is None
        journal.finish_workspace_operation(session, "done", {"created": True})
        assert journal.reserve_workspace_operation(session, "pending", command) is None
    finally:
        journal.close()
    journal = Journal(tmp_path)
    try:
        assert journal.reserve_workspace_operation(session, "done", command) == {"created": True}
        with pytest.raises(AgentError, match="operation_outcome_unknown"):
            journal.reserve_workspace_operation(session, "pending", command)
        with pytest.raises(AgentError, match="conflict"):
            journal.reserve_workspace_operation(session, "done", {"path": "another"})
    finally:
        journal.close()
