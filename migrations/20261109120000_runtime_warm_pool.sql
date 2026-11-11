-- +goose Up
-- Пул заранее подготовленных runtime-VPS без сессии (backend#89). Захваченная VPS остаётся в пуле как
-- CLAIMED с session_id: её облачные ресурсы помечены pool_id, и reconcile по этой строке понимает, чьи они.
CREATE TABLE runtime_pool (
  id uuid PRIMARY KEY, state text NOT NULL, server_id text, volume_id text, port_id text, private_ip text,
  host_private_key_sealed bytea, host_public_key text, image_id text NOT NULL, agent_version text NOT NULL,
  session_id uuid, last_error text,
  created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now());
CREATE INDEX runtime_pool_ready ON runtime_pool (created_at) WHERE state = 'READY';
-- VPS пула, из которой получено окружение; NULL — VPS создана для сессии.
ALTER TABLE runtime_environments ADD COLUMN pool_id uuid;

-- +goose Down
ALTER TABLE runtime_environments DROP COLUMN pool_id;
DROP TABLE runtime_pool;
