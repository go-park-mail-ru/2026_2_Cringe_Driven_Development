-- create_user(login text, bcrypt_hash text) -> id, login, created_at, updated_at
PREPARE create_user(text, text) AS
INSERT INTO colab.app_user (login, password_hash)
VALUES ($1, $2)
RETURNING id, login, created_at, updated_at;

-- find_credentials(normalized_login text) -> id, password_hash; только для проверки пароля.
PREPARE find_credentials(text) AS
SELECT id, password_hash FROM colab.app_user WHERE login = $1;

-- get_profile(user_id integer) -> профиль и необязательный ключ аватара.
PREPARE get_profile(integer) AS
SELECT u.id, u.login, f.file_path AS avatar_path, u.created_at, u.updated_at
  FROM colab.app_user AS u
  LEFT JOIN colab.file AS f ON f.id = u.avatar_image_id
 WHERE u.id = $1;
