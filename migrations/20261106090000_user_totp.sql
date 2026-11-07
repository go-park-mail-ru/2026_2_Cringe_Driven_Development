-- +goose Up
ALTER TABLE users
    ADD COLUMN totp_secret bytea,
    ADD COLUMN totp_enabled boolean NOT NULL DEFAULT false,
    ADD COLUMN totp_last_step bigint NOT NULL DEFAULT -1,
    ADD CONSTRAINT users_totp_state CHECK (
        (totp_secret IS NULL AND NOT totp_enabled AND totp_last_step = -1)
        OR (totp_secret IS NOT NULL AND octet_length(totp_secret) > 0 AND (
            (NOT totp_enabled AND totp_last_step = -1)
            OR (totp_enabled AND totp_last_step >= 0)
        ))
    );

-- +goose Down
ALTER TABLE users
    DROP CONSTRAINT users_totp_state,
    DROP COLUMN totp_last_step,
    DROP COLUMN totp_enabled,
    DROP COLUMN totp_secret;
