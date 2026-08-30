// Package postgres implements the domain repository ports against
// PostgreSQL using pgx, with explicit SQL and no ORM.
package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Querier is satisfied by both *pgxpool.Pool and pgx.Tx. Repositories
// depend on this instead of a concrete pool so they can run against a
// plain connection in production or inside a caller-managed transaction
// in tests.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Beginner extends Querier with the ability to start a transaction.
// Satisfied by both *pgxpool.Pool and, via pgx's savepoint-based nested
// transaction support, pgx.Tx itself - so MediaRepository can run its own
// transaction for Create/Delete (to keep a media row and its blob in
// sync) whether it's handed a bare pool in production or an outer
// transaction in an integration test.
type Beginner interface {
	Querier
	Begin(ctx context.Context) (pgx.Tx, error)
}
