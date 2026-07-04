-- 008_refresh_detail_constraints.sql
-- Relax sync_task_defs CHECK constraints to allow the refresh_detail task type.
--
-- source:   + 'refresh_detail'  (new source for backfilling old detail fields)
-- strategy: + 'refresh'         (new strategy — not full scan, not incremental)
-- task_kind: + 'refresh_detail' (new task kind for river job routing)

ALTER TABLE sync_task_defs
    DROP CONSTRAINT IF EXISTS sync_task_defs_source_check,
    DROP CONSTRAINT IF EXISTS sync_task_defs_strategy_check,
    DROP CONSTRAINT IF EXISTS sync_task_defs_task_kind_check;

ALTER TABLE sync_task_defs
    ADD CONSTRAINT sync_task_defs_source_check
        CHECK (source = ANY (ARRAY['gallery_list'::text, 'favorites'::text, 'refresh_detail'::text])),
    ADD CONSTRAINT sync_task_defs_strategy_check
        CHECK (strategy = ANY (ARRAY['full'::text, 'incremental'::text, 'refresh'::text])),
    ADD CONSTRAINT sync_task_defs_task_kind_check
        CHECK (task_kind = ANY (ARRAY['gallery_sync'::text, 'favorites_sync'::text, 'refresh_detail'::text]));
