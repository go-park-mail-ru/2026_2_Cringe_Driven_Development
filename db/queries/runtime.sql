-- runtime_budget(owner_id integer, at timestamptz) -> тариф, расход, остаток и can_start.
-- at фиксируется один раз после lock_user; все интервалы полуоткрытые.
PREPARE runtime_budget(integer, timestamptz) AS
WITH bounds AS (
    SELECT date_trunc('day', $2, 'UTC') AS begins_at,
           date_trunc('day', $2, 'UTC') + INTERVAL '86400 seconds' AS ends_at
), spent AS (
    SELECT coalesce(sum(extract(epoch FROM
               least(coalesce(r.end_time, $2), $2, b.ends_at)
               - greatest(r.start_time, b.begins_at))), 0) AS seconds
      FROM colab.runtime_session AS r
      JOIN colab.project AS p ON p.id = r.project_id
      CROSS JOIN bounds AS b
     WHERE p.user_id = $1 AND r.start_time < least($2, b.ends_at)
       AND coalesce(r.end_time, $2) > b.begins_at
), active_tariff AS (
    SELECT t.id, t.name, t.runtime_limit, t.ram_limit, t.cpu_limit
      FROM colab.user_subscription AS s
      JOIN colab.subscription_type AS t ON t.id = s.subscription_type_id
     WHERE s.user_id = $1 AND s.start_time <= $2 AND $2 < s.end_time
    UNION ALL
    SELECT t.id, t.name, t.runtime_limit, t.ram_limit, t.cpu_limit
      FROM colab.subscription_type AS t
     WHERE t.price_rub = 0 AND NOT EXISTS (
         SELECT 1 FROM colab.user_subscription AS s
          WHERE s.user_id = $1 AND s.start_time <= $2 AND $2 < s.end_time)
)
SELECT t.id AS tariff_id, t.name, t.runtime_limit, t.ram_limit, t.cpu_limit,
       s.seconds AS spent_seconds, greatest(0, t.runtime_limit - s.seconds) AS remaining_seconds,
       s.seconds < t.runtime_limit AS can_start
  FROM active_tariff AS t CROSS JOIN spent AS s
 WHERE EXISTS (SELECT 1 FROM colab.app_user WHERE id = $1);

-- start_runtime(owner_id integer, project_id integer, actual_start timestamptz) -> id, start_time.
-- Только в транзакции после lock_user и runtime_budget(can_start=true).
-- Worker готов исполнять; очередь и подготовка ресурсов не входят в интервал.
PREPARE start_runtime(integer, integer, timestamptz) AS
INSERT INTO colab.runtime_session (project_id, start_time)
SELECT id, $3 FROM colab.project WHERE id = $2 AND user_id = $1
RETURNING id, start_time;

-- finish_runtime(owner_id integer, runtime_id integer, confirmed_stop timestamptz) -> id, end_time.
-- Для общего порядка блокировок завершение также предваряется lock_user.
PREPARE finish_runtime(integer, integer, timestamptz) AS
UPDATE colab.runtime_session AS r SET end_time = $3
  FROM colab.project AS p
 WHERE r.id = $2 AND r.project_id = p.id AND p.user_id = $1 AND r.end_time IS NULL
RETURNING r.id, r.end_time;

-- runtime_history(actor_id integer, project_id integer) -> запуски доступного проекта.
PREPARE runtime_history(integer, integer) AS
SELECT r.id, r.start_time, r.end_time
  FROM colab.runtime_session AS r JOIN colab.project AS p ON p.id = r.project_id
 WHERE p.id = $2 AND (p.user_id = $1 OR EXISTS (
       SELECT 1 FROM colab.project_share AS s WHERE s.project_id = p.id AND s.user_id = $1))
 ORDER BY r.start_time DESC, r.id DESC;
