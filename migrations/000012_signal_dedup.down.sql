DROP INDEX IF EXISTS signals_duplicate_of_idx;
DROP INDEX IF EXISTS signals_project_content_hash_idx;

ALTER TABLE signals
    DROP COLUMN IF EXISTS duplicate_of_id,
    DROP COLUMN IF EXISTS content_hash;
