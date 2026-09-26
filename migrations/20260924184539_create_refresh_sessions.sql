-- +goose Up
CREATE TABLE refresh_sessions (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    token_hash text        NOT NULL UNIQUE,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX refresh_sessions_user_id_idx ON refresh_sessions (user_id);

-- +goose Down
DROP TABLE refresh_sessions;
