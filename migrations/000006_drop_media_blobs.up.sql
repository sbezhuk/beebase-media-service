-- media_blobs was the pre-object-storage table for file content. Since
-- media content was moved to object storage (BEEB-32) and all blobs
-- have been migrated, media_blobs is no longer used.
DROP TABLE IF EXISTS media_blobs;
