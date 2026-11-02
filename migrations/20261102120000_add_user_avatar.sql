-- +goose Up
ALTER TABLE users ADD COLUMN avatar_key text;

-- +goose Down
ALTER TABLE users DROP COLUMN avatar_key;
