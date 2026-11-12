"""Define validated API command models and application errors."""

from typing import Annotated
from uuid import UUID

from pydantic import BaseModel, ConfigDict, Field

ExecutionID = Annotated[str, Field(min_length=1, max_length=128)]


class Command(BaseModel):
    """Reject unknown command fields and non-finite numeric values."""

    model_config = ConfigDict(extra="forbid", allow_inf_nan=False)


class Execute(Command):
    """Describe a code execution request.

    Attributes:
        execution_id (str): Idempotency key scoped to the session.
        code (str): Source code to execute in the existing kernel.
        timeout_seconds (float): Execution deadline, from 1 to 86400 seconds.
    """

    execution_id: ExecutionID
    code: str = Field(max_length=1024 * 1024)
    timeout_seconds: float = Field(default=300, ge=1, le=86400)


class Inspect(Command):
    """Describe a variable inspection request queued alongside executions.

    Attributes:
        execution_id (str): Idempotency key scoped to the session.
    """

    execution_id: ExecutionID


class StopExecutions(Command):
    """Identify a retry of the same execution stop."""

    operation_id: ExecutionID
    execution_ids: list[ExecutionID] = Field(default_factory=list)


class Assign(Command):
    """Describe the one-time assignment of the agent to a session.

    Attributes:
        session_id (UUID): Session served by this VPS.
        language (str): Kernel language; only python is accepted by the agent.
        token (str): Bearer token for all following session requests.
    """

    session_id: UUID
    language: str = Field(max_length=32)
    token: str = Field(min_length=32, max_length=256, pattern=r"^[!-~]+$")


class AgentError(Exception):
    """Carry an application error to the HTTP exception handler.

    Attributes:
        status (int): HTTP status code.
        detail (str): Public error description.
    """

    def __init__(self, status: int, detail: str):
        """Store the HTTP status and public error description.

        Args:
            status (int): HTTP status code.
            detail (str): Description safe to expose to the client.
        """
        self.status = status
        self.detail = detail
