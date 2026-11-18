"""Управляйте очередью исполнения и состоянием одной сессии."""

import asyncio
import json
import logging
import time
import uuid
from contextlib import suppress
from dataclasses import dataclass, field

from . import inspection
from .journal import encode
from .jupyter import Jupyter
from .models import AgentError
from .observability import session_task

log = logging.getLogger(__name__)


class KernelDiedError(ConnectionError):
    """Сообщает о гибели kernel по событию Jupyter."""

    pass


@dataclass
class Active:
    """Хранит состояние отправленной команды до ответа Jupyter и перехода kernel в idle."""

    command: dict
    msg_id: str
    done: asyncio.Event = field(default_factory=asyncio.Event)
    reply: dict | None = None
    idle: bool = False
    interrupted: bool = False
    finalized: asyncio.Event = field(default_factory=asyncio.Event)


class Session:
    """Управляйте исполнением и состоянием одного kernel.

    Сессия владеет задачами чтения и исполнения. Общий lock сериализует
    dispatch и команды управления. Journal связывает поздний вывод с командой
    даже после завершения её исполнения.
    """

    def __init__(self, agent, session_id, language):
        """Создайте состояние сессии до запуска её задач.

        Args:
            agent: Координатор, предоставляющий настройки и journal.
            session_id: Канонический UUID назначенной сессии.
            language: Проверенный язык kernel.
        """
        self.agent = agent
        self.id = session_id
        self.language = language
        self.journal = agent.journal
        self.settings = agent.settings
        self.lock = asyncio.Lock()
        self.queue = asyncio.Queue(maxsize=100)
        self.transport: Jupyter | None = None
        self.generation = None
        self.active = None
        self.pending = None
        self.worker = None
        self.reader = None
        self.ready = False
        self.draining = False
        self.stopping_executions = False
        self.stopped_operations: set[str] = set()
        self.closed = False
        self.unattributed_bytes = 0
        self.unattributed_truncated = False
        self.last_activity = time.monotonic()

    def touch(self):
        """Обновите время активности для idle timeout и восстановления."""
        self.last_activity = time.monotonic()
        self.journal.touch(self.id)

    def queued(self):
        """Посчитайте ожидающие команды, включая команду, ожидающую общий lock."""
        return self.queue.qsize() + int(self.pending is not None and self.active is None)

    async def start(self, transport):
        """Примите готовый транспорт и запустите задачи сессии.

        Args:
            transport: Открытое подключение к подготовленному kernel.
        """
        try:
            async with self.lock:
                self.transport = transport
                self.generation = str(uuid.uuid4())
                self.ready = True
                self.touch()
                self.journal.session_state(self.id, "READY", generation=self.generation)
                self.journal.event(self.id, "kernel_started", generation=self.generation)
                self.reader = session_task(
                    self.read(), name=f"reader:{self.id}", identifier=self.id
                )
                self.worker = session_task(self.run(), name=f"worker:{self.id}", identifier=self.id)
        except Exception:
            log.exception("Session startup failed: %s", self.id)
            await self.finish("FAILED", "startup_failed")
        finally:
            self.agent.notify()

    def accept(self, command):
        """Сохраните команду до постановки в очередь.

        Args:
            command: Проверенное исполнение или инспекция с постоянным идентификатором.
        """
        if self.stopping_executions:
            raise AgentError(409, "Execution stop is in progress")
        if self.journal.existing(self.id, command):
            return self.journal.execution(self.id, command["execution_id"])
        if not self.ready or self.draining or self.agent.stopping:
            raise AgentError(503, "Session is not accepting commands")
        if self.journal.fenced(self.id, command["execution_id"]):
            self.journal.accept(self.id, command)
            self.journal.transition(
                self.id,
                command["execution_id"],
                "CANCELLED",
                {"reason": "executions_stopped"},
            )
            self.agent.notify()
            return self.journal.execution(self.id, command["execution_id"])
        if self.queued() >= 100:
            raise AgentError(429, "Execution queue is full")
        self.journal.accept(self.id, command)
        self.queue.put_nowait(command)
        self.touch()
        self.agent.notify()
        return self.journal.execution(self.id, command["execution_id"])

    async def read(self):
        """Читайте канал kernel и передавайте сообщения обработчику сессии."""
        assert self.transport is not None
        try:
            while True:
                self.on_message(await self.transport.receive())
        except asyncio.CancelledError:
            raise
        except KernelDiedError:
            self.ready = False
            await self.finish("FAILED", "kernel_died")
        except Exception:
            log.exception("Kernel channel failed: %s", self.id)
            self.ready = False
            self.journal.event(self.id, "channel_failed", generation=self.generation)
            await self.finish("FAILED", "channel_lost")

    def on_message(self, msg):
        """Сопоставьте сообщение с активной или ранее завершённой командой.

        Args:
            msg: Декодированное сообщение Jupyter с каналом и заголовками.
        """
        parent = msg.get("parent_header", {}).get("msg_id")
        kind = msg["header"]["msg_type"]
        channel, content = msg["channel"], msg["content"]
        if kind == "status" and content.get("execution_state") == "dead":
            self.journal.event(self.id, "kernel_died", generation=self.generation)
            raise KernelDiedError("Kernel died")
        row = self.journal.parent(self.id, self.generation, parent)
        payload = {
            "channel": channel,
            "msg_type": kind,
            "content": content,
            "metadata": msg.get("metadata", {}),
            "parent_msg_id": parent,
            "generation": self.generation,
        }
        if msg.get("buffers_base64"):
            payload["buffers_base64"] = msg["buffers_base64"]
        if row is not None:
            inspect = json.loads(row["command"])["kind"] == "inspect"
            if not inspect:
                self.journal.output(self.id, row, payload, self.settings.max_output_bytes)
            active = self.active
            if active is not None and active.msg_id == parent:
                if channel == "shell" and kind == "execute_reply":
                    active.reply = content
                if channel == "iopub" and kind == "status":
                    active.idle |= content.get("execution_state") == "idle"
                if active.reply is not None and active.idle:
                    active.done.set()
        else:
            size = len(encode(payload).encode())
            if (
                self.unattributed_bytes + size <= self.settings.max_output_bytes
                and not self.unattributed_truncated
            ):
                self.unattributed_bytes += size
                self.journal.event(self.id, "unattributed_output", **payload)
            elif not self.unattributed_truncated:
                self.unattributed_truncated = True
                self.journal.event(
                    self.id,
                    "output_truncated",
                    generation=self.generation,
                    limit=self.settings.max_output_bytes,
                )
        self.agent.notify()

    async def run(self):
        """Исполняйте команды последовательно и сохраняйте завершение в journal."""
        assert self.transport is not None
        while True:
            command = await self.queue.get()
            self.pending = command
            execution_id = command["execution_id"]
            failure = None
            try:
                # Dispatch и control делят блокировку: запоздалый interrupt никогда не должен
                # попасть в следующую ячейку после естественного завершения предыдущей.
                async with self.lock:
                    if self.journal.execution(self.id, execution_id)["state"] == "CANCELLED":
                        continue
                    if not self.ready or self.draining or self.stopping_executions:
                        self.journal.transition(
                            self.id, execution_id, "CANCELLED", {"reason": "session_unavailable"}
                        )
                        continue
                    active = Active(command, str(uuid.uuid4()))
                    self.active = active
                    self.journal.transition(self.id, execution_id, "RUNNING")
                    # Корреляция сохраняется надёжно до отправки запроса на выполнение.
                    self.journal.dispatched(self.id, execution_id, active.msg_id, self.generation)
                    await asyncio.wait_for(
                        self.transport.send(
                            "execute_request",
                            inspection.request_content(command),
                            active.msg_id,
                        ),
                        10,
                    )
                timed_out = False
                try:
                    await asyncio.wait_for(active.done.wait(), command["timeout_seconds"])
                except TimeoutError:
                    timed_out = True
                    async with self.lock:
                        if not active.done.is_set():
                            await self.transport.interrupt()
                            await asyncio.wait_for(
                                active.done.wait(), self.settings.interrupt_seconds
                            )
                reply = active.reply
                if reply is None:
                    raise KernelDiedError("Missing kernel reply")
                if timed_out:
                    state, result = "TIMED_OUT", reply
                elif active.interrupted:
                    state, result = "INTERRUPTED", reply
                else:
                    state = "SUCCEEDED" if reply["status"] == "ok" else "FAILED"
                    result = reply
                if command["kind"] == "inspect" and state == "SUCCEEDED":
                    try:
                        result = inspection.result(reply)
                    except (ValueError, KeyError, SyntaxError):
                        state, result = "FAILED", {"reason": "invalid_inspection_result"}
                self.journal.transition(
                    self.id, execution_id, state, result, limit=self.settings.max_output_bytes
                )
            except asyncio.CancelledError:
                self.journal.transition(
                    self.id,
                    execution_id,
                    "UNKNOWN" if self.active else "CANCELLED",
                    {"reason": "session_stopping"},
                )
                raise
            except Exception:
                log.exception("Execution lost: %s/%s", self.id, execution_id)
                self.ready = False
                self.journal.transition(
                    self.id, execution_id, "UNKNOWN", {"reason": "kernel_result_unavailable"}
                )
                failure = "kernel_result_unavailable"
            finally:
                if self.active is not None:
                    self.active.finalized.set()
                self.active = None
                self.pending = None
                self.queue.task_done()
                self.touch()
                self.agent.notify()
            if failure:
                await self.finish("FAILED", failure)
                return

    async def interrupt_locked(self):
        """Прервите текущую команду под уже взятой блокировкой."""
        active = self.active
        if active is None or active.done.is_set():
            return
        active.interrupted = True
        assert self.transport is not None
        await self.transport.interrupt()
        await asyncio.wait_for(active.done.wait(), self.settings.interrupt_seconds)

    async def interrupt(self):
        """Сериализуйте прерывание с отправкой команды в kernel."""
        try:
            async with self.lock:
                self.touch()
                await self.interrupt_locked()
        except Exception:
            await self.finish("FAILED", "interrupt_unconfirmed")

    async def stop_executions(self, operation_id, execution_ids=()):
        """Остановите команды и сохраните результат остановки для повтора.

        Args:
            operation_id: Стабильный идентификатор операции остановки.
            execution_ids: Идентификаторы команд, известных backend.
        """
        if self.stopping_executions:
            raise AgentError(409, "Execution stop is in progress")
        if operation_id in self.stopped_operations:
            return
        if not self.ready or self.closed or self.draining:
            raise AgentError(409, "Session is not ready")
        self.stopping_executions = True
        try:
            async with self.lock:
                self.touch()
                self.journal.fence_commands(self.id, execution_ids)
                self.cancel_queued()
                if self.pending is not None and self.active is None:
                    self.journal.transition(
                        self.id,
                        self.pending["execution_id"],
                        "CANCELLED",
                        {"reason": "executions_stopped"},
                    )
                    self.pending = None
                active = self.active
                if active is not None and active.reply is not None:
                    await asyncio.wait_for(active.done.wait(), self.settings.interrupt_seconds)
                else:
                    await self.interrupt_locked()
            if active is not None:
                await asyncio.wait_for(active.finalized.wait(), self.settings.interrupt_seconds)
            if not self.ready or self.closed:
                raise RuntimeError("Session became unavailable")
            self.stopped_operations.add(operation_id)
        except asyncio.CancelledError:
            self.ready = False
            await self.finish("FAILED", "interrupt_unconfirmed")
            raise
        except Exception as exc:
            await self.finish("FAILED", "interrupt_unconfirmed")
            raise AgentError(503, "Execution stop was not confirmed") from exc
        finally:
            self.stopping_executions = False
            self.agent.notify()

    async def drain(self):
        """Запретите новые команды и начните остановку текущей."""
        async with self.lock:
            if self.closed:
                return
            self.draining = True
            self.touch()
            self.journal.session_state(self.id, "DRAINING")
            self.cancel_queued()
        await self.interrupt()
        self.agent.notify()

    def cancel_queued(self):
        """Завершите ожидающие команды без отправки в kernel."""
        while not self.queue.empty():
            command = self.queue.get_nowait()
            self.journal.transition(
                self.id, command["execution_id"], "CANCELLED", {"reason": "session_unavailable"}
            )
            self.queue.task_done()

    async def restart(self):
        """Перезапустите свободный kernel и смените поколение сессии."""
        try:
            async with self.lock:
                if (
                    self.closed
                    or self.draining
                    or not self.ready
                    or self.active
                    or self.pending
                    or not self.queue.empty()
                ):
                    raise AgentError(409, "Restart requires a ready, idle session")
                self.ready = False
                assert self.reader is not None and self.transport is not None
                self.reader.cancel()
                await asyncio.gather(self.reader, return_exceptions=True)
                await self.transport.restart(self.settings.startup_seconds)
                self.generation = str(uuid.uuid4())
                self.unattributed_bytes = 0
                self.unattributed_truncated = False
                self.journal.session_state(self.id, "READY", generation=self.generation)
                self.journal.event(self.id, "kernel_started", generation=self.generation)
                self.reader = session_task(
                    self.read(), name=f"reader:{self.id}", identifier=self.id
                )
                self.ready = True
                self.touch()
                self.agent.notify()
        except AgentError:
            raise
        except Exception:
            await self.finish("FAILED", "restart_failed")
            raise AgentError(503, "Kernel restart failed") from None

    async def finish(self, state="STOPPED", reason="requested"):
        """Отмените и дождитесь задач сессии перед очисткой контейнера.

        Args:
            state: Терминальное состояние для journal.
            reason: Причина завершения для journal и клиента.
        """
        async with self.lock:
            if self.closed:
                return
            self.ready = False
            self.draining = True
            tasks = [
                task
                for task in (self.worker, self.reader)
                if task is not None and task is not asyncio.current_task()
            ]
            for task in tasks:
                task.cancel()
            await asyncio.gather(*tasks, return_exceptions=True)
            self.cancel_queued()
            self.journal.unfinished(self.id, "UNKNOWN", reason)
            if self.transport is not None:
                with suppress(Exception):
                    await self.transport.close()
            try:
                await self.agent.containers.remove()
            except Exception:
                # Runtime остаётся недоступным, пока очистка не пройдёт. Монитор повторяет попытки.
                log.exception("Container cleanup failed: %s", self.id)
                self.agent.cleanup_pending = (state, reason)
                self.journal.session_state(self.id, "DRAINING", "cleanup_failed")
                self.agent.notify()
                return
            self.agent.cleanup_pending = None
            self.journal.session_state(self.id, state, reason)
            self.journal.event(self.id, "runtime_stopped", generation=self.generation)
            self.closed = True
            self.agent.notify()
