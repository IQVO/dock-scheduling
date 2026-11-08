package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ProcessedEventRepo is the pgx-backed ports.ProcessedEvents: the inbound
// consumers' dedupe guard over the processed_events table (migration 0001).
type ProcessedEventRepo struct {
	pool *pgxpool.Pool
}

// NewProcessedEventRepo constructs a ProcessedEventRepo over pool.
func NewProcessedEventRepo(pool *pgxpool.Pool) *ProcessedEventRepo {
	return &ProcessedEventRepo{pool: pool}
}

// Claim records eventID as processed, returning false when an earlier call
// already recorded it. Called inside a ports.UnitOfWork (the consumers'
// case) the INSERT joins the SAME transaction as the fact's effects, so a
// rollback un-claims it and a redelivery is processed, not skipped. The
// table is keyed by the event id alone (migration 0001): every consumed type
// funnels into the same open-or-touch / correlation effect, so one guard
// suffices; a concurrent claim of the same id blocks on the primary key
// until the other transaction ends.
func (r *ProcessedEventRepo) Claim(ctx context.Context, eventID string) (bool, error) {
	tag, err := queryFor(ctx, r.pool).Exec(ctx, `
		INSERT INTO processed_events (event_id)
		VALUES ($1)
		ON CONFLICT (event_id) DO NOTHING
	`, eventID)
	if err != nil {
		return false, fmt.Errorf("claim processed event: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}
