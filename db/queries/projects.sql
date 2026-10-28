-- list_projects(actor_id integer) -> собственные и расшаренные проекты, без дублей.
PREPARE list_projects(integer) AS
SELECT p.id, p.user_id, p.name, p.created_at, p.updated_at
  FROM colab.project AS p
 WHERE p.user_id = $1
    OR EXISTS (SELECT 1 FROM colab.project_share AS s
                WHERE s.project_id = p.id AND s.user_id = $1)
 ORDER BY p.updated_at DESC, p.id DESC;

-- get_project(actor_id integer, project_id integer) -> проект и ключ ipynb.
PREPARE get_project(integer, integer) AS
SELECT p.id, p.user_id, p.name, f.file_path, p.created_at, p.updated_at
  FROM colab.project AS p
  JOIN colab.file AS f ON f.id = p.notebook_file_id
 WHERE p.id = $2
   AND (p.user_id = $1 OR EXISTS (
       SELECT 1 FROM colab.project_share AS s WHERE s.project_id = p.id AND s.user_id = $1));

-- create_project(owner_id integer, s3_key text, name text) -> id, name, updated_at.
-- Обе вставки атомарны; отсутствие пользователя не создаёт файл.
PREPARE create_project(integer, text, text) AS
WITH notebook AS (
    INSERT INTO colab.file (file_path, file_extension)
    SELECT $2, 'ipynb' WHERE EXISTS (SELECT 1 FROM colab.app_user WHERE id = $1)
    RETURNING id
)
INSERT INTO colab.project (user_id, notebook_file_id, name)
SELECT $1, id, $3 FROM notebook
RETURNING id, name, updated_at;

-- rename_project(owner_id integer, project_id integer, expected_updated_at timestamptz,
--                name text) -> id, name, updated_at; 0 строк означает конфликт/нет доступа.
PREPARE rename_project(integer, integer, timestamptz, text) AS
UPDATE colab.project SET name = $4
 WHERE id = $2 AND user_id = $1 AND updated_at = $3
RETURNING id, name, updated_at;

-- share_project(owner_id integer, project_id integer, recipient_id integer) -> id.
PREPARE share_project(integer, integer, integer) AS
INSERT INTO colab.project_share (project_id, user_id)
SELECT p.id, $3 FROM colab.project AS p WHERE p.id = $2 AND p.user_id = $1
ON CONFLICT (project_id, user_id) DO NOTHING
RETURNING id;

-- revoke_share(owner_id integer, project_id integer, recipient_id integer) -> id.
PREPARE revoke_share(integer, integer, integer) AS
DELETE FROM colab.project_share AS s USING colab.project AS p
 WHERE s.project_id = p.id AND p.id = $2 AND p.user_id = $1 AND s.user_id = $3
RETURNING s.id;
