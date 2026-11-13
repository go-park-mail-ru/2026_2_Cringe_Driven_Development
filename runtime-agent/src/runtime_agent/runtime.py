"""Coordinate per-session execution queues, lifecycle operations, and recovery."""

import asyncio
import hmac
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

log = logging.getLogger(__name__)


class KernelDiedError(ConnectionError):
    """Signal that Jupyter reported a dead kernel."""

    pass


@dataclass
class Active:
    """Track completion of one dispatched execution.

    Attributes:
        command (dict): Validated execution or inspection request.
        msg_id (str): Jupyter request ID used to match incoming messages.
        done (asyncio.Event): Set after both execute_reply and matching idle status
            arrive.
        reply (dict | None): Received execute_reply content, if any.
        idle (bool): Whether a matching idle status has been received.
        interrupted (bool): Whether manual interruption was requested.
    """

    command: dict
    msg_id: str
    done: asyncio.Event = field(default_factory=asyncio.Event)
    reply: dict | None = None
    idle: bool = False
    interrupted: bool = False
    finalized: asyncio.Event = field(default_factory=asyncio.Event)


class Session:
    """Serialize execution and control operations for one isolated kernel.

    The queue worker and control methods share a lock for dispatch and interrupts.
    Persisted message correlations also associate late output with completed commands.

    Attributes:
        agent (Agent): Coordinator owning this single runtime.
        id (str): Canonical UUID of the assigned session.
        language (str): Fixed kernel language, python.
        journal (Journal): Persistent commands, state and event journal.
        settings (Settings): Resource limits and lifecycle deadlines.
        lock (asyncio.Lock): Serializes dispatch and kernel control operations.
        queue (asyncio.Queue): Pending execution and inspection commands.
        transport (Jupyter | None): Open kernel transport after successful startup.
        generation (str | None): UUID changed on kernel startup or restart.
        active (Active | None): Tracker for the dispatched command.
        pending (dict | None): Dequeued command awaiting dispatch or completion.
        worker (asyncio.Task | None): Sequential command processing task.
        reader (asyncio.Task | None): Kernel channel reader task.
        ready (bool): Whether the kernel is available for execution.
        draining (bool): Whether new commands are rejected.
        closed (bool): Whether container cleanup has completed.
        unattributed_bytes (int): Output bytes stored for unknown parent IDs.
        unattributed_truncated (bool): Whether that generation's budget is exhausted.
        last_activity (float): Monotonic timestamp used for idle expiration.
    """

    def __init__(self, agent, session_id, language):
        """Initialize session state without starting its container.

        Args:
            agent (Agent): Owning coordinator and shared infrastructure.
            session_id (str): Canonical UUID of the session.
            language (str): Validated kernel language, python.
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
        """Refresh both monotonic and persisted activity timestamps."""
        self.last_activity = time.monotonic()
        self.journal.touch(self.id)

    def queued(self):
        """Count waiting commands, including one awaiting the control lock.

        Returns:
            int: Number of accepted commands that have not started running.
        """
        return self.queue.qsize() + int(self.pending is not None and self.active is None)

    async def start(self, transport):
        """Take over a prepared kernel, record readiness, and launch reader and worker tasks.

        Startup errors are logged and converted to a FAILED session with cleanup attempted.

        Args:
            transport (Jupyter): Open transport of the kernel prepared before the assignment.
        """
        try:
            async with self.lock:
                self.transport = transport
                self.generation = str(uuid.uuid4())
                self.ready = True
                self.touch()
                self.journal.session_state(self.id, "READY", generation=self.generation)
                self.journal.event(self.id, "kernel_started", generation=self.generation)
                self.reader = asyncio.create_task(self.read(), name=f"reader:{self.id}")
                self.worker = asyncio.create_task(self.run(), name=f"worker:{self.id}")
        except Exception:
            log.exception("Session startup failed: %s", self.id)
            await self.finish("FAILED", "startup_failed")
        finally:
            self.agent.notify()

    def accept(self, command):
        """Return an existing command or durably enqueue a new one.

        Args:
            command (dict): Validated command including execution_id, kind, and
                timeout_seconds.

        Returns:
            dict: Execution ID, state, and decoded result, if available.

        Raises:
            AgentError: The ID conflicts (409), the queue is full (429), or the session is
                unavailable (503).
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
        """Consume kernel messages until cancellation or a channel failure.

        Kernel death and malformed or lost channels fail the session and trigger cleanup.
        """
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
        """Journal a kernel message and update its active execution tracker.

        Args:
            msg (dict): Decoded Jupyter message with channel, header, parent_header, and
                content.

        Raises:
            KernelDiedError: Jupyter reports that the kernel is dead.
            ValueError: Serialization fails.
            KeyError: A required protocol field is missing.
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
        """Execute queued commands sequentially and persist their final states.

        Completion requires both reply and idle messages. Unconfirmed timeouts or execution
        failures stop the session; cancellation records UNKNOWN for dispatched work.
        """
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
        """Request an interrupt and await confirmation with the lock held.

        The caller must hold the session control lock so the signal cannot reach a later
        command.

        Raises:
            TimeoutError: The interrupt request or completion confirmation times out.
        """
        active = self.active
        if active is None or active.done.is_set():
            return
        active.interrupted = True
        assert self.transport is not None
        await self.transport.interrupt()
        await asyncio.wait_for(active.done.wait(), self.settings.interrupt_seconds)

    async def interrupt(self):
        """Interrupt the active command, failing the session if unconfirmed."""
        try:
            async with self.lock:
                self.touch()
                await self.interrupt_locked()
        except Exception:
            await self.finish("FAILED", "interrupt_unconfirmed")

    async def stop_executions(self, operation_id, execution_ids=()):
        """Cancel queued commands and confirm interruption without restarting kernel."""
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
        """Reject new commands, cancel queued work, and interrupt the active command."""
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
        """Mark commands still in the queue CANCELLED and release queue counters."""
        while not self.queue.empty():
            command = self.queue.get_nowait()
            self.journal.transition(
                self.id, command["execution_id"], "CANCELLED", {"reason": "session_unavailable"}
            )
            self.queue.task_done()

    async def restart(self):
        """Restart an idle kernel while preserving its workspace and journal.

        Raises:
            AgentError: The session is not ready and idle (409), or the restart fails (503).
        """
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
                self.reader = asyncio.create_task(self.read(), name=f"reader:{self.id}")
                self.ready = True
                self.touch()
                self.agent.notify()
        except AgentError:
            raise
        except Exception:
            await self.finish("FAILED", "restart_failed")
            raise AgentError(503, "Kernel restart failed") from None

    async def finish(self, state="STOPPED", reason="requested"):
        """Stop tasks and remove the container while preserving the event journal.

        Repeated calls on a closed session are harmless. Failed container removal leaves the
        session DRAINING for a monitor-driven cleanup retry.

        Args:
            state (str): Terminal session state to record after successful cleanup.
            reason (str): Reason for stopping the session.
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


