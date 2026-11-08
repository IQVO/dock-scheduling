package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/claudioed/dock-scheduling/internal/application/outbox"
)

// OutboxStore is the relay's read side of the outbox table: Drain is one
// claim-and-send pass. It complements OutboxPublisher (the write side);
// nothing but Drain ever deletes or retries a row.
type OutboxStore struct {
	pool *pgxpool.Pool
}

// NewOutboxStore constructs an OutboxStore over pool.
func NewOutboxStore(pool *pgxpool.Pool) *OutboxStore { return &OutboxStore{pool: pool} }

// Drain claims up to limit due rows (next_attempt_at <= now, and attempts
// below maxAttempts when maxAttempts > 0) in id order under FOR UPDATE
// SKIP LOCKED (two overlapping relays claim disjoint sets), hands each one
// to handle INSIDE the pass's transaction, and stages a DELETE for every
// row whose handle returned nil — the send and the removal commit together,
// so a crash between the broker ack and the commit republishes the row later
// (at-least-once; the persisted CloudEvents id makes the duplicate harmless).
//
// The first handle error stops the pass right there: the failed row keeps its
// place (attempts+1, next_attempt_at pushed one exponential backoff step
// forward — 2^attempts seconds, capped at five minutes), the rows after it
// are left untouched — a later row never overtakes a row still being
// retried — and the pass COMMITS what it already staged, so nothing before
// the failure is lost. The error comes back as *outbox.SendFailureError carrying
// the row's new attempt count.
//
// A row whose payload is not the envelope OutboxPublisher writes does not
// fail the pass: it is handed over as a Row with an empty Fact.Event (and
// the raw payload preserved), which the send path dead-letters.
func (s *OutboxStore) Drain(ctx context.Context, limit, maxAttempts int, handle func(context.Context, outbox.Row) error) (int, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin outbox pass: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	rows, err := s.claim(ctx, tx, limit, maxAttempts)
	if err != nil {
		return 0, err
	}

	published := 0
	for _, row := range rows {
		if err := handle(ctx, row); err != nil {
			return published, s.recordFailure(ctx, tx, row, err)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM outbox WHERE id = $1`, row.ID); err != nil {
			return published, fmt.Errorf("remove outbox row %d: %w", row.ID, err)
		}
		published++
	}
	if err := tx.Commit(ctx); err != nil {
		return published, fmt.Errorf("commit outbox pass: %w", err)
	}
	return published, nil
}

// recordFailure stages the failed row's attempts/next_attempt_at, commits the
// pass (the rows already staged for deletion go with it) and returns the
// failure with the row's new attempt count. The UPDATE and the COMMIT use a
// cancellation-proof context so a shutdown mid-pass still persists both.
func (s *OutboxStore) recordFailure(ctx context.Context, tx pgx.Tx, row outbox.Row, sendErr error) error {
	dctx := context.WithoutCancel(ctx)
	failure := &outbox.SendFailureError{RowID: row.ID, Row: row, Err: sendErr}
	if err := tx.QueryRow(dctx, `
		UPDATE outbox
		SET attempts = attempts + 1,
		    next_attempt_at = now() + make_interval(secs => least(300, power(2, attempts + 1)))
		WHERE id = $1
		RETURNING attempts
	`, row.ID).Scan(&failure.Attempts); err != nil {
		return errors.Join(sendErr, fmt.Errorf("record outbox failure on row %d: %w", row.ID, err))
	}
	if err := tx.Commit(dctx); err != nil {
		return errors.Join(sendErr, fmt.Errorf("commit outbox pass: %w", err))
	}
	return failure
}

// claim selects and locks up to limit due rows, oldest first, within tx.
func (s *OutboxStore) claim(ctx context.Context, tx pgx.Tx, limit, maxAttempts int) ([]outbox.Row, error) {
	filter, args := "", []any{limit}
	if maxAttempts > 0 {
		filter = "AND attempts < $2"
		args = append(args, maxAttempts)
	}
	rows, err := tx.Query(ctx, `
		SELECT id, event_id, aggreg_type, aggreg_id, payload, occurred_at
		FROM outbox
		WHERE next_attempt_at <= now() `+filter+`
		ORDER BY id
		LIMIT $1
		FOR UPDATE SKIP LOCKED
	`, args...)
	if err != nil {
		return nil, fmt.Errorf("claim outbox rows: %w", err)
	}
	defer rows.Close()

	out := make([]outbox.Row, 0, limit)
	for rows.Next() {
		var (
			row        outbox.Row
			eventID    string
			occurredAt time.Time
		)
		if err := rows.Scan(&row.ID, &eventID, &row.Fact.Entity, &row.Fact.AggregateID, &row.RawPayload, &occurredAt); err != nil {
			return nil, fmt.Errorf("scan outbox row: %w", err)
		}
		row.Fact.EventID = eventID
		row.Fact.OccurredAt = occurredAt.UTC()
		// The payload envelope OutboxPublisher wrote: event name, aggregate
		// id, occurred-at and the wire-shaped data. A payload that is not
		// that envelope leaves Fact.Event empty — the row is poison and the
		// send path dead-letters it with the raw bytes.
		var env struct {
			Event       string          `json:"event"`
			AggregateID string          `json:"aggregate_id"`
			OccurredAt  time.Time       `json:"occurred_at"`
			Data        json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(row.RawPayload, &env); err == nil {
			row.Fact.Event = env.Event
			if !env.OccurredAt.IsZero() {
				row.Fact.OccurredAt = env.OccurredAt.UTC()
			}
			if env.AggregateID != "" {
				row.Fact.AggregateID = env.AggregateID
			}
			row.Fact.Data = env.Data
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read outbox rows: %w", err)
	}
	return out, nil
}
