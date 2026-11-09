"""Only the event-loop thread accesses SQLite; state and events commit together."""

import fcntl
import hashlib
import json
import sqlite3
import time
import uuid

from .models import AgentError


def encode(value):
    """Serialize a value as compact UTF-8-compatible JSON text.

    Args:
        value (object): JSON-serializable value.

    Returns:
        str: JSON preserving Unicode characters.

    Raises:
        TypeError: The value contains an unsupported object.
        ValueError: The value contains non-finite numbers or circular references.
    """
    return json.dumps(value, ensure_ascii=False, separators=(",", ":"), allow_nan=False)


class Journal:
    """Persist session state, commands, and replayable events in SQLite.

    Use only from the owning event-loop thread. State transitions and their associated
    events commit together. A filesystem lock prevents a second process owning the
    directory.

    Attributes:
        lock (io.TextIOWrapper): Open file holding the exclusive process lock.
        db (sqlite3.Connection): SQLite connection used by the event-loop thread.
        instance_id (str): Persistent agent UUID used to label owned containers.
    """

    def __init__(self, directory):
        """Lock a state directory and initialize the persistent schema.

        Args:
            directory (pathlib.Path): Directory for the lock and SQLite database.

        Raises:
            RuntimeError: The exclusive state lock cannot be acquired.
            OSError: The state directory or lock file cannot be created.
            sqlite3.Error: The database cannot be opened or initialized.
        """
        directory.mkdir(parents=True, exist_ok=True, mode=0o700)
        self.lock = (directory / "agent.lock").open("a")
        try:
            fcntl.flock(self.lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except OSError:
            self.lock.close()
            raise RuntimeError("Another agent owns STATE_DIR") from None
        self.db = sqlite3.connect(directory / "runtime.sqlite3")
        self.db.row_factory = sqlite3.Row
        self.db.executescript("""
            PRAGMA journal_mode=WAL;
            PRAGMA synchronous=FULL;
            PRAGMA foreign_keys=ON;
            CREATE TABLE IF NOT EXISTS metadata (key TEXT PRIMARY KEY, value TEXT NOT NULL);
            CREATE TABLE IF NOT EXISTS sessions (
                id TEXT PRIMARY KEY, language TEXT NOT NULL, state TEXT NOT NULL,
                generation TEXT, reason TEXT, last_activity REAL NOT NULL
            );
            CREATE TABLE IF NOT EXISTS executions (
                session_id TEXT NOT NULL REFERENCES sessions(id), id TEXT NOT NULL,
                fingerprint TEXT NOT NULL, command TEXT NOT NULL, state TEXT NOT NULL,
                result TEXT, msg_id TEXT, generation TEXT,
                output_bytes INTEGER NOT NULL DEFAULT 0,
                truncated INTEGER NOT NULL DEFAULT 0,
                PRIMARY KEY (session_id, id)
            );
            CREATE UNIQUE INDEX IF NOT EXISTS execution_message
                ON executions(session_id, generation, msg_id);
            CREATE TABLE IF NOT EXISTS events (
                seq INTEGER PRIMARY KEY AUTOINCREMENT,
                session_id TEXT NOT NULL REFERENCES sessions(id), body TEXT NOT NULL
            );
            CREATE INDEX IF NOT EXISTS session_events ON events(session_id, seq);
        """)
        with self.db:
            self.db.execute(
                "INSERT OR IGNORE INTO metadata VALUES ('instance_id', ?)", (str(uuid.uuid4()),)
            )
        self.instance_id = self.db.execute(
            "SELECT value FROM metadata WHERE key='instance_id'"
        ).fetchone()[0]

    def close(self):
        """Close SQLite and release the exclusive process lock."""
        self.db.close()
        self.lock.close()

    def _event(self, session_id, kind, execution_id=None, **data):
        """Insert an event into the caller-owned transaction.

        Args:
            session_id (str): Canonical UUID of the session.
            kind (str): Event type.
            execution_id (str | None): Related execution, or None for session events.
            **data: Additional JSON-serializable event fields.
        """
        self.db.execute(
            "INSERT INTO events(session_id, body) VALUES (?, ?)",
            (
                session_id,
                encode(
                    dict(
                        session_id=session_id,
                        kind=kind,
                        execution_id=execution_id,
                        timestamp=time.time(),
                        **data,
                    )
                ),
            ),
        )

    def event(self, session_id, kind, execution_id=None, **data):
        """Commit a standalone event.

        Args:
            session_id (str): Canonical UUID of the session.
            kind (str): Event type.
            execution_id (str | None): Related execution, or None for session events.
            **data: Additional JSON-serializable event fields.
        """
        with self.db:
            self._event(session_id, kind, execution_id, **data)

    def session(self, session_id):
        """Read persisted session metadata.

        Args:
            session_id (str): Canonical UUID of the session.

        Returns:
            dict: Session ID, language, state, generation, reason, and last activity
            timestamp.

        Raises:
            AgentError: The session does not exist (HTTP 404).
        """
        row = self.db.execute("SELECT * FROM sessions WHERE id=?", (session_id,)).fetchone()
        if row is None:
            raise AgentError(404, "Session not found")
        return {"session_id": row["id"], **{k: row[k] for k in row.keys() if k != "id"}}

    def create(self, session_id, language, token):
        """Atomically record the assignment: a STARTING session, its token and event.

        Args:
            session_id (str): Canonical UUID of the session.
            language (str): Validated kernel language.
            token (str): Bearer token of the session API.

        Raises:
            sqlite3.IntegrityError: The session ID is already stored.
        """
        with self.db:
            self.db.execute(
                "INSERT INTO sessions VALUES (?, ?, 'STARTING', NULL, NULL, ?)",
                (session_id, language, time.time()),
            )
            self.db.execute("INSERT INTO metadata VALUES ('token', ?)", (token,))
            self._event(session_id, "session_state", state="STARTING", reason=None)

    def assignment(self):
        """Read the recorded assignment.

        Returns:
            tuple[str, str, str] | None: Session ID, language and token, or None
            before the assignment.

        Raises:
            RuntimeError: The state directory holds more than one session.
        """
        rows = self.db.execute("SELECT id, language FROM sessions").fetchall()
        if not rows:
            return None
        if len(rows) != 1:
            raise RuntimeError("STATE_DIR holds several sessions; reprovision the VPS")
        token = self.db.execute("SELECT value FROM metadata WHERE key='token'").fetchone()
        return rows[0]["id"], rows[0]["language"], token[0] if token else ""

    def session_state(self, session_id, state, reason=None, generation=None):
        """Commit a session state change and its event.

        Args:
            session_id (str): Canonical UUID of the session.
            state (str): New persisted state.
            reason (str | None): Machine-readable reason for the transition.
            generation (str | None): New kernel UUID, or None to preserve the current
                generation.
        """
        with self.db:
            self.db.execute(
                "UPDATE sessions SET state=?, reason=?, "
                "generation=COALESCE(?, generation) WHERE id=?",
                (state, reason, generation, session_id),
            )
            self._event(
                session_id,
                "session_state",
                state=state,
                reason=reason,
                generation=self.session(session_id)["generation"],
            )

    def touch(self, session_id):
        """Persist the current wall-clock time as session activity.

        Args:
            session_id (str): Canonical UUID of the session.
        """
        with self.db:
            self.db.execute(
                "UPDATE sessions SET last_activity=? WHERE id=?", (time.time(), session_id)
            )

    def existing(self, session_id, command):
        """Check whether an idempotent command already exists.

        Args:
            session_id (str): Canonical UUID of the session.
            command (dict): Validated command including execution_id, kind, and
                timeout_seconds.

        Returns:
            bool: True for an identical stored command; False for an unused execution ID.

        Raises:
            AgentError: The ID belongs to a command with different contents (HTTP 409).
        """
        row = self.db.execute(
            "SELECT fingerprint FROM executions WHERE session_id=? AND id=?",
            (session_id, command["execution_id"]),
        ).fetchone()
        if row is None:
            return False
        if row[0] != self.fingerprint(command):
            raise AgentError(409, "execution_id already used for another command")
        return True

    @staticmethod
    def fingerprint(command):
        """Hash a command using sorted JSON object keys.

        Args:
            command (dict): Validated command including execution_id, kind, and
                timeout_seconds.

        Returns:
            str: Hexadecimal SHA-256 digest used for idempotency checks.
        """
        return hashlib.sha256(json.dumps(command, sort_keys=True).encode()).hexdigest()

    def accept(self, session_id, command):
        """Commit a new QUEUED command and its state event.

        Args:
            session_id (str): Canonical UUID of the session.
            command (dict): Validated command including execution_id, kind, and
                timeout_seconds.

        Raises:
            sqlite3.IntegrityError: The execution already exists or its session is missing.
        """
        with self.db:
            self.db.execute(
                "INSERT INTO executions "
                "(session_id,id,fingerprint,command,state) VALUES (?,?,?,?, 'QUEUED')",
                (session_id, command["execution_id"], self.fingerprint(command), encode(command)),
            )
            self._event(
                session_id, "execution_state", command["execution_id"], state="QUEUED", result=None
            )

    def execution(self, session_id, execution_id):
        """Read the current state and result of a command.

        Args:
            session_id (str): Canonical UUID of the session.
            execution_id (str): Command identifier within the session.

        Returns:
            dict: Execution ID, state, and decoded result, if available.

        Raises:
            AgentError: The execution does not exist (HTTP 404).
        """
        row = self.db.execute(
            "SELECT * FROM executions WHERE session_id=? AND id=?", (session_id, execution_id)
        ).fetchone()
        if row is None:
            raise AgentError(404, "Execution not found")
        return {
            "execution_id": row["id"],
            "state": row["state"],
            "result": json.loads(row["result"]) if row["result"] else None,
        }

    def transition(self, session_id, execution_id, state, result=None, *, limit=None):
        """Commit an execution state change and its event.

        Args:
            session_id (str): Canonical UUID of the session.
            execution_id (str): Command identifier within the session.
            state (str): New persisted state.
            result (object | None): JSON-serializable result or failure details.
            limit (int | None): Cumulative output budget in bytes. When set,
                charge both stored result copies and replace an oversized result
                with a fixed truncation summary. None skips budget accounting for
                trusted lifecycle transitions.
        """
        with self.db:
            if limit is not None and result is not None:
                # Results are stored twice: in executions and in the state event.
                if not self._charge(
                    session_id, execution_id, 2 * len(encode(result).encode()), limit
                ):
                    status = "ok" if state == "SUCCEEDED" else "error"
                    result = {"status": status, "output_truncated": True}
            self.db.execute(
                "UPDATE executions SET state=?, result=? WHERE session_id=? AND id=?",
                (state, encode(result), session_id, execution_id),
            )
            self._event(session_id, "execution_state", execution_id, state=state, result=result)

    def dispatched(self, session_id, execution_id, msg_id, generation):
        """Persist kernel-message correlation before sending a command.

        Args:
            session_id (str): Canonical UUID of the session.
            execution_id (str): Command identifier within the session.
            msg_id (str): Jupyter execution request ID.
            generation (str): UUID of the kernel generation.
        """
        with self.db:
            self.db.execute(
                "UPDATE executions SET msg_id=?, generation=? WHERE session_id=? AND id=?",
                (msg_id, generation, session_id, execution_id),
            )
            self._event(
                session_id,
                "execution_dispatched",
                execution_id,
                kernel_msg_id=msg_id,
                generation=generation,
            )

    def parent(self, session_id, generation, msg_id):
        """Find an execution by its kernel generation and message ID.

        Args:
            session_id (str): Canonical UUID of the session.
            generation (str): UUID of the kernel generation.
            msg_id (str | None): Parent request ID from a kernel message.

        Returns:
            sqlite3.Row | None: Complete execution row, or None if the message is
            unattributed.
        """
        return self.db.execute(
            "SELECT * FROM executions WHERE session_id=? AND generation=? AND msg_id=?",
            (session_id, generation, msg_id),
        ).fetchone()

    def _charge(self, session_id, execution_id, size, limit):
        """Reserve output bytes within the caller-owned transaction.

        Read current counters from SQLite so multiple writes cannot use a stale
        budget. The first excess reservation records output_truncated; later
        reservations for that execution are rejected without another notification.

        Args:
            session_id (str): Canonical UUID of the session.
            execution_id (str): Execution whose output budget is charged.
            size (int): Serialized UTF-8 bytes to reserve, including stored copies.
            limit (int): Maximum cumulative output bytes for this execution.

        Returns:
            bool: True if the bytes were reserved in the current transaction;
            False if the output is already truncated or the reservation exceeds it.

        Raises:
            sqlite3.Error: Budget counters or the truncation event cannot be written.
        """
        row = self.db.execute(
            "SELECT output_bytes,truncated FROM executions WHERE session_id=? AND id=?",
            (session_id, execution_id),
        ).fetchone()
        if row["truncated"]:
            return False
        if row["output_bytes"] + size > limit:
            self.db.execute(
                "UPDATE executions SET truncated=1 WHERE session_id=? AND id=?",
                (session_id, execution_id),
            )
            self._event(session_id, "output_truncated", execution_id, limit=limit)
            return False
        self.db.execute(
            "UPDATE executions SET output_bytes=output_bytes+? WHERE session_id=? AND id=?",
            (size, session_id, execution_id),
        )
        return True

    def output(self, session_id, row, payload, limit):
        """Persist output within its cumulative budget or mark truncation once.

        Args:
            session_id (str): Canonical UUID of the session.
            row (sqlite3.Row): Current execution row, including output counters.
            payload (dict): Kernel message fields to persist.
            limit (int): Maximum serialized output size in bytes for this execution.
        """
        size = len(encode(payload).encode())
        with self.db:
            if self._charge(session_id, row["id"], size, limit):
                self._event(session_id, "kernel_message", row["id"], **payload)

    def unfinished(self, session_id, state, reason):
        """Transition all QUEUED and RUNNING commands in a session.

        Args:
            session_id (str): Canonical UUID of the session.
            state (str): New persisted state.
            reason (str): Reason stored in each execution result.
        """
        rows = self.db.execute(
            "SELECT id FROM executions WHERE session_id=? AND state IN ('QUEUED','RUNNING')",
            (session_id,),
        ).fetchall()
        for row in rows:
            self.transition(session_id, row[0], state, {"reason": reason})

    def recover(self):
        """Stop unfinished sessions and mark their pending commands UNKNOWN.

        Call after stale containers have been removed. Commands are never replayed
        automatically.
        """
        for row in self.db.execute(
            "SELECT id FROM sessions WHERE state NOT IN ('STOPPED','FAILED')"
        ).fetchall():
            self.unfinished(row[0], "UNKNOWN", "agent_restarted")
            self.session_state(row[0], "STOPPED", "agent_restarted")

    def events(self, session_id, after):
        """Возвращает до 100 событий после курсора с целевым размером страницы 16 МиБ.

        Первое слишком большое событие возвращается целиком, чтобы курсор
        мог продвинуться и следующие события оставались доступными.
        """
        rows = self.db.execute(
            "SELECT seq,body FROM events WHERE session_id=? AND seq>? ORDER BY seq LIMIT 100",
            (session_id, after),
        )
        page = []
        size = 0
        try:
            for row in rows:
                event_size = len(row["body"].encode("utf-8"))
                if page and size + event_size > 16 * 1024 * 1024:
                    break
                page.append({"seq": row["seq"], **json.loads(row["body"])})
                size += event_size
        finally:
            rows.close()
        return page
