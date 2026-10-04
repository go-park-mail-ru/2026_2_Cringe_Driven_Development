-- +goose Up
-- Проект ДЗ №1: применять к отдельной учебной БД, не к БД работающего API.
CREATE EXTENSION IF NOT EXISTS btree_gist WITH SCHEMA public;
CREATE SCHEMA colab;
SET LOCAL search_path TO colab, public;

CREATE TABLE file (
    id integer GENERATED ALWAYS AS IDENTITY NOT NULL PRIMARY KEY CHECK (id > 0),
    file_path text NOT NULL UNIQUE
        CHECK (file_path = btrim(file_path) AND char_length(file_path) > 0),
    file_extension text NOT NULL CHECK (file_extension ~ '^[a-z0-9]+$'),
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (isfinite(created_at) AND isfinite(updated_at) AND updated_at >= created_at)
);

CREATE TABLE app_user (
    id integer GENERATED ALWAYS AS IDENTITY NOT NULL PRIMARY KEY CHECK (id > 0),
    avatar_image_id integer REFERENCES file (id)
        ON UPDATE RESTRICT ON DELETE RESTRICT,
    login text NOT NULL UNIQUE CHECK (login ~ '^[a-z0-9_]{3,32}$'),
    password_hash text NOT NULL
        CHECK (password_hash ~ '^\$2[aby]\$(0[4-9]|[12][0-9]|3[01])\$[./A-Za-z0-9]{53}$'),
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (isfinite(created_at) AND isfinite(updated_at) AND updated_at >= created_at)
);

CREATE TABLE project (
    id integer GENERATED ALWAYS AS IDENTITY NOT NULL PRIMARY KEY CHECK (id > 0),
    user_id integer NOT NULL REFERENCES app_user (id)
        ON UPDATE RESTRICT ON DELETE RESTRICT,
    notebook_file_id integer NOT NULL UNIQUE REFERENCES file (id)
        ON UPDATE RESTRICT ON DELETE RESTRICT,
    name text NOT NULL
        CHECK (name = btrim(name) AND char_length(name) BETWEEN 1 AND 200),
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (isfinite(created_at) AND isfinite(updated_at) AND updated_at >= created_at)
);
CREATE INDEX project_user_idx ON project (user_id);

CREATE TABLE environment_file (
    id integer GENERATED ALWAYS AS IDENTITY NOT NULL PRIMARY KEY CHECK (id > 0),
    project_id integer NOT NULL REFERENCES project (id)
        ON UPDATE RESTRICT ON DELETE CASCADE,
    file_id integer NOT NULL UNIQUE REFERENCES file (id)
        ON UPDATE RESTRICT ON DELETE RESTRICT,
    path text NOT NULL
        CHECK (path = btrim(path) AND char_length(path) BETWEEN 1 AND 1024
            AND path !~ '^/' AND path !~ '/$'),
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT environment_file_path_key UNIQUE (project_id, path),
    CHECK (isfinite(created_at) AND isfinite(updated_at) AND updated_at >= created_at)
);

CREATE TABLE runtime_session (
    id integer GENERATED ALWAYS AS IDENTITY NOT NULL PRIMARY KEY CHECK (id > 0),
    project_id integer NOT NULL REFERENCES project (id)
        ON UPDATE RESTRICT ON DELETE RESTRICT,
    start_time timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP CHECK (isfinite(start_time)),
    end_time timestamptz,
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (end_time IS NULL OR (isfinite(end_time) AND end_time >= start_time)),
    CHECK (isfinite(created_at) AND isfinite(updated_at) AND updated_at >= created_at)
);
CREATE INDEX runtime_session_project_idx ON runtime_session (project_id);
CREATE UNIQUE INDEX runtime_session_active_key ON runtime_session (project_id)
 WHERE end_time IS NULL;

