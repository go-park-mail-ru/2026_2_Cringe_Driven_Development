-- +goose Up
SET LOCAL search_path TO colab, public;

-- Учебные учётные записи. Пароль обеих: DemoPass123!; хранится bcrypt-хеш.
INSERT INTO app_user (login, password_hash)
VALUES ('alice', '$2b$10$abcdefghijklmnopqrstuuqKNgT5BDubAZMfatHFaUZLSA1SDUM0S'),
       ('bob', '$2b$10$abcdefghijklmnopqrstuuqKNgT5BDubAZMfatHFaUZLSA1SDUM0S');

-- Единицы: runtime_limit и duration — секунды; ram_limit — MiB; цена — рубли.
-- Значения учебные. Месяц в этом примере — фиксированные 30 суток.
INSERT INTO subscription_type
       (name, runtime_limit, ram_limit, cpu_limit, price_rub, duration)
VALUES ('Free', 600, 512, 1, 0.00, NULL),
       ('Pro', 3600, 2048, 2, 990.00, 2592000),
       ('Max', 7200, 8192, 4, 1490.00, 604800);

-- Первоначальный срок Pro: 30 суток от начала текущего дня UTC.
-- После 10 суток: Max на неделю. Pro возобновляется до прежнего дня 30 (ещё 13 суток).
INSERT INTO user_subscription (user_id, subscription_type_id, start_time, end_time)
SELECT u.id, t.id,
       date_trunc('day', CURRENT_TIMESTAMP, 'UTC'),
       date_trunc('day', CURRENT_TIMESTAMP, 'UTC') + INTERVAL '864000 seconds'
  FROM app_user AS u
 CROSS JOIN subscription_type AS t
 WHERE u.login = 'alice'
   AND t.name = 'Pro';

INSERT INTO user_subscription (user_id, subscription_type_id, start_time, end_time)
SELECT u.id, t.id,
       date_trunc('day', CURRENT_TIMESTAMP, 'UTC') + INTERVAL '864000 seconds',
       date_trunc('day', CURRENT_TIMESTAMP, 'UTC') + INTERVAL '1468800 seconds'
  FROM app_user AS u
 CROSS JOIN subscription_type AS t
 WHERE u.login = 'alice'
   AND t.name = 'Max';

INSERT INTO user_subscription (user_id, subscription_type_id, start_time, end_time)
SELECT u.id, t.id,
       date_trunc('day', CURRENT_TIMESTAMP, 'UTC') + INTERVAL '1468800 seconds',
       date_trunc('day', CURRENT_TIMESTAMP, 'UTC') + INTERVAL '2592000 seconds'
  FROM app_user AS u
 CROSS JOIN subscription_type AS t
 WHERE u.login = 'alice'
   AND t.name = 'Pro';

-- Bob пользуется бесплатным тарифом: строки user_subscription не нужны.
-- Проекты не создаём: миграция не загружает реальные ipynb/ZIP в S3.

-- +goose Down
SET LOCAL search_path TO colab, public;
-- Откат seed предназначен только для отдельной учебной БД.
DELETE FROM user_subscription
 WHERE user_id IN (SELECT id FROM app_user WHERE login IN ('alice', 'bob'));
DELETE FROM app_user
 WHERE login IN ('alice', 'bob');
-- Административный откат единственного бесплатного тарифа.
ALTER TABLE subscription_type DISABLE TRIGGER protect_subscription_type;
DELETE FROM subscription_type
 WHERE name IN ('Free', 'Pro', 'Max');
ALTER TABLE subscription_type ENABLE TRIGGER protect_subscription_type;
