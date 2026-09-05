CREATE TABLE media_blobs (
    media_id   UUID PRIMARY KEY REFERENCES media(id) ON DELETE CASCADE,
    data       BYTEA NOT NULL,
    compressed BOOLEAN NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
