CREATE TABLE media (
    id                UUID PRIMARY KEY,
    -- Denormalized owner, verified once against apiary-service or
    -- hive-service (whichever owner_type applies) at upload time - never
    -- re-checked cross-service after that, same pattern hive-service uses
    -- for apiary ownership.
    user_id           UUID NOT NULL,
    -- No foreign key: apiaries/hives live in different services and
    -- databases. owner_id is opaque here; ownership was confirmed against
    -- the relevant service once, at upload time. Generic owner_type +
    -- owner_id (rather than a dedicated ApiaryMedia/HiveMedia table)
    -- keeps this infrastructure reusable for future entity types.
    owner_type        TEXT NOT NULL CHECK (owner_type IN ('apiary', 'hive')),
    owner_id          UUID NOT NULL,
    original_filename TEXT NOT NULL,
    content_type      TEXT NOT NULL,
    size_bytes        BIGINT NOT NULL CHECK (size_bytes >= 0),
    -- Reserved for a future async processing state (e.g. virus-scan
    -- quarantine). Every row created today is 'available'; rows are never
    -- flipped to a 'deleted' status - deleted_at is the single source of
    -- truth for existence, exactly like every other table in this project.
    status            TEXT NOT NULL DEFAULT 'available' CHECK (status IN ('available')),
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at        TIMESTAMPTZ
);

-- Serves a possible future "list all my media" endpoint.
CREATE INDEX idx_media_user_id_created_at ON media (user_id, created_at, id) WHERE deleted_at IS NULL;

-- Serves ListByOwner: GET /api/v1/media?owner_type=&owner_id=, paginated
-- and ordered by (created_at, id), exactly like hive-service's ListByUser.
CREATE INDEX idx_media_owner ON media (owner_type, owner_id, created_at, id) WHERE deleted_at IS NULL;

-- File bytes, kept in a separate table so media list/get queries never
-- touch blob storage. MVP storage choice: bytes live directly in
-- PostgreSQL (no S3/MinIO dependency), gzip-compressed by the repository
-- layer when that actually shrinks the payload; `compressed` records
-- whether `data` needs gunzipping on read. See the repository package
-- and the README's Storage section for the rationale and the future
-- migration path to object storage if usage outgrows this.
CREATE TABLE media_blobs (
    media_id   UUID PRIMARY KEY REFERENCES media(id) ON DELETE CASCADE,
    data       BYTEA NOT NULL,
    compressed BOOLEAN NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
