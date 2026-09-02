-- Media upload is decoupled from any owning apiary/hive: owner_type and
-- owner_id are now set later, by Attach, rather than required at upload
-- time. NULL means "uploaded but not yet attached to anything".
ALTER TABLE media
    ALTER COLUMN owner_type DROP NOT NULL,
    ALTER COLUMN owner_id DROP NOT NULL;

ALTER TABLE media DROP CONSTRAINT media_owner_type_check;
ALTER TABLE media ADD CONSTRAINT media_owner_type_check
    CHECK (owner_type IS NULL OR owner_type IN ('APIARY', 'HIVE'));

-- owner_type and owner_id are set together or not at all.
ALTER TABLE media ADD CONSTRAINT media_owner_pair_check
    CHECK ((owner_type IS NULL) = (owner_id IS NULL));
