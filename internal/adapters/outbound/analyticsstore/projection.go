package analyticsstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/claudioed/dock-scheduling/internal/analytics/adherence"
)

// Projection is the Postgres writer of the analytical model: it implements
// adherence.Projection over the WRITER pool.
type Projection struct{ pool *pgxpool.Pool }

var _ adherence.Projection = (*Projection)(nil)

// NewProjection builds the writer over a pool from NewPool.
func NewProjection(pool *pgxpool.Pool) *Projection { return &Projection{pool: pool} }

// Apply records the event id and folds the event into appointment_facts in ONE
// transaction. A second call with the same id commits nothing and returns
// applied = false. Every milestone is kept with LEAST/COALESCE and the window
// is guarded by the aggregate version, so neither a replay nor events arriving
// out of order change the outcome.
func (p *Projection) Apply(ctx context.Context, e adherence.Event) (applied bool, err error) {
	if err := e.Validate(); err != nil {
		return false, err
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tag, err := tx.Exec(ctx,
		`INSERT INTO analytics_processed_events (event_id, event_type, occurred_at) VALUES ($1, $2, $3) ON CONFLICT (event_id) DO NOTHING`,
		e.EventID, string(e.Kind), e.At)
	if err != nil {
		return false, classify(err)
	}
	if tag.RowsAffected() == 0 {
		return false, tx.Commit(ctx)
	}
	if err := fold(ctx, tx, e); err != nil {
		return false, classify(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, classify(err)
	}
	return true, nil
}

// The upserts. A fact that arrives before the event that creates the row
// creates it (site_id is NOT NULL and every analytics event carries it), and a
// later event only fills what its predecessor left open.
const (
	// Booked and Rescheduled: the window moves only forward in aggregate
	// version; the carrier and the booking instant are filled once.
	sqlWindow = `
INSERT INTO appointment_facts (appointment_id, site_id, carrier_scac, window_start, window_end, window_version, booked_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (appointment_id) DO UPDATE SET
    carrier_scac   = COALESCE(EXCLUDED.carrier_scac, appointment_facts.carrier_scac),
    window_start   = CASE WHEN EXCLUDED.window_version > appointment_facts.window_version THEN EXCLUDED.window_start ELSE appointment_facts.window_start END,
    window_end     = CASE WHEN EXCLUDED.window_version > appointment_facts.window_version THEN EXCLUDED.window_end   ELSE appointment_facts.window_end   END,
    window_version = GREATEST(appointment_facts.window_version, EXCLUDED.window_version),
    booked_at      = LEAST(appointment_facts.booked_at, EXCLUDED.booked_at)`

	sqlCheckedIn = `
INSERT INTO appointment_facts (appointment_id, site_id, checked_in_at, arrival_status)
VALUES ($1, $2, $3, $4)
ON CONFLICT (appointment_id) DO UPDATE SET
    checked_in_at  = LEAST(appointment_facts.checked_in_at, EXCLUDED.checked_in_at),
    arrival_status = COALESCE(appointment_facts.arrival_status, EXCLUDED.arrival_status)`

	sqlCompleted = `
INSERT INTO appointment_facts (appointment_id, site_id, completed_at)
VALUES ($1, $2, $3)
ON CONFLICT (appointment_id) DO UPDATE SET
    completed_at = LEAST(appointment_facts.completed_at, EXCLUDED.completed_at)`

	sqlCancelled = `
INSERT INTO appointment_facts (appointment_id, site_id, cancelled_at, cancellation_reason)
VALUES ($1, $2, $3, $4)
ON CONFLICT (appointment_id) DO UPDATE SET
    cancelled_at        = LEAST(appointment_facts.cancelled_at, EXCLUDED.cancelled_at),
    cancellation_reason = COALESCE(appointment_facts.cancellation_reason, EXCLUDED.cancellation_reason)`

	sqlNoShow = `
INSERT INTO appointment_facts (appointment_id, site_id, carrier_scac, window_end, no_show_at)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (appointment_id) DO UPDATE SET
    carrier_scac = COALESCE(appointment_facts.carrier_scac, EXCLUDED.carrier_scac),
    window_end   = COALESCE(appointment_facts.window_end, EXCLUDED.window_end),
    no_show_at   = LEAST(appointment_facts.no_show_at, EXCLUDED.no_show_at)`
)

func fold(ctx context.Context, tx pgx.Tx, e adherence.Event) error {
	var err error
	switch e.Kind {
	case adherence.Booked:
		_, err = tx.Exec(ctx, sqlWindow, e.AppointmentID, e.SiteID, text(e.CarrierSCAC), e.WindowStart, e.WindowEnd, e.Version, e.At)
	case adherence.Rescheduled:
		_, err = tx.Exec(ctx, sqlWindow, e.AppointmentID, e.SiteID, nil, e.WindowStart, e.WindowEnd, e.Version, nil)
	case adherence.CheckedIn:
		_, err = tx.Exec(ctx, sqlCheckedIn, e.AppointmentID, e.SiteID, e.CheckedInAt, string(e.ArrivalStatus))
	case adherence.Completed:
		_, err = tx.Exec(ctx, sqlCompleted, e.AppointmentID, e.SiteID, e.CompletedAt)
	case adherence.Cancelled:
		_, err = tx.Exec(ctx, sqlCancelled, e.AppointmentID, e.SiteID, e.At, text(e.Reason))
	case adherence.NoShow:
		_, err = tx.Exec(ctx, sqlNoShow, e.AppointmentID, e.SiteID, text(e.CarrierSCAC), instant(e.WindowEnd), e.At)
	default:
		err = fmt.Errorf("%w: unknown kind %q", adherence.ErrInvalidEvent, e.Kind)
	}
	return err
}

// text maps "" to SQL NULL.
func text(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// instant maps the zero time to SQL NULL.
func instant(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

// classify wraps the errors Postgres raises DETERMINISTICALLY for a given
// statement and data (SQLSTATE class 22, data exception, and class 23,
// integrity constraint violation) in adherence.ErrRejected: the same bytes can
// never succeed, so the consumer dead-letters them. Everything else (a dropped
// connection, a timeout, a shutdown) is transient and returned unchanged.
func classify(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && len(pgErr.Code) == 5 && (pgErr.Code[:2] == "22" || pgErr.Code[:2] == "23") {
		return fmt.Errorf("%w: %w", adherence.ErrRejected, err)
	}
	return err
}
