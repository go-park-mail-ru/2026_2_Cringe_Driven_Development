-- psql: owner_id, tariff_id - ID; promo_id - ID или пустая строка.
-- Подготовленные запросы из all.sql уже загружены в ЭТО соединение.
\set ON_ERROR_STOP on
BEGIN ISOLATION LEVEL READ COMMITTED;
EXECUTE lock_user(:'owner_id'::integer) \gset locked_
EXECUTE lock_paid_tariff(:'tariff_id'::integer) \gset tariff_
SELECT clock_timestamp() AS purchased_at \gset
EXECUTE buy_subscription(:'owner_id'::integer, :'tariff_id'::integer,
                         nullif(:'promo_id', '')::integer, :'purchased_at'::timestamptz);
SET CONSTRAINTS ALL IMMEDIATE;
COMMIT;
