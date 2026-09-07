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
// media.BlobStore (Amazon S3 in production, see internal/platform/blobstore)
// - internal/repository/media.Repository composes the two into the full
// domain/media.Repository port, so nothing above that composite knows
// metadata and content are stored in different places.
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

// Delete hard-deletes the media row belonging to userID.
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
// their stored content too.
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

// DeleteAllByUser hard-deletes every row belonging to userID in one
// statement, returning the ids actually deleted so the caller can clean up
// their stored content too.
func (r *MediaRepository) DeleteAllByUser(ctx context.Context, userID uuid.UUID) ([]uuid.UUID, error) {
	const q = `DELETE FROM media WHERE user_id = $1 RETURNING id`

	rows, err := r.db.Query(ctx, q, userID)
	if err != nil {
		return nil, fmt.Errorf("postgres: delete all media by user: %w", err)
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
		return nil, fmt.Errorf("postgres: delete all media by user: %w", err)
	}

	return deleted, nil
}
