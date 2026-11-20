-- +goose Up
ALTER TABLE notebooks
    ADD COLUMN sharing_mode text NOT NULL DEFAULT 'private'
    CHECK (sharing_mode IN ('private', 'restricted', 'public'));

CREATE TABLE notebook_readers (
    notebook_id bigint NOT NULL REFERENCES notebooks(id) ON DELETE CASCADE,
    user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    PRIMARY KEY (notebook_id, user_id)
);

CREATE INDEX notebook_readers_user_id_idx
    ON notebook_readers (user_id);

-- +goose Down
DROP TABLE notebook_readers;

ALTER TABLE notebooks
    DROP COLUMN sharing_mode;
