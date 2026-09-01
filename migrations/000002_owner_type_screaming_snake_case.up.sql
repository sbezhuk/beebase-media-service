-- owner_type now serializes as SCREAMING_SNAKE_CASE ('APIARY'/'HIVE') to
-- match the mobile client's MediaOwnerType enum (Dart's
-- @JsonEnum(fieldRename: FieldRename.screamingSnake)). Drop the old CHECK
-- constraint dynamically (rather than hardcoding its auto-generated name,
-- which could vary) so this migration is robust regardless of how
-- Postgres named it when 000001 created the table inline.
DO $$
DECLARE
    check_name text;
BEGIN
    SELECT con.conname INTO check_name
    FROM pg_constraint con
    JOIN pg_class rel ON rel.oid = con.conrelid
    JOIN pg_attribute att ON att.attrelid = con.conrelid AND att.attnum = ANY(con.conkey)
    WHERE rel.relname = 'media'
      AND con.contype = 'c'
      AND att.attname = 'owner_type';

    IF check_name IS NOT NULL THEN
        EXECUTE format('ALTER TABLE media DROP CONSTRAINT %I', check_name);
    END IF;
END $$;

UPDATE media SET owner_type = UPPER(owner_type);

ALTER TABLE media ADD CONSTRAINT media_owner_type_check CHECK (owner_type IN ('APIARY', 'HIVE'));
