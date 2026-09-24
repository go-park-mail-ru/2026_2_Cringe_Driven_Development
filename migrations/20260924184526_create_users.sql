-- +goose Up
CREATE TABLE users (
    id            uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    login         text        NOT NULL,
    password_hash text        NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);

-- Логин уникален без учёта регистра: "Dasha" и "dasha" — один пользователь.
CREATE UNIQUE INDEX users_login_lower_key ON users (lower(login));

-- +goose Down
DROP TABLE users;
