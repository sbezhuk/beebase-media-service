-- Reverses 000002_owner_type_screaming_snake_case.up.sql: restores the
-- original lowercase owner_type values and the original CHECK constraint.
ALTER TABLE media DROP CONSTRAINT IF EXISTS media_owner_type_check;

UPDATE media SET owner_type = LOWER(owner_type);

ALTER TABLE media ADD CONSTRAINT media_owner_type_check CHECK (owner_type IN ('apiary', 'hive'));
