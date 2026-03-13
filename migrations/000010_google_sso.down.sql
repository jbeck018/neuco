DROP INDEX IF EXISTS users_github_id_key;
ALTER TABLE users ADD CONSTRAINT users_github_id_key UNIQUE (github_id);
ALTER TABLE users ALTER COLUMN github_id SET NOT NULL;
DROP INDEX IF EXISTS users_google_id_key;
ALTER TABLE users DROP COLUMN IF EXISTS name;
ALTER TABLE users DROP COLUMN IF EXISTS google_id;
