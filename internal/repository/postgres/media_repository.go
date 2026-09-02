package postgres

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/sbezhuk/beebase-common/pagination"
	"github.com/sbezhuk/beebase-media-service/internal/domain/media"
)

// pgUniqueViolation is the PostgreSQL SQLSTATE code for a unique
// constraint violation (23505), used to detect an ID collision on Create.
const pgUniqueViolation = "23505"

// MediaRepository implements domain/media.Repository against PostgreSQL.
// Every method scopes its query by user_id, so a user can never read or
// write media they don't own: there's no separate ownership-check step to
// forget.
//
// File content is stored directly in PostgreSQL (a media_blobs table,
// kept separate from media's own metadata columns so list/get queries
// never touch blob data), gzip-compressed when that actually shrinks the
// payload. This is an MVP choice to avoid a paid object-storage
// dependency; nothing outside this file knows bytes are stored this way,
// so swapping in an S3-backed implementation later is a contained change.
type MediaRepository struct {
	db Beginner
}

// NewMediaRepository returns a MediaRepository backed by db.
func NewMediaRepository(db Beginner) *MediaRepository {
	return &MediaRepository{db: db}
}

// Create persists m and content together in one transaction, so a media
// row can never exist without its content (or vice versa).
func (r *MediaRepository) Create(ctx context.Context, m *media.Media, content []byte) error {
	data, compressed := compress(content)

	tx, err := r.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("postgres: begin create media tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	const insertMedia = `
		INSERT INTO media (id, user_id, owner_type, owner_id, original_filename, content_type, size_bytes, status, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`
	_, err = tx.Exec(ctx, insertMedia,
		m.ID, m.UserID, m.OwnerType, m.OwnerID, m.OriginalFilename, m.ContentType, m.SizeBytes, m.Status, m.CreatedAt, m.UpdatedAt,
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
			return media.ErrIDConflict
		}
		return fmt.Errorf("postgres: create media: %w", err)
	}

	const insertBlob = `
		INSERT INTO media_blobs (media_id, data, compressed)
		VALUES ($1, $2, $3)
	`
	if _, err := tx.Exec(ctx, insertBlob, m.ID, data, compressed); err != nil {
		return fmt.Errorf("postgres: create media blob: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("postgres: commit create media tx: %w", err)
	}

	return nil
}

// Attach implements domain/media.Repository.
func (r *MediaRepository) Attach(ctx context.Context, userID, mediaID uuid.UUID, ownerType string, ownerID uuid.UUID) (*media.Media, error) {
	const q = `
		UPDATE media
		SET owner_type = $1, owner_id = $2, updated_at = now()
		WHERE id = $3 AND user_id = $4 AND deleted_at IS NULL AND owner_type IS NULL
	`

	tag, err := r.db.Exec(ctx, q, ownerType, ownerID, mediaID, userID)
	if err != nil {
		return nil, fmt.Errorf("postgres: attach media: %w", err)
	}
	if tag.RowsAffected() == 1 {
		return r.GetByID(ctx, userID, mediaID)
	}

	// Nothing was updated: mediaID might not exist or not belong to
	// userID (GetByID reports ErrNotFound for both, indistinguishably),
	// or it might already be attached - either to this same owner
	// already (idempotent success) or to a different one
	// (ErrAlreadyAttached).
	m, err := r.GetByID(ctx, userID, mediaID)
	if err != nil {
		return nil, err
	}
	if m.OwnerType != nil && *m.OwnerType == ownerType && m.OwnerID != nil && *m.OwnerID == ownerID {
		return m, nil
	}
	return nil, media.ErrAlreadyAttached
}

func (r *MediaRepository) GetByID(ctx context.Context, userID, mediaID uuid.UUID) (*media.Media, error) {
	const q = `
		SELECT id, user_id, owner_type, owner_id, original_filename, content_type, size_bytes, status, created_at, updated_at, deleted_at
		FROM media
		WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL
	`

	var m media.Media
	err := r.db.QueryRow(ctx, q, mediaID, userID).Scan(
		&m.ID, &m.UserID, &m.OwnerType, &m.OwnerID, &m.OriginalFilename, &m.ContentType, &m.SizeBytes, &m.Status, &m.CreatedAt, &m.UpdatedAt, &m.DeletedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, media.ErrNotFound
		}
		return nil, fmt.Errorf("postgres: get media: %w", err)
	}

	return &m, nil
}

func (r *MediaRepository) GetContent(ctx context.Context, userID, mediaID uuid.UUID) (*media.Media, []byte, error) {
	m, err := r.GetByID(ctx, userID, mediaID)
	if err != nil {
		return nil, nil, err
	}

	const q = `SELECT data, compressed FROM media_blobs WHERE media_id = $1`

	var data []byte
	var compressed bool
	if err := r.db.QueryRow(ctx, q, mediaID).Scan(&data, &compressed); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// The metadata row exists but its blob is missing. This
			// should be unreachable given Create/Delete keep the two in
			// one transaction; surfaced as a hard error rather than a 404
			// so it doesn't look like a client mistake.
			return nil, nil, fmt.Errorf("postgres: media %s has no stored content", mediaID)
		}
		return nil, nil, fmt.Errorf("postgres: get media content: %w", err)
	}

	content, err := decompress(data, compressed)
	if err != nil {
		return nil, nil, err
	}

	return m, content, nil
}

func (r *MediaRepository) ListByOwner(ctx context.Context, userID uuid.UUID, ownerType string, ownerID uuid.UUID, p pagination.Params) ([]*media.Media, int, error) {
	const countQ = `
		SELECT count(*)
		FROM media
		WHERE user_id = $1 AND owner_type = $2 AND owner_id = $3 AND deleted_at IS NULL
	`

	var total int
	if err := r.db.QueryRow(ctx, countQ, userID, ownerType, ownerID).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("postgres: count media: %w", err)
	}

	const q = `
		SELECT id, user_id, owner_type, owner_id, original_filename, content_type, size_bytes, status, created_at, updated_at, deleted_at
		FROM media
		WHERE user_id = $1 AND owner_type = $2 AND owner_id = $3 AND deleted_at IS NULL
		ORDER BY created_at ASC, id ASC
		LIMIT $4 OFFSET $5
	`

	rows, err := r.db.Query(ctx, q, userID, ownerType, ownerID, p.Limit, p.Offset())
	if err != nil {
		return nil, 0, fmt.Errorf("postgres: list media: %w", err)
	}
	defer rows.Close()

	items := []*media.Media{}
	for rows.Next() {
		var m media.Media
		if err := rows.Scan(&m.ID, &m.UserID, &m.OwnerType, &m.OwnerID, &m.OriginalFilename, &m.ContentType, &m.SizeBytes, &m.Status, &m.CreatedAt, &m.UpdatedAt, &m.DeletedAt); err != nil {
			return nil, 0, fmt.Errorf("postgres: scan media: %w", err)
		}
		items = append(items, &m)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("postgres: list media: %w", err)
	}

	return items, total, nil
}

// Delete soft-deletes the media row and hard-deletes its blob in one
// transaction, reclaiming storage immediately - unlike a best-effort
// cleanup against a separate object store, both live in the same
// database, so there's no reason to leave the delete partial.
func (r *MediaRepository) Delete(ctx context.Context, userID, mediaID uuid.UUID) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("postgres: begin delete media tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	const softDelete = `
		UPDATE media
		SET deleted_at = now()
		WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL
	`
	tag, err := tx.Exec(ctx, softDelete, mediaID, userID)
	if err != nil {
		return fmt.Errorf("postgres: soft delete media: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return media.ErrNotFound
	}

	const dropBlob = `DELETE FROM media_blobs WHERE media_id = $1`
	if _, err := tx.Exec(ctx, dropBlob, mediaID); err != nil {
		return fmt.Errorf("postgres: delete media blob: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("postgres: commit delete media tx: %w", err)
	}

	return nil
}

// DeleteByOwner hard-deletes every media row for ownerType/ownerID/userID
// in one statement. No transaction is needed here (unlike Delete): the
// media_blobs FK is ON DELETE CASCADE, so each row's blob is removed
// automatically as part of the same DELETE.
func (r *MediaRepository) DeleteByOwner(ctx context.Context, userID uuid.UUID, ownerType string, ownerID uuid.UUID) (int64, error) {
	const q = `DELETE FROM media WHERE owner_type = $1 AND owner_id = $2 AND user_id = $3`

	tag, err := r.db.Exec(ctx, q, ownerType, ownerID, userID)
	if err != nil {
		return 0, fmt.Errorf("postgres: delete media by owner: %w", err)
	}

	return tag.RowsAffected(), nil
}

// compress gzip-compresses content, returning the compressed bytes and
// true only if compression actually made it smaller. Already-compressed
// formats like JPEG or PDF often don't shrink further; storing the raw
// bytes in that case avoids paying gzip's framing overhead for nothing.
func compress(content []byte) ([]byte, bool) {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	if _, err := gw.Write(content); err != nil {
		return content, false
	}
	if err := gw.Close(); err != nil {
		return content, false
	}

	if buf.Len() >= len(content) {
		return content, false
	}
	return buf.Bytes(), true
}

func decompress(data []byte, compressed bool) ([]byte, error) {
	if !compressed {
		return data, nil
	}

	gr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("postgres: gunzip media content: %w", err)
	}
	defer func() { _ = gr.Close() }()

	out, err := io.ReadAll(gr)
	if err != nil {
		return nil, fmt.Errorf("postgres: gunzip media content: %w", err)
	}

	return out, nil
}
