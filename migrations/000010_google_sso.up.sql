-- Add Google SSO support to the users table.
-- google_id is nullable (existing users signed up via GitHub won't have one).
-- A unique index ensures no two users can claim the same Google account.
-- We also add a name column for display (Google provides full name, GitHub provides login).

ALTER TABLE users ADD COLUMN IF NOT EXISTS google_id TEXT;
ALTER TABLE users ADD COLUMN IF NOT EXISTS name TEXT;

CREATE UNIQUE INDEX IF NOT EXISTS users_google_id_key ON users (google_id) WHERE google_id IS NOT NULL;

-- Allow github_id to be NULL for users who sign up exclusively via Google.
ALTER TABLE users ALTER COLUMN github_id DROP NOT NULL;

-- Drop the existing unique constraint on github_id and replace with a partial unique index
-- so NULL values don't conflict.
ALTER TABLE users DROP CONSTRAINT IF EXISTS users_github_id_key;
CREATE UNIQUE INDEX IF NOT EXISTS users_github_id_key ON users (github_id) WHERE github_id IS NOT NULL;
