-- +goose Up
-- Сессии и владение фоновыми задачами хранятся независимо от состояния процесса Go.
CREATE TABLE runtime_sessions (
 id uuid PRIMARY KEY,
 owner_id bigint NOT NULL REFERENCES users(id),
 notebook_id bigint NOT NULL REFERENCES notebooks(id),
 request_id text NOT NULL,
 language text NOT NULL CHECK(language IN ('python','r')),
 state text NOT NULL,
 data jsonb NOT NULL,
 lease_owner text NOT NULL DEFAULT '',
 lease_version bigint NOT NULL DEFAULT 0,
 lease_until timestamptz NOT NULL DEFAULT '-infinity',
 next_check timestamptz NOT NULL DEFAULT now(),
 UNIQUE(owner_id,request_id)
);
-- Пользователь занимает один слот до подтверждённого завершения и очистки сессии.
CREATE UNIQUE INDEX runtime_active_owner ON runtime_sessions(owner_id) WHERE state NOT IN ('STOPPED','FAILED');
-- Команды и их исходники сохраняются до отправки агенту; admission_order задаёт общую очередь.
CREATE TABLE runtime_executions (
 admission_order bigserial NOT NULL,
 session_id uuid NOT NULL REFERENCES runtime_sessions(id),
 id text NOT NULL,
 notebook_id bigint NOT NULL REFERENCES notebooks(id),
 cell_id text NOT NULL,
 attempt bigint NOT NULL,
 data jsonb NOT NULL,
 PRIMARY KEY(session_id,id),
 UNIQUE(notebook_id,cell_id,attempt)
);
-- Публичный курсор отделён от seq агента; уникальность agent_seq предотвращает повтор событий.
CREATE TABLE runtime_events (
 session_id uuid NOT NULL REFERENCES runtime_sessions(id),
 public_seq bigint NOT NULL,
 agent_seq bigint,
 kind text NOT NULL,
 body jsonb NOT NULL,
 PRIMARY KEY(session_id,public_seq),
 UNIQUE(session_id,agent_seq)
);
-- Отложенная запись результата в ipynb переживает перезапуск и повторяется после временного сбоя.
CREATE TABLE runtime_projections (
 next_attempt timestamptz NOT NULL DEFAULT now(),
 session_id uuid NOT NULL,
 execution_id text NOT NULL,
 version bigint NOT NULL,
 PRIMARY KEY(session_id,execution_id),
 FOREIGN KEY(session_id,execution_id) REFERENCES runtime_executions(session_id,id)
);
-- +goose Down
DROP TABLE runtime_projections;
DROP TABLE runtime_events;
DROP TABLE runtime_executions;
DROP TABLE runtime_sessions;
