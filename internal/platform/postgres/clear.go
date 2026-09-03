package postgres

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ClearAllData truncates every application table in the public schema,
// resetting identity sequences and cascading across any foreign keys
// between them. It leaves the schema itself (tables, indexes, constraints)
// and golang-migrate's schema_migrations bookkeeping table untouched. It
// returns the names of the tables it cleared, or nil if there were none.
func ClearAllData(ctx context.Context, pool *pgxpool.Pool) ([]string, error) {
	rows, err := pool.Query(ctx, `
		SELECT tablename
		FROM pg_tables
		WHERE schemaname = 'public' AND tablename <> 'schema_migrations'
		ORDER BY tablename
	`)
	if err != nil {
		return nil, fmt.Errorf("list tables: %w", err)
	}

	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan table name: %w", err)
		}
		tables = append(tables, name)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list tables: %w", err)
	}
	if len(tables) == 0 {
		return nil, nil
	}

	quoted := make([]string, len(tables))
	for i, t := range tables {
		quoted[i] = pgx.Identifier{t}.Sanitize()
	}

	stmt := fmt.Sprintf("TRUNCATE TABLE %s RESTART IDENTITY CASCADE", strings.Join(quoted, ", "))
	if _, err := pool.Exec(ctx, stmt); err != nil {
		return nil, fmt.Errorf("truncate tables: %w", err)
	}

	return tables, nil
}
