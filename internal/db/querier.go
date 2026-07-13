// Package db holds the minimal interface repo layers depend on, instead of
// a concrete *pgxpool.Pool, so tests can pass a pgx.Tx (rolled back after
// each test) in its place. Both types satisfy this without any wrapping —
// their Query/QueryRow/Exec signatures are identical.
package db

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
}