class Agent:
    """Own one kernel container, its one-time session assignment and the journal.

    Before the assignment the agent keeps a fresh, unused Python kernel ready.
    POST /assign hands this kernel to the session without starting a container.
    Later process startups recover the journal and leave the assignment stopped
    without replaying code.

    Attributes:
        settings (Settings): Immutable host configuration.
        journal (Journal): Exclusively owned persistent execution journal.
        containers (Containers): Backend owning the kernel container.
        session (Session | None): The assigned runtime, or None while waiting.
        token (str | None): Bearer token of the assigned session.
        warm (Jupyter | None): Ready kernel waiting for the assignment.
        warming (asyncio.Task | None): Startup of the waiting kernel.
        draining_warm (asyncio.Task | None): Reader of the waiting kernel channel.
        assigning (asyncio.Lock): Serializes the assignment and waiting kernel checks.
        cleanup_pending (tuple[str, str] | None): Final state and reason to retry
            after container removal fails.
        subscribers (set[asyncio.Event]): Independent event-stream wakeups.
        stopping (bool): Whether shutdown has started.
        ready (bool): Whether Docker recovery and the latest health check passed.
        monitor (asyncio.Task | None): Background lifecycle monitor.
        started (bool): Whether startup passed recovery and may clean resources.
    """

    def __init__(self, settings, journal, containers):
        """Initialize the agent without starting Docker operations or tasks.

        Args:
            settings (Settings): Validated immutable host configuration.
            journal (Journal): Open journal owned by this process.
            containers (Containers): Container backend or compatible implementation.
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
        """Wake each subscriber independently."""
        for wakeup in self.subscribers:
            wakeup.set()

    async def start(self):
        """Recover owned resources, restore an assignment or prepare a waiting kernel.

        An existing assignment is recovered as stopped, and pending commands are
        never dispatched again.

        Raises:
            RuntimeError: STATE_DIR holds several sessions or the network is invalid.
            docker.errors.DockerException: Docker health or resource recovery fails.
            sqlite3.Error: Assignment metadata or recovery cannot be persisted.
        """
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
        """Start the kernel for a future session; failures are retried by the monitor."""
        try:
            self.warm = await self.containers.start()
            log.info("Waiting kernel is ready")
        except Exception:
            log.exception("Waiting kernel startup failed")
            return
        self.draining_warm = asyncio.create_task(self.drain_warm(self.warm), name="kernel-wait")

    async def drain_warm(self, transport):
        """Read the waiting kernel channel until the assignment takes it over.

        Jupyter Server pings the WebSocket and closes it when no pong arrives for
        90 s; pongs are sent only while the channel is read. A closed channel
        leaves the agent without a kernel, and the monitor starts a new one.

        Args:
            transport (Jupyter): Open transport of the waiting kernel.
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
        """Stop reading the waiting kernel channel before handing it over or closing it."""
        if self.draining_warm is not None:
            self.draining_warm.cancel()
            await asyncio.gather(self.draining_warm, return_exceptions=True)
            self.draining_warm = None

    async def assign(self, session_id, language, token):
        """Assign the agent to a session once and hand it the prepared kernel.

        A repeated request for the same session and token returns the current state.

        Args:
            session_id (str): Canonical UUID of the session.
            language (str): Requested kernel language.
            token (str): Bearer token for following session requests.

        Returns:
            dict: Session snapshot after the kernel is ready.

        Raises:
            AgentError: The language is not python (400), the agent is assigned to
                another session or token (409), or no kernel is available (503).
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
        """Return the assigned runtime.

        Returns:
            Session: Assigned runtime, which may be stopped.

        Raises:
            AgentError: The agent is not assigned yet (HTTP 503).
        """
        if self.session is None:
            raise AgentError(503, "Runtime is not assigned")
        return self.session

    def snapshot(self):
        """Combine persisted assignment state with current runtime status.

        Returns:
            dict: Assignment metadata, session_id, readiness, draining status,
            active execution ID and number of waiting commands. Before the
            assignment the state is WAITING and readiness means a ready kernel.
        """
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
        """Return the assigned runtime if the agent is available.

        Returns:
            Session: Assigned runtime, which may be draining.

        Raises:
            AgentError: The agent is unassigned, unavailable, stopping or the runtime
                is closed (HTTP 503).
        """
        runtime = self.assigned()
        if not self.ready or self.stopping or runtime.closed:
            raise AgentError(503, "Runtime is stopped or unavailable")
        return runtime

    def accept(self, command):
        """Resolve an idempotent retry or queue a command on the live runtime.

        Retries can retrieve persisted results even after the runtime stops.

        Args:
            command (dict): Validated execution or inspection request containing
                execution_id, kind and timeout_seconds.

        Returns:
            dict: Execution ID, current state and available result.

        Raises:
            AgentError: The ID is reused for different content (409), the queue
                is full (429), or the runtime is unassigned or unavailable (503).
        """
        sid = self.assigned().id
        if self.journal.existing(sid, command):
            return self.journal.execution(sid, command["execution_id"])
        return self.live().accept(command)

    async def stop_session(self):
        """Stop the assigned runtime while retaining its journal.

        Repeated stops are harmless. Container cleanup failures leave the runtime
        in DRAINING until a later cleanup attempt succeeds.

        Returns:
            dict: Assignment snapshot after the cleanup attempt.

        Raises:
            AgentError: The agent is not assigned yet (HTTP 503).
        """
        await self.assigned().finish()
        return self.snapshot()

    async def check_waiting(self):
        """Keep a live waiting kernel: replace a dead one and retry a failed startup.

        Raises:
            docker.errors.DockerException: Docker cannot inspect or remove the container.
        """
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
        """Retry cleanup, detect container death and expire an idle runtime.

        Active executions, queued work and locked control operations prevent idle
        expiration. Closed runtimes are left unchanged. Before the assignment only
        the waiting kernel is checked; idle expiration does not apply.

        Raises:
            docker.errors.DockerException: Docker cannot inspect container health.
            sqlite3.Error: A lifecycle transition cannot be persisted.
        """
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
        """Monitor Docker health and the runtime until cancellation.

        Unexpected check failures are logged and mark the agent unavailable. The
        next successful iteration restores availability without replaying commands.
        """
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
        """Stop monitoring, release owned resources and close the Docker client.

        A rejected startup is not cleaned up. The persistent journal is closed
        separately by the application's lifespan.
        """
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
