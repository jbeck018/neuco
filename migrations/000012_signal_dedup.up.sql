-- Add content hash and duplicate tracking to signals table.
-- content_hash is a SHA-256 hex digest of normalized content used for exact dedup.
-- duplicate_of_id links a signal to the original it duplicates.

ALTER TABLE signals
    ADD COLUMN content_hash    TEXT,
    ADD COLUMN duplicate_of_id UUID REFERENCES signals(id) ON DELETE SET NULL;

-- Unique constraint per project: only one signal with a given content hash.
-- Partial index excludes signals that are already marked as duplicates.
CREATE UNIQUE INDEX signals_project_content_hash_idx
    ON signals (project_id, content_hash)
    WHERE content_hash IS NOT NULL AND duplicate_of_id IS NULL;

-- Index for finding duplicates of a given signal.
CREATE INDEX signals_duplicate_of_idx
    ON signals (duplicate_of_id)
    WHERE duplicate_of_id IS NOT NULL;

-- Backfill content_hash for existing signals.
UPDATE signals
SET content_hash = encode(sha256(convert_to(lower(trim(content)), 'UTF8')), 'hex')
WHERE content_hash IS NULL;
