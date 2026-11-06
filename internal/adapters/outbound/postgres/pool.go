// Package postgres provides pgxpool-backed implementations of the outbound
// ports (the appointment and calendar repositories, the local-copy views, the outbox EventPublisher
// and the UnitOfWork) plus a golang-migrate runner over the SQL migrations
// EMBEDDED in the top-level migrations package, so the binary needs no
// migrations directory on disk.
package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// MaxConns is this adapter's per-process connection ceiling.
const MaxConns = 10

// StatementTimeout bounds how long a single query may hold a connection:
// every statement here is a single-row read/write, one owner lookup or one
// bounded list.
const StatementTimeout = "5s"

// NewPool opens a connection pool against databaseURL with MaxConns and
// StatementTimeout applied to every connection.
func NewPool(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, err
	}

	config.MaxConns = MaxConns
	config.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, "SET statement_timeout = '"+StatementTimeout+"'")
		return err
	}

	return pgxpool.NewWithConfig(ctx, config)
}
