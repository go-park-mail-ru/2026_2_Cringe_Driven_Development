-- +goose Up
-- Запустить офлайн-миграцию ноутбуков и проверку до старта API.
CREATE TABLE notebook_legacy_sources (
 notebook_id bigint PRIMARY KEY REFERENCES notebooks(id) ON DELETE CASCADE,
 file_key text NOT NULL,
 verified_at timestamptz
);
INSERT INTO notebook_legacy_sources(notebook_id,file_key) SELECT id,file_key FROM notebooks;
ALTER TABLE notebooks DROP COLUMN file_key;
ALTER TABLE notebooks
 ADD COLUMN revision text,
 ADD COLUMN preview_code text NOT NULL DEFAULT '',
 ADD COLUMN size_bytes bigint NOT NULL DEFAULT 0 CHECK(size_bytes BETWEEN 0 AND 134217728),
 ADD COLUMN header_bytes bigint NOT NULL DEFAULT 0 CHECK(header_bytes >= 0),
 ADD COLUMN nbformat integer NOT NULL DEFAULT 4,
 ADD COLUMN nbformat_minor integer NOT NULL DEFAULT 5,
 ADD COLUMN metadata jsonb,
 ADD COLUMN extra jsonb NOT NULL DEFAULT '{}',
 ADD CONSTRAINT notebooks_cells_count_check CHECK(cells_count BETWEEN 0 AND 1000);
CREATE TABLE notebook_cells (
 notebook_id bigint NOT NULL REFERENCES notebooks(id) ON DELETE CASCADE,
 cell_id text NOT NULL,
 position integer NOT NULL CHECK(position >= 0),
 kind text NOT NULL CHECK(kind IN ('code','markdown','raw')),
 source text NOT NULL CHECK(octet_length(source) <= 65536),
 source_version text NOT NULL,
 preview_candidate boolean NOT NULL,
 metadata jsonb,
 extra jsonb NOT NULL DEFAULT '{}',
 edit_bytes bigint NOT NULL CHECK(edit_bytes >= 0),
 runtime_metadata_overhead bigint NOT NULL CHECK(runtime_metadata_overhead >= -1),
 PRIMARY KEY(notebook_id,cell_id),
 CONSTRAINT notebook_cells_position_key UNIQUE(notebook_id,position) DEFERRABLE INITIALLY DEFERRED
);
CREATE INDEX notebook_cells_preview_idx ON notebook_cells(notebook_id,position) WHERE kind='code' AND preview_candidate;
-- Уникальный B-tree выше также обслуживает упорядоченный поиск по (notebook_id,position).
CREATE TABLE notebook_cell_data (
 notebook_id bigint NOT NULL,
 cell_id text NOT NULL,
 output_truncated boolean NOT NULL DEFAULT false,
 has_runtime_metadata boolean NOT NULL DEFAULT false,
 runtime_metadata_bytes bigint NOT NULL DEFAULT 0 CHECK(runtime_metadata_bytes >= 0),
 outputs jsonb CHECK(outputs IS NULL OR jsonb_typeof(outputs)='array'),
 execution_count bigint,
 has_execution_count boolean NOT NULL DEFAULT false,
 attachments jsonb,
 outputs_bytes bigint NOT NULL DEFAULT 0 CHECK(outputs_bytes >= 0),
 execution_bytes bigint NOT NULL DEFAULT 0 CHECK(execution_bytes >= 0),
 attachments_bytes bigint NOT NULL DEFAULT 0 CHECK(attachments_bytes >= 0),
 PRIMARY KEY(notebook_id,cell_id),
 FOREIGN KEY(notebook_id,cell_id) REFERENCES notebook_cells(notebook_id,cell_id) ON DELETE CASCADE
);
-- +goose Down
-- Миграция данных намеренно необратима; вместо отката восстановите бэкап БД.
-- +goose StatementBegin
DO $$ BEGIN RAISE EXCEPTION 'notebook cell migration requires restoring a backup for rollback'; END $$;
-- +goose StatementEnd
