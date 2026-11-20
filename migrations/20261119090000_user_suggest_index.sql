-- +goose Up
CREATE INDEX users_suggest_prefix_idx ON users (lower(login) COLLATE "C", id);

-- +goose Down
DROP INDEX users_suggest_prefix_idx;
