-- psql: owner_id, project_id - ID. Worker готов к фактическому старту.
\set ON_ERROR_STOP on
BEGIN ISOLATION LEVEL READ COMMITTED;
EXECUTE lock_user(:'owner_id'::integer) \gset locked_
SELECT clock_timestamp() AS actual_start \gset
EXECUTE runtime_budget(:'owner_id'::integer, :'actual_start'::timestamptz) \gset budget_
\if :budget_can_start
    EXECUTE start_runtime(:'owner_id'::integer, :'project_id'::integer, :'actual_start'::timestamptz) \gset runtime_
    COMMIT;
    \echo 'runtime started:' :runtime_id
\else
    ROLLBACK;
    \echo 'daily quota exhausted'
\endif
