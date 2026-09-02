ALTER TABLE media
    ADD COLUMN owner_type TEXT,
    ADD COLUMN owner_id UUID;

ALTER TABLE media ADD CONSTRAINT media_owner_type_check
    CHECK (owner_type IS NULL OR owner_type IN ('APIARY', 'HIVE'));
ALTER TABLE media ADD CONSTRAINT media_owner_pair_check
    CHECK ((owner_type IS NULL) = (owner_id IS NULL));

CREATE INDEX idx_media_owner ON media (owner_type, owner_id, created_at, id) WHERE deleted_at IS NULL;
