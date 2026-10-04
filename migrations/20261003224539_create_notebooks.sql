-- +goose Up
-- Содержимое блокнота лежит в S3 по ключу file_key, здесь только метаданные.
CREATE TABLE notebooks (
    id          bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    owner_id    bigint      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    name        text        NOT NULL,
    file_key    text        NOT NULL UNIQUE,
    cells_count integer     NOT NULL DEFAULT 0,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

-- Под список блокнотов пользователя, свежие первыми.
CREATE INDEX notebooks_owner_id_updated_at_idx ON notebooks (owner_id, updated_at DESC);

-- +goose Down
DROP TABLE notebooks;
