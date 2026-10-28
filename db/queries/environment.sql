-- add_environment_files(owner_id integer, project_id integer,
--     s3_key_1 text, extension_1 text, path_1 text,
--     s3_key_2 text, extension_2 text, path_2 text) -> id, path, updated_at.
-- Пакет из двух файлов - пример VALUES; для другого размера меняется число параметров.
-- Нарушение любого ограничения откатывает весь пакет, включая file.
PREPARE add_environment_files(integer, integer, text, text, text, text, text, text) AS
WITH input(file_path, file_extension, path) AS (
    VALUES ($3, $4, $5), ($6, $7, $8)
), inserted AS (
    INSERT INTO colab.file (file_path, file_extension)
    SELECT i.file_path, i.file_extension FROM input AS i
     WHERE EXISTS (SELECT 1 FROM colab.project WHERE id = $2 AND user_id = $1)
    RETURNING id, file_path
)
INSERT INTO colab.environment_file (project_id, file_id, path)
SELECT $2, f.id, i.path FROM inserted AS f JOIN input AS i USING (file_path)
RETURNING id, path, updated_at;

-- rename_environment_file(owner_id, environment_file_id, expected_updated_at, path)
PREPARE rename_environment_file(integer, integer, timestamptz, text) AS
UPDATE colab.environment_file AS e SET path = $4
  FROM colab.project AS p
 WHERE e.id = $2 AND e.project_id = p.id AND p.user_id = $1 AND e.updated_at = $3
RETURNING e.id, e.path, e.updated_at;

-- remove_environment_file(owner_id integer, environment_file_id integer) -> file_id.
-- Удаляется связь; ключ S3 и строка file остаются до отдельной уборки.
PREPARE remove_environment_file(integer, integer) AS
DELETE FROM colab.environment_file AS e USING colab.project AS p
 WHERE e.id = $2 AND e.project_id = p.id AND p.user_id = $1
RETURNING e.file_id;