CREATE TABLE subscription_type (
    id integer GENERATED ALWAYS AS IDENTITY NOT NULL PRIMARY KEY CHECK (id > 0),
    name text NOT NULL UNIQUE
        CHECK (name = btrim(name) AND char_length(name) BETWEEN 1 AND 100),
    runtime_limit integer NOT NULL CHECK (runtime_limit > 0),
    ram_limit integer NOT NULL CHECK (ram_limit > 0),
    cpu_limit integer NOT NULL CHECK (cpu_limit > 0),
    price_rub numeric(12, 2) NOT NULL CHECK (price_rub BETWEEN 0 AND 9999999999.99),
    duration integer,
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK ((price_rub = 0 AND duration IS NULL)
        OR (price_rub > 0 AND duration IS NOT NULL AND duration > 0)),
    CHECK (isfinite(created_at) AND isfinite(updated_at) AND updated_at >= created_at)
);
CREATE UNIQUE INDEX subscription_type_default_key ON subscription_type (price_rub)
 WHERE price_rub = 0;

CREATE TABLE user_subscription (
    id integer GENERATED ALWAYS AS IDENTITY NOT NULL PRIMARY KEY CHECK (id > 0),
    user_id integer NOT NULL REFERENCES app_user (id)
        ON UPDATE RESTRICT ON DELETE RESTRICT,
    subscription_type_id integer NOT NULL REFERENCES subscription_type (id)
        ON UPDATE RESTRICT ON DELETE RESTRICT,
    start_time timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP CHECK (isfinite(start_time)),
    end_time timestamptz NOT NULL CHECK (isfinite(end_time)),
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (end_time > start_time),
    CHECK (isfinite(created_at) AND isfinite(updated_at) AND updated_at >= created_at),
    CONSTRAINT user_subscription_start_key UNIQUE (user_id, start_time)
        DEFERRABLE INITIALLY DEFERRED,
    CONSTRAINT user_subscription_end_key UNIQUE (user_id, end_time)
        DEFERRABLE INITIALLY DEFERRED,
    -- Диапазон только в выражении индекса; столбцы остаются скалярными.
    CONSTRAINT user_subscription_no_overlap EXCLUDE USING gist (
        user_id WITH =,
        tstzrange(start_time, end_time, '[)') WITH &&
    ) DEFERRABLE INITIALLY DEFERRED
);
CREATE INDEX user_subscription_type_idx ON user_subscription (subscription_type_id);

CREATE TABLE promo_code (
    id integer GENERATED ALWAYS AS IDENTITY NOT NULL PRIMARY KEY CHECK (id > 0),
    code text NOT NULL UNIQUE CHECK (code ~ '^[A-Z0-9]{4,32}$'),
    expires_at timestamptz NOT NULL CHECK (isfinite(expires_at)),
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (expires_at > created_at),
    CHECK (isfinite(created_at) AND isfinite(updated_at) AND updated_at >= created_at)
);

CREATE TABLE payment (
    id integer GENERATED ALWAYS AS IDENTITY NOT NULL PRIMARY KEY CHECK (id > 0),
    user_id integer NOT NULL REFERENCES app_user (id)
        ON UPDATE RESTRICT ON DELETE RESTRICT,
    subscription_type_id integer NOT NULL REFERENCES subscription_type (id)
        ON UPDATE RESTRICT ON DELETE RESTRICT,
    promo_code_id integer REFERENCES promo_code (id)
        ON UPDATE RESTRICT ON DELETE RESTRICT,
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    -- Один промокод засчитывается пользователю один раз; строки без промокода не ограничены.
    CONSTRAINT payment_promo_code_key UNIQUE (user_id, promo_code_id),
    CHECK (isfinite(created_at) AND isfinite(updated_at) AND updated_at >= created_at)
);
CREATE INDEX payment_type_idx ON payment (subscription_type_id);
CREATE INDEX payment_promo_code_idx ON payment (promo_code_id);

CREATE TABLE project_share (
    id integer GENERATED ALWAYS AS IDENTITY NOT NULL PRIMARY KEY CHECK (id > 0),
    project_id integer NOT NULL REFERENCES project (id)
        ON UPDATE RESTRICT ON DELETE CASCADE,
    user_id integer NOT NULL REFERENCES app_user (id)
        ON UPDATE RESTRICT ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT project_share_user_key UNIQUE (project_id, user_id),
    CHECK (isfinite(created_at) AND isfinite(updated_at) AND updated_at >= created_at)
);
CREATE INDEX project_share_user_idx ON project_share (user_id);

-- +goose Down
DROP SCHEMA colab CASCADE;
-- btree_gist не удаляем: расширение может использоваться другими схемами.
