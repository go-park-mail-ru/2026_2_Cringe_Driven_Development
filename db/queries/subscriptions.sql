-- Блокировка пользователя - ПЕРВЫЙ запрос в транзакции покупки/старта.
-- lock_user(user_id integer) -> id; ожидается ровно одна строка.
PREPARE lock_user(integer) AS
SELECT id FROM colab.app_user WHERE id = $1 FOR UPDATE;

-- Затем блокировка тарифа; ожидается ровно одна строка.
-- lock_paid_tariff(tariff_id integer) -> id, runtime_limit, ram_limit, cpu_limit, price_rub, duration.
PREPARE lock_paid_tariff(integer) AS
SELECT id, runtime_limit, ram_limit, cpu_limit, price_rub, duration
  FROM colab.subscription_type WHERE id = $1 AND price_rub > 0 FOR SHARE;

-- buy_subscription(user_id integer, tariff_id integer, promo_id integer nullable,
--                  purchased_at timestamptz) -> payment_id, subscription_id, start_time, end_time.
-- Предусловия: lock_user, затем lock_paid_tariff; purchased_at получен ПОСЛЕ блокировок.
-- Подтверждение внешней оплаты получено до BEGIN. Промокод проверяет триггер.
PREPARE buy_subscription(integer, integer, integer, timestamptz) AS
WITH period AS (
    SELECT $4 AS starts_at, $4 + duration * INTERVAL '1 second' AS ends_at
      FROM colab.subscription_type WHERE id = $2 AND price_rub > 0
), removed AS (
    DELETE FROM colab.user_subscription AS s USING period AS p
     WHERE s.user_id = $1 AND s.start_time < p.ends_at AND s.end_time > p.starts_at
    RETURNING s.subscription_type_id, s.start_time, s.end_time
), preserved AS (
    INSERT INTO colab.user_subscription (user_id, subscription_type_id, start_time, end_time)
    SELECT $1, r.subscription_type_id, r.start_time, p.starts_at
      FROM removed AS r CROSS JOIN period AS p WHERE r.start_time < p.starts_at
    UNION ALL
    SELECT $1, r.subscription_type_id, p.ends_at, r.end_time
      FROM removed AS r CROSS JOIN period AS p WHERE r.end_time > p.ends_at
    RETURNING id
), receipt AS (
    INSERT INTO colab.payment (user_id, subscription_type_id, promo_code_id, created_at, updated_at)
    SELECT $1, $2, $3, starts_at, starts_at FROM period
    RETURNING id
), granted AS (
    INSERT INTO colab.user_subscription (user_id, subscription_type_id, start_time, end_time)
    SELECT $1, $2, starts_at, ends_at FROM period
    RETURNING id, start_time, end_time
)
SELECT r.id AS payment_id, g.id AS subscription_id, g.start_time, g.end_time
  FROM receipt AS r CROSS JOIN granted AS g;
