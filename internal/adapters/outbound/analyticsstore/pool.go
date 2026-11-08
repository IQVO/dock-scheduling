// Package analyticsstore holds the Postgres adapters of the analytics read side
// (ADR 0006) over the SEPARATE analytical database: Projection (the writer used
// by cmd/projector) and Reader (the read-only report queries used by
// cmd/reports). No OLTP package may import this one (arch test).
package analyticsstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres" // the postgres:// driver
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/claudioed/dock-scheduling/analytics"
)

// EnvDatabaseURL names the analytical database. It is a DIRECT DSN (the
// migrations take golang-migrate's advisory lock, which does not survive
// PgBouncer transaction pooling) and it is never the OLTP DATABASE_URL.
const EnvDatabaseURL = "ANALYTICS_DATABASE_URL"

const (
	// MaxConns is the projector's connection ceiling: one consumer loop issuing
	// single-event upserts needs very few.
	MaxConns = 5
	// ReportsMaxConns is the reports process's ceiling (stateless reads,
	// horizontally scalable by the chart).
	ReportsMaxConns = 5
	// StatementTimeout bounds one projector statement.
	StatementTimeout = "10s"
	// ReportsStatementTimeout bounds one report query: an aggregate over a
	// caller-chosen range (at most 92 days) gets more headroom than a
	// single-row upsert, but never runs unbounded.
	ReportsStatementTimeout = "15s"
)

// RunMigrations applies every pending embedded analytical migration
// (analytics/migrations) against databaseURL. Running it again is a no-op.
func RunMigrations(databaseURL string) error {
	m, err := Migrator(databaseURL)
	if err != nil {
		return err
	}
	defer func() { _, _ = m.Close() }()
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return err
	}
	return nil
}

// Migrator opens a golang-migrate runner over the embedded analytical
// migrations. The caller closes it.
func Migrator(databaseURL string) (*migrate.Migrate, error) {
	src, err := iofs.New(analytics.Migrations, "migrations")
	if err != nil {
		return nil, fmt.Errorf("open embedded analytics migrations: %w", err)
	}
	return migrate.NewWithSourceInstance("iofs", src, databaseURL)
}

// NewPool opens the WRITER pool over the analytical database.
func NewPool(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	return newPool(ctx, databaseURL, MaxConns, StatementTimeout, false)
}

// NewReadOnlyPool opens the READER pool: every connection defaults to read-only
// transactions, so a bug in cmd/reports cannot mutate the model even if its
// database role could (defence in depth on top of the role).
func NewReadOnlyPool(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	return newPool(ctx, databaseURL, ReportsMaxConns, ReportsStatementTimeout, true)
}

func newPool(ctx context.Context, databaseURL string, maxConns int32, statementTimeout string, readOnly bool) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, err
	}
	cfg.MaxConns = maxConns
	if readOnly {
		if cfg.ConnConfig.RuntimeParams == nil {
			cfg.ConnConfig.RuntimeParams = map[string]string{}
		}
		cfg.ConnConfig.RuntimeParams["default_transaction_read_only"] = "on"
	}
	cfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, fmt.Sprintf("SET statement_timeout = '%s'", statementTimeout))
		return err
	}
	return pgxpool.NewWithConfig(ctx, cfg)
}
