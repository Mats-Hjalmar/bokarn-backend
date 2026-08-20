package db

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// TX is the subset of pgx satisfied by both *pgxpool.Pool and pgx.Tx, so a
// store method can run either on the pool directly or inside a caller-owned
// transaction. The use-case layer opens the tx and passes it down; a handler
// doing a single operation passes the pool.
type TX interface {
	Exec(
		ctx context.Context,
		sql string,
		args ...any,
	) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}
