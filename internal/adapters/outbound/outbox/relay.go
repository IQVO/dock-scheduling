// Package outbox is the transactional-outbox relay: a background loop that
// drains the outbox table (written in the same transaction as the aggregate,
// see postgres.OutboxPublisher/OutboxStore) one row at a time through a
// Handler — in production the Kafka adapter's RelaySink, which encodes each
// fact to its CloudEvents of ADR 0004 and writes it to the broker, parking
// rows it can never publish on the dead-letter topic.
//
// The shape is the fleet's relay (pick-face-replenishment / slotting-
// optimization, after inventory-storage's outbox_relay.go): drain every
// interval (default 1s), a full batch is followed immediately by another
// pass, a failed pass is logged and retried after the interval, and delivery
// is at-least-once with the CloudEvents id persisted at enqueue time so a
// retry republishes the same id.
package outbox

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/claudioed/dock-scheduling/internal/application/outbox"
)

// Defaults for the relay's tuning knobs.
const (
	DefaultInterval  = time.Second
	DefaultBatchSize = 100
	// DefaultMaxAttempts is the attempt cap a composition root uses when
	// OUTBOX_MAX_ATTEMPTS is unset (fleet convention). The relay's own zero
	// value stays 0 (unlimited), so the cap is always an explicit
	// WithMaxAttempts at the composition root.
	DefaultMaxAttempts = 20
)

// AttemptCapReachedMsg is the ERROR log message written once for a row whose
// failure reached the attempt cap.
const AttemptCapReachedMsg = "outbox relay: row reached the attempt cap and is excluded from future claims; re-arm it with attempts = 0 after fixing the cause"

// Store is the relay's view of the outbox table: Drain claims up to limit
// due rows in id order — skipping rows that already failed maxAttempts times
// when maxAttempts > 0 — calls handle for each one at a time, removes a row
// only after handle returned nil, and stops at the first handle error, which
// it returns as an *outbox.SendFailureError. postgres.OutboxStore implements it.
type Store interface {
	Drain(ctx context.Context, limit, maxAttempts int, handle func(context.Context, outbox.Row) error) (int, error)
}

// Handler sends one claimed row on its way. kafka.RelaySink implements it:
// encode to CloudEvents, write to the broker, dead-letter the poison.
type Handler interface {
	Handle(ctx context.Context, row outbox.Row) error
}

// Relay drains a Store through a Handler.
type Relay struct {
	store       Store
	handler     Handler
	logger      *slog.Logger
	interval    time.Duration
	batchSize   int
	maxAttempts int
}

// Option customises a Relay.
type Option func(*Relay)

// WithInterval sets how long the relay sleeps after a pass that did not fill
// a batch (or failed). Non-positive values are ignored.
func WithInterval(d time.Duration) Option {
	return func(r *Relay) {
		if d > 0 {
			r.interval = d
		}
	}
}

// WithBatchSize caps how many rows one pass claims. Non-positive values are
// ignored.
func WithBatchSize(n int) Option {
	return func(r *Relay) {
		if n > 0 {
			r.batchSize = n
		}
	}
}

// WithMaxAttempts caps how many times a row may fail before it is excluded
// from future claims. The excluded row stays in the outbox with its attempts
// for inspection, the failure that reaches the cap is logged at ERROR with
// the row's id, event and key, and later rows — including rows with the same
// key — overtake it (the deliberate per-key ordering trade-off, ADR 0005).
// 0 means unlimited (the default); negative values are ignored. Every cmd/*
// that builds a relay must pass it (fitness test TestOutboxRelaysPassAttemptCap).
func WithMaxAttempts(n int) Option {
	return func(r *Relay) {
		if n >= 0 {
			r.maxAttempts = n
		}
	}
}

// NewOutboxRelay constructs a relay draining store through handler.
func NewOutboxRelay(store Store, handler Handler, logger *slog.Logger, opts ...Option) *Relay {
	if logger == nil {
		logger = slog.Default()
	}
	r := &Relay{store: store, handler: handler, logger: logger, interval: DefaultInterval, batchSize: DefaultBatchSize}
	for _, o := range opts {
		o(r)
	}
	return r
}

// RelayOnce performs a single pass and returns how many rows it published.
// When the pass failed on a row whose failure count reached the attempt cap,
// that row is logged once at ERROR (AttemptCapReachedMsg), after the failure
// was committed.
func (r *Relay) RelayOnce(ctx context.Context) (int, error) {
	n, err := r.store.Drain(ctx, r.batchSize, r.maxAttempts, r.handler.Handle)
	r.logAttemptCap(ctx, err)
	return n, err
}

// logAttemptCap logs the row whose failure has just reached the cap: from the
// next pass on it is excluded from claims and stays in the outbox until an
// operator re-arms it (UPDATE outbox SET attempts = 0, next_attempt_at = now()
// WHERE id = ...).
func (r *Relay) logAttemptCap(ctx context.Context, err error) {
	var sf *outbox.SendFailureError
	if r.maxAttempts == 0 || !errors.As(err, &sf) || sf.Attempts < r.maxAttempts {
		return
	}
	r.logger.ErrorContext(ctx, AttemptCapReachedMsg,
		"outbox_id", sf.RowID, "event_type", sf.Row.Fact.Entity+"."+sf.Row.Fact.Event,
		"key", sf.Row.Fact.AggregateID, "attempts", sf.Attempts,
		"max_attempts", r.maxAttempts, "error", sf.Err)
}

// Run drains the outbox until ctx is cancelled and then returns ctx.Err(). A
// pass that publishes a full batch is followed immediately by another (there
// is probably more waiting); any other pass sleeps for the interval. A
// failing pass is logged and retried after the interval — the rows stay in
// the outbox, so nothing is lost. Run never dials anything itself: a broker
// that is down at boot just makes passes fail until it is up.
func (r *Relay) Run(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := r.RelayOnce(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			r.logger.ErrorContext(ctx, "outbox relay pass failed", "error", err)
		}
		if n == r.batchSize && err == nil {
			continue
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(r.interval):
		}
	}
}
