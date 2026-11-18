"""Определения проверяемых команд API и прикладных ошибок."""

from typing import Annotated
from uuid import UUID

from pydantic import BaseModel, ConfigDict, Field

ExecutionID = Annotated[str, Field(min_length=1, max_length=128)]


class Command(BaseModel):
    """Отклоняет неизвестные поля и NaN и бесконечности."""

    model_config = ConfigDict(extra="forbid", allow_inf_nan=False)


class Execute(Command):
    """Описывает команду исполнения кода в Python kernel."""

    execution_id: ExecutionID
    code: str = Field(max_length=1024 * 1024)
    timeout_seconds: float = Field(default=300, ge=1, le=86400)


class Inspect(Command):
    """Описывает инспекцию переменных в общей очереди."""

    execution_id: ExecutionID


class StopExecutions(Command):
    """Связывает повтор остановки с прежним идентификатором операции."""

    operation_id: ExecutionID
    execution_ids: list[ExecutionID] = Field(default_factory=list)


class Assign(Command):
    """Описывает однократное назначение агенту сессии."""

    session_id: UUID
    language: str = Field(max_length=32)
    token: str = Field(min_length=32, max_length=256, pattern=r"^[!-~]+$")


class AgentError(Exception):
    """Передаёт прикладную ошибку обработчику HTTP."""

    def __init__(self, status: int, detail: str):
        """Создайте прикладную ошибку для HTTP.

        Args:
            status: HTTP-код ответа.
            detail: Публичное описание ошибки.
        """
        self.status = status
        self.detail = detail
