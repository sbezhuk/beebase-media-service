-- Attach (POST /media/{id}/attach) and the owner_type/owner_id it set are
-- gone: apiary-service/hive-service are now the sole source of truth for
-- "which media ids belong to me" (their own local images columns),
-- verified by asking media-service "does this id belong to the caller"
-- (GET /media?ids=) rather than media-service tracking a mutable,
-- exclusive owner itself. media-service no longer needs to know apiaries
-- or hives exist at all.
DROP INDEX idx_media_owner;

ALTER TABLE media
    DROP CONSTRAINT media_owner_type_check,
    DROP CONSTRAINT media_owner_pair_check,
    DROP COLUMN owner_type,
    DROP COLUMN owner_id;
