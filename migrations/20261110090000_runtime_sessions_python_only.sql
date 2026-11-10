-- +goose Up
-- R больше не поддерживается (backend#97): удаляем R-сессии с зависимыми строками и разрешаем только python.
DELETE FROM runtime_projections WHERE session_id IN (SELECT id FROM runtime_sessions WHERE language = 'r');
DELETE FROM runtime_executions WHERE session_id IN (SELECT id FROM runtime_sessions WHERE language = 'r');
DELETE FROM runtime_events WHERE session_id IN (SELECT id FROM runtime_sessions WHERE language = 'r');
DELETE FROM runtime_batches WHERE session_id IN (SELECT id FROM runtime_sessions WHERE language = 'r');
DELETE FROM runtime_sessions WHERE language = 'r';
ALTER TABLE runtime_sessions DROP CONSTRAINT runtime_sessions_language_check;
ALTER TABLE runtime_sessions ADD CONSTRAINT runtime_sessions_language_check CHECK (language IN ('python'));

-- +goose Down
ALTER TABLE runtime_sessions DROP CONSTRAINT runtime_sessions_language_check;
ALTER TABLE runtime_sessions ADD CONSTRAINT runtime_sessions_language_check CHECK (language IN ('python','r'));
