"""Координируйте подготовку kernel, назначение и остановку агента."""

import asyncio
import hmac
import logging
import time
from contextlib import suppress

from .models import AgentError
from .session import Session

log = logging.getLogger(__name__)


class Agent:
    """Владейте контейнером, назначением сессии и journal.

    До назначения агент держит готовый Python kernel и передаёт его сессии.
    При перезапуске агент восстанавливает состояние из journal без повтора кода.
    Агент владеет monitor и задачами прогрева и повторяет очистку при сбое.
    """

    def __init__(self, settings, journal, containers):
        """Свяжите зависимости агента до запуска фоновых задач.

        Args:
            settings: Проверенная конфигурация хоста.
            journal: Открытый журнал, принадлежащий текущему процессу.
            containers: Адаптер, управляющий контейнером kernel.
        """
        self.settings = settings
        self.journal = journal
        self.containers = containers
        self.session: Session | None = None
        self.token: str | None = None
        self.warm = None
        self.warming: asyncio.Task | None = None
        self.draining_warm: asyncio.Task | None = None
        self.assigning = asyncio.Lock()
        self.cleanup_pending: tuple[str, str] | None = None
        self.subscribers = set()
        self.stopping = False
        self.ready = False
        self.monitor = None
        self.started = False

    def notify(self):
        """Разбудите каждого подписчика событий независимо."""
        for wakeup in self.subscribers:
            wakeup.set()

    async def start(self):
        """Восстановите ресурсы из journal или подготовьте ожидающий kernel."""
        assignment = self.journal.assignment()
        await self.containers.ping()
        await self.containers.recover()
        self.journal.recover()
        self.started = True
        self.ready = True
        if assignment:
            session_id, language, self.token = assignment
            self.session = Session(self, session_id, language)
            # Не перезапускаем завершённое или прерванное назначение при рестарте процесса.
            self.session.closed = True
        else:
            self.warming = asyncio.create_task(self.warm_up(), name="kernel-warm-up")
        self.monitor = asyncio.create_task(self.watch(), name="runtime-monitor")

    async def warm_up(self):
        """Подготовьте kernel для будущего назначения."""
        try:
            self.warm = await self.containers.start()
            log.info("Waiting kernel is ready")
        except Exception:
            log.exception("Waiting kernel startup failed")
            return
        self.draining_warm = asyncio.create_task(self.drain_warm(self.warm), name="kernel-wait")

    async def drain_warm(self, transport):
        """Поддерживайте чтение канала ожидающего kernel.

        Args:
            transport: Открытый транспорт готового kernel.
        """
        try:
            while True:
                await transport.receive()
        except asyncio.CancelledError:
            raise
        except Exception:
            log.warning("Waiting kernel channel closed; a new kernel will be started")
            if self.warm is transport:
                self.warm = None
                with suppress(Exception):
                    await transport.close()

    async def stop_draining(self):
        """Отмените и дождитесь читателя перед передачей транспорта сессии."""
        if self.draining_warm is not None:
            self.draining_warm.cancel()
            await asyncio.gather(self.draining_warm, return_exceptions=True)
            self.draining_warm = None

    async def assign(self, session_id, language, token):
        """Назначьте сессию и передайте ей подготовленный kernel.

        Args:
            session_id: UUID назначаемой сессии.
            language: Запрошенный язык kernel, поддерживается Python.
            token: Токен доступа к HTTP API назначенной сессии.
        """
        if language != "python":
            raise AgentError(400, "Only python sessions are supported")
        async with self.assigning:
            if self.session is not None:
                if (
                    self.session.id == session_id
                    and self.token is not None
                    and hmac.compare_digest(self.token, token)
                ):
                    return self.snapshot()
                raise AgentError(409, "Runtime is assigned to another session")
            if self.warming is not None:
                await asyncio.shield(self.warming)
            await self.stop_draining()
            if self.warm is None or self.stopping or not self.ready:
                raise AgentError(503, "Kernel is not ready")
            self.journal.create(session_id, language, token)
            self.token = token
            self.session = Session(self, session_id, language)
            transport, self.warm = self.warm, None
            log.info("Assigned to session %s", session_id)
            await self.session.start(transport)
            self.notify()
            return self.snapshot()

    def assigned(self):
        """Верните назначенную сессию или ошибку недоступности."""
        if self.session is None:
            raise AgentError(503, "Runtime is not assigned")
        return self.session

    def snapshot(self):
        """Верните состояние для внутреннего health API."""
        runtime = self.session
        if runtime is None:
            return {
                "session_id": None,
                "language": None,
                "state": "WAITING",
                "generation": None,
                "reason": None,
                "last_activity": None,
                "ready": self.ready and self.warm is not None and not self.stopping,
                "draining": False,
                "active_execution_id": None,
                "queued": 0,
            }
        result = self.journal.session(runtime.id)
        return {
            **result,
            "session_id": runtime.id,
            "ready": self.ready and runtime.ready and not runtime.draining and not self.stopping,
            "draining": runtime.draining or result["state"] == "DRAINING",
            "active_execution_id": runtime.active.command["execution_id"]
            if runtime.active
            else None,
            "queued": runtime.queued(),
        }

    def live(self):
        """Верните доступную для операций сессию."""
        runtime = self.assigned()
        if not self.ready or self.stopping or runtime.closed:
            raise AgentError(503, "Runtime is stopped or unavailable")
        return runtime

    def accept(self, command):
        """Передайте команду очереди назначенной сессии.

        Args:
            command: Проверенная команда исполнения или инспекции.
        """
        sid = self.assigned().id
        if self.journal.existing(sid, command):
            return self.journal.execution(sid, command["execution_id"])
        return self.live().accept(command)

    async def stop_session(self):
        """Остановите назначенную сессию и запланируйте повтор сбойной очистки."""
        await self.assigned().finish()
        return self.snapshot()

    async def check_waiting(self):
        """Проверьте готовность ожидающего kernel и восстановите его при сбое."""
        if self.assigning.locked() or (self.warming is not None and not self.warming.done()):
            return
        if self.warm is not None and await self.containers.alive():
            return
        await self.stop_draining()
        if self.warm is not None:
            log.warning("Waiting kernel died; starting a new one")
            transport, self.warm = self.warm, None
            with suppress(Exception):
                await transport.close()
        await self.containers.remove()
        self.warming = asyncio.create_task(self.warm_up(), name="kernel-warm-up")

    async def check_session(self):
        """Проверьте назначенную сессию, активность и ожидающую очистку."""
        runtime = self.session
        if runtime is None:
            await self.check_waiting()
            return
        if runtime.closed:
            return
        if self.cleanup_pending:
            await runtime.finish(*self.cleanup_pending)
        elif not runtime.lock.locked():
            if not await self.containers.alive():
                await runtime.finish("FAILED", "kernel_died")
            elif (
                not runtime.active
                and not runtime.pending
                and runtime.queue.empty()
                and time.monotonic() - runtime.last_activity >= self.settings.idle_seconds
            ):
                await runtime.finish("STOPPED", "idle_timeout")

    async def watch(self):
        """Проверяйте состояние агента и повторяйте незавершённую очистку."""
        while True:
            await asyncio.sleep(self.settings.monitor_seconds)
            try:
                await self.containers.ping()
                self.ready = not self.stopping
                await self.check_session()
            except Exception:
                self.ready = False
                log.exception("Runtime monitor failed")

    async def close(self):
        """Дождитесь остановки фоновых задач и очистки ресурсов агента."""
        self.stopping = True
        self.ready = False
        for task in (self.monitor, self.warming, self.draining_warm):
            if task:
                task.cancel()
                await asyncio.gather(task, return_exceptions=True)
        if self.started:
            if self.session is not None:
                await self.session.finish()
            else:
                if self.warm is not None:
                    with suppress(Exception):
                        await self.warm.close()
                    self.warm = None
                try:
                    await self.containers.remove()
                except Exception:
                    log.exception("Waiting kernel cleanup failed")
        await self.containers.close()
