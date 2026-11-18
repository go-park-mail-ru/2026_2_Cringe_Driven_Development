"""Завершайте операции с ресурсами перед передачей отмены вызывающему."""

import asyncio
from collections.abc import Coroutine
from typing import Any


async def complete_before_cancel[T](operation: Coroutine[Any, Any, T]) -> T:
    """Дождитесь операции даже при повторной отмене вызывающей задачи.

    Args:
        operation: Корутина, владеющая изменением ресурсов.
    """
    task = asyncio.create_task(operation)
    cancellation = None
    while True:
        try:
            result = await asyncio.shield(task)
        except asyncio.CancelledError as exc:
            if task.cancelled():
                raise
            cancellation = exc
        except Exception as exc:
            if cancellation is not None:
                raise cancellation from exc
            raise
        else:
            if cancellation is not None:
                raise cancellation
            return result
