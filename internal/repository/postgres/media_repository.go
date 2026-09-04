package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/sbezhuk/beebase-media-service/internal/domain/media"
)

// pgUniqueViolation is the PostgreSQL SQLSTATE code for a unique
// constraint violation (23505), used to detect an ID collision on Create.
const pgUniqueViolation = "23505"

// MediaRepository implements the metadata half of domain/media.Repository
// against PostgreSQL: the media table only. File content lives in a
// media.BlobStore (Cloudflare R2 in production, see internal/platform/r2)
// - internal/repository/media.Repository composes the two into the full
// domain/media.Repository port, so nothing above that composite knows
// metadata and content are stored in different places.
//
// media_blobs, the pre-R2 table that used to hold file content directly
// in PostgreSQL, still exists purely as the migration's bookkeeping: a
// remaining row there means that media id's content hasn't been moved to
// R2 yet (see GetLegacyContent, and LegacyBlobStore in
// legacy_blob_store.go). Every method here otherwise ignores it.
type MediaRepository struct {
	db Querier
}

// NewMediaRepository returns a MediaRepository backed by db.
func NewMediaRepository(db Querier) *MediaRepository {
	return &MediaRepository{db: db}
}

// Create persists m's metadata. If m.ID already belongs to an existing
// row, it returns media.ErrIDConflict.
func (r *MediaRepository) Create(ctx context.Context, m *media.Media) error {
	const q = `
		INSERT INTO media (id, user_id, original_filename, content_type, size_bytes, status, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`
	_, err := r.db.Exec(ctx, q,
		m.ID, m.UserID, m.OriginalFilename, m.ContentType, m.SizeBytes, m.Status, m.CreatedAt, m.UpdatedAt,
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
			return media.ErrIDConflict
		}
		return fmt.Errorf("postgres: create media: %w", err)
	}

	return nil
}

func (r *MediaRepository) GetByID(ctx context.Context, userID, mediaID uuid.UUID) (*media.Media, error) {
	const q = `
		SELECT id, user_id, original_filename, content_type, size_bytes, status, created_at, updated_at, deleted_at
		FROM media
		WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL
	`

	var m media.Media
	err := r.db.QueryRow(ctx, q, mediaID, userID).Scan(
		&m.ID, &m.UserID, &m.OriginalFilename, &m.ContentType, &m.SizeBytes, &m.Status, &m.CreatedAt, &m.UpdatedAt, &m.DeletedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, media.ErrNotFound
		}
		return nil, fmt.Errorf("postgres: get media: %w", err)
	}

	return &m, nil
}

// ListByIDs implements domain/media.Repository with a single query against
// every id in ids at once, scoped to userID exactly like every other method
// here.
func (r *MediaRepository) ListByIDs(ctx context.Context, userID uuid.UUID, ids []uuid.UUID) ([]*media.Media, error) {
	if len(ids) == 0 {
		return []*media.Media{}, nil
	}

	const q = `
		SELECT id, user_id, original_filename, content_type, size_bytes, status, created_at, updated_at, deleted_at
		FROM media
		WHERE user_id = $1 AND id = ANY($2) AND deleted_at IS NULL
	`

	rows, err := r.db.Query(ctx, q, userID, ids)
	if err != nil {
		return nil, fmt.Errorf("postgres: list media by ids: %w", err)
	}
	defer rows.Close()

	items := []*media.Media{}
	for rows.Next() {
		var m media.Media
		if err := rows.Scan(&m.ID, &m.UserID, &m.OriginalFilename, &m.ContentType, &m.SizeBytes, &m.Status, &m.CreatedAt, &m.UpdatedAt, &m.DeletedAt); err != nil {
			return nil, fmt.Errorf("postgres: scan media: %w", err)
		}
		items = append(items, &m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list media by ids: %w", err)
	}

	return items, nil
}

// Delete hard-deletes the media row belonging to userID. Its media_blobs
// row, if any (only present for media the R2 migration hasn't reached
// yet), cascades away via the table's ON DELETE CASCADE FK as part of the
// same statement.
func (r *MediaRepository) Delete(ctx context.Context, userID, mediaID uuid.UUID) error {
	const q = `DELETE FROM media WHERE id = $1 AND user_id = $2`

	tag, err := r.db.Exec(ctx, q, mediaID, userID)
	if err != nil {
		return fmt.Errorf("postgres: delete media: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return media.ErrNotFound
	}

	return nil
}

// DeleteByIDs hard-deletes every row in ids belonging to userID in one
// statement, returning the ids actually deleted so the caller can clean up
// their stored content too. media_blobs rows, where still present, cascade
// away via the table's ON DELETE CASCADE FK as part of the same statement.
func (r *MediaRepository) DeleteByIDs(ctx context.Context, userID uuid.UUID, ids []uuid.UUID) ([]uuid.UUID, error) {
	if len(ids) == 0 {
		return nil, nil
	}

	const q = `DELETE FROM media WHERE id = ANY($1) AND user_id = $2 RETURNING id`

	rows, err := r.db.Query(ctx, q, ids, userID)
	if err != nil {
		return nil, fmt.Errorf("postgres: delete media by ids: %w", err)
	}
	defer rows.Close()

	var deleted []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("postgres: scan deleted media id: %w", err)
		}
		deleted = append(deleted, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: delete media by ids: %w", err)
	}

	return deleted, nil
}

// GetLegacyContent returns mediaID's pre-R2, database-stored file content
// (decompressed), or media.ErrBlobNotFound if there's none - either
// because it was never stored that way, or because the background
// migration has already moved it to R2 and removed the row. It's the
// transitional fallback internal/repository/media.Repository.GetContent
// uses while the migration is still in progress; see that type's doc
// comment. Once the migration completes and media_blobs is dropped, this
// method (and the fallback that calls it) can go away.
func (r *MediaRepository) GetLegacyContent(ctx context.Context, mediaID uuid.UUID) ([]byte, error) {
	const q = `SELECT data, compressed FROM media_blobs WHERE media_id = $1`

	var data []byte
	var compressed bool
	if err := r.db.QueryRow(ctx, q, mediaID).Scan(&data, &compressed); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, media.ErrBlobNotFound
		}
		return nil, fmt.Errorf("postgres: get legacy media content: %w", err)
	}

	content, err := decompress(data, compressed)
	if err != nil {
		return nil, err
	}

	return content, nil
}
