-- +goose Up
DROP INDEX notebooks_owner_id_updated_at_idx;
CREATE INDEX notebooks_owner_id_updated_at_idx ON notebooks (owner_id, updated_at DESC, id DESC);

-- +goose Down
DROP INDEX notebooks_owner_id_updated_at_idx;
CREATE INDEX notebooks_owner_id_updated_at_idx ON notebooks (owner_id, updated_at DESC);
