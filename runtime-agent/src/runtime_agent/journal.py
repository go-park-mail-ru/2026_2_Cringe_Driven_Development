"""Храните состояние и события в SQLite на потоке event loop."""

import fcntl
import hashlib
import json
import sqlite3
import time
import uuid

from .models import AgentError
from .observability import LoggedConnection


def encode(value):
    """Сериализуйте значение в компактный JSON с исходным Unicode.

    Args:
        value: Значение без циклических ссылок, NaN и бесконечностей.
    """
    return json.dumps(value, ensure_ascii=False, separators=(",", ":"), allow_nan=False)


class Journal:
    """Храните состояние, команды и события одной сессии.

    Переходы состояния записываются вместе с событиями одной транзакцией.
    Файловая блокировка запрещает второму процессу открыть каталог состояния.
    """

    def __init__(self, directory):
        """Заблокируйте каталог состояния и подготовьте SQLite.

        Args:
            directory: Каталог SQLite и файла межпроцессной блокировки.
        """
        directory.mkdir(parents=True, exist_ok=True, mode=0o700)
        self.lock = (directory / "agent.lock").open("a")
        try:
            fcntl.flock(self.lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except OSError:
            self.lock.close()
            raise RuntimeError("Another agent owns STATE_DIR") from None
        self.db = sqlite3.connect(directory / "runtime.sqlite3", factory=LoggedConnection)
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
            CREATE TABLE IF NOT EXISTS stopped_execution_ids (
                session_id TEXT NOT NULL REFERENCES sessions(id), id TEXT NOT NULL,
                PRIMARY KEY (session_id, id)
            );
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
        """Закройте SQLite и освободите межпроцессную блокировку."""
        self.db.close()
        self.lock.close()

    def _event(self, session_id, kind, execution_id=None, **data):
        """Запишите событие в транзакции вызывающего кода.

        Args:
            session_id: UUID сессии.
            kind: Вид события журнала.
            execution_id: ID команды при наличии.
            **data: Поля события.
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
        """Запишите отдельное событие в собственной транзакции.

        Args:
            session_id: UUID сессии.
            kind: Вид события.
            execution_id: ID команды при наличии.
            **data: Поля события.
        """
        with self.db:
            self._event(session_id, kind, execution_id, **data)

    def session(self, session_id):
        """Прочитайте метаданные сессии по session_id."""
        row = self.db.execute("SELECT * FROM sessions WHERE id=?", (session_id,)).fetchone()
        if row is None:
            raise AgentError(404, "Session not found")
        return {"session_id": row["id"], **{k: row[k] for k in row.keys() if k != "id"}}

    def create(self, session_id, language, token):
        """Запишите назначение, токен и событие в одной транзакции.

        Args:
            session_id: Назначаемый UUID.
            language: Язык kernel.
            token: Токен внутреннего API сессии.
        """
        with self.db:
            self.db.execute(
                "INSERT INTO sessions VALUES (?, ?, 'STARTING', NULL, NULL, ?)",
                (session_id, language, time.time()),
            )
            self.db.execute("INSERT INTO metadata VALUES ('token', ?)", (token,))
            self._event(session_id, "session_state", state="STARTING", reason=None)

    def assignment(self):
        """Прочитайте сохранённое назначение и его токен."""
        rows = self.db.execute("SELECT id, language FROM sessions").fetchall()
        if not rows:
            return None
        if len(rows) != 1:
            raise RuntimeError("STATE_DIR holds several sessions; reprovision the VPS")
        token = self.db.execute("SELECT value FROM metadata WHERE key='token'").fetchone()
        return rows[0]["id"], rows[0]["language"], token[0] if token else ""

    def session_state(self, session_id, state, reason=None, generation=None):
        """Сохраните переход состояния и событие атомарно.

        Args:
            session_id: UUID сессии.
            state: Новое состояние.
            reason: Причина перехода при наличии.
            generation: Поколение kernel при наличии.
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
        """Обновите время активности сессии session_id по системным часам."""
        with self.db:
            self.db.execute(
                "UPDATE sessions SET last_activity=? WHERE id=?", (time.time(), session_id)
            )

    def existing(self, session_id, command):
        """Найдите идемпотентный повтор или обнаружьте конфликт запроса.

        Args:
            session_id: UUID сессии.
            command: Команда с ID и содержимым для сравнения.
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
        """Вычислите хеш команды command с устойчивым порядком ключей."""
        return hashlib.sha256(json.dumps(command, sort_keys=True).encode()).hexdigest()

    def accept(self, session_id, command):
        """Сохраните новую команду и событие QUEUED.

        Args:
            session_id: UUID сессии.
            command: Проверенная команда с устойчивым ID.
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
        """Прочитайте состояние и результат команды execution_id в сессии session_id."""
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
        """Сохраните состояние команды и событие в одной транзакции.

        Args:
            session_id: UUID сессии.
            execution_id: ID команды.
            state: Новое состояние.
            result: Результат при наличии.
            limit: Бюджет результата в байтах при наличии.
        """
        with self.db:
            if limit is not None and result is not None:
                # Результаты хранятся дважды: в executions и в событии состояния.
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

    def fence_commands(self, session_id, execution_ids):
        """Запретите позднюю доставку команд после подтверждённой остановки.

        Args:
            session_id: UUID сессии.
            execution_ids: ID остановленных команд.
        """
        with self.db:
            self.db.executemany(
                "INSERT OR IGNORE INTO stopped_execution_ids VALUES (?, ?)",
                ((session_id, execution_id) for execution_id in execution_ids),
            )

    def fenced(self, session_id, execution_id):
        """Проверьте барьер остановки для execution_id в сессии session_id."""
        return (
            self.db.execute(
                "SELECT 1 FROM stopped_execution_ids WHERE session_id=? AND id=?",
                (session_id, execution_id),
            ).fetchone()
            is not None
        )

    def dispatched(self, session_id, execution_id, msg_id, generation):
        """Сохраните связь команды с сообщением Jupyter до отправки.

        Args:
            session_id: UUID сессии.
            execution_id: ID команды.
            msg_id: ID сообщения Jupyter.
            generation: Поколение отправки.
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
        """Найдите команду для сообщения, включая поздний вывод.

        Args:
            session_id: UUID сессии.
            generation: Поколение kernel.
            msg_id: Родительский ID сообщения Jupyter.
        """
        return self.db.execute(
            "SELECT * FROM executions WHERE session_id=? AND generation=? AND msg_id=?",
            (session_id, generation, msg_id),
        ).fetchone()

    def _charge(self, session_id, execution_id, size, limit):
        """Зарезервируйте часть бюджета вывода в текущей транзакции.

        Args:
            session_id: UUID сессии.
            execution_id: ID команды.
            size: Размер новой записи в байтах.
            limit: Общий лимит вывода в байтах.
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
        """Запишите вывод в пределах бюджета или один раз отметьте усечение.

        Args:
            session_id: UUID сессии.
            row: Сохранённая связь сообщения с командой.
            payload: Данные вывода.
            limit: Общий бюджет вывода в байтах.
        """
        size = len(encode(payload).encode())
        with self.db:
            if self._charge(session_id, row["id"], size, limit):
                self._event(session_id, "kernel_message", row["id"], **payload)

    def unfinished(self, session_id, state, reason):
        """Переведите все ожидающие и активные команды в заданное состояние.

        Args:
            session_id: UUID сессии.
            state: Терминальное состояние команд.
            reason: Причина завершения.
        """
        rows = self.db.execute(
            "SELECT id FROM executions WHERE session_id=? AND state IN ('QUEUED','RUNNING')",
            (session_id,),
        ).fetchall()
        for row in rows:
            self.transition(session_id, row[0], state, {"reason": reason})

    def recover(self):
        """Завершите прерванные сессии и пометьте команды UNKNOWN."""
        for row in self.db.execute(
            "SELECT id FROM sessions WHERE state NOT IN ('STOPPED','FAILED')"
        ).fetchall():
            self.unfinished(row[0], "UNKNOWN", "agent_restarted")
            self.session_state(row[0], "STOPPED", "agent_restarted")

    def events(self, session_id, after):
        """Верните до 100 событий с целевым размером страницы 16 МиБ.

        Args:
            session_id: UUID сессии.
            after: Исключающий seq последнего прочитанного события.
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
