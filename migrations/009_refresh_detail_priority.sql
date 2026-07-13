-- Support refresh_detail keyset pagination over the active pending set.
-- The old gid-only partial index could filter pending rows but still required
-- a sort for every fav_count-prioritized batch.

CREATE INDEX IF NOT EXISTS idx_eh_galleries_refresh_priority
    ON eh_galleries (fav_count DESC, gid DESC)
    WHERE file_size IS NULL AND is_active = TRUE;
