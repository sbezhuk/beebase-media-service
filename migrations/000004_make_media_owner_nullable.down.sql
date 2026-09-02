-- Fails if any row currently has a NULL owner_type/owner_id (i.e. any
-- media uploaded but never attached) - by design, since there's no
-- sensible owner to backfill.
ALTER TABLE media DROP CONSTRAINT media_owner_pair_check;
ALTER TABLE media DROP CONSTRAINT media_owner_type_check;
ALTER TABLE media ADD CONSTRAINT media_owner_type_check
    CHECK (owner_type IN ('APIARY', 'HIVE'));

ALTER TABLE media
    ALTER COLUMN owner_type SET NOT NULL,
    ALTER COLUMN owner_id SET NOT NULL;
