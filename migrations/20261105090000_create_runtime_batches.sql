-- +goose Up
-- Фиксируем запрос и полный ответ Run all, чтобы повтор request_id вернул те же execution_ids.
CREATE TABLE runtime_batches (
 session_id uuid NOT NULL REFERENCES runtime_sessions(id),
 request_id text NOT NULL,
 request jsonb NOT NULL,
 result jsonb NOT NULL,
 PRIMARY KEY(session_id,request_id)
);
-- +goose Down
DROP TABLE runtime_batches;
