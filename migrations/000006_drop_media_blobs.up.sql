-- media_blobs was the pre-R2 storage table for file content. Since media
-- content was moved to Cloudflare R2 (BEEB-32) and all blobs have been migrated,
-- media_blobs is no longer used.
DROP TABLE IF EXISTS media_blobs;
