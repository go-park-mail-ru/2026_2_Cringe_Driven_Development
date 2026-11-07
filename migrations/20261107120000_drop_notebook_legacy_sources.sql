-- +goose Up
-- Перенос старых ipynb из S3 отменён: БД приложения сбрасывается полностью (backend#79).
-- Таблица сверки переноса больше не нужна.
DROP TABLE notebook_legacy_sources;

-- +goose Down
CREATE TABLE notebook_legacy_sources (
 notebook_id bigint PRIMARY KEY REFERENCES notebooks(id) ON DELETE CASCADE,
 file_key text NOT NULL,
 verified_at timestamptz
);
