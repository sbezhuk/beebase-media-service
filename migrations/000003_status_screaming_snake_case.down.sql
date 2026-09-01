-- Reverses 000003_status_screaming_snake_case.up.sql: restores the
-- original lowercase status value, default, and CHECK constraint.
ALTER TABLE media DROP CONSTRAINT IF EXISTS media_status_check;

UPDATE media SET status = LOWER(status);

ALTER TABLE media ALTER COLUMN status SET DEFAULT 'available';
ALTER TABLE media ADD CONSTRAINT media_status_check CHECK (status IN ('available'));
