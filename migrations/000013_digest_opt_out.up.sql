-- Add digest opt-out preference to org_members.
-- Default FALSE means all members receive digests (existing behavior).
ALTER TABLE org_members ADD COLUMN digest_opt_out BOOLEAN NOT NULL DEFAULT FALSE;
