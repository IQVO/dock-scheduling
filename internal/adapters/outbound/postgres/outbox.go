package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/claudioed/dock-scheduling/internal/application/outbox"
	"github.com/claudioed/dock-scheduling/internal/application/usecases"
	"github.com/claudioed/dock-scheduling/internal/domain/carrier"
	"github.com/claudioed/dock-scheduling/internal/domain/dockappointment"
	"github.com/claudioed/dock-scheduling/internal/domain/dockcalendar"
)

// Aggregate type names of the outbox rows (aggreg_type column); they are the
// `entity` segment of the CloudEvents `type` (ADR 0004).
const (
	aggregDockAppointment = "dockappointment"
	aggregDockCalendar    = "dockcalendar"
	aggregCarrier         = "carrier"
)

// OutboxPublisher is the pgxpool-backed ports.EventPublisher: the write side
// of the transactional outbox. Called in the SAME UnitOfWork as the
// aggregate's Save, every Publish inserts one row per domain fact; the relay
// (adapters/outbound/outbox) drains the rows to Kafka as CloudEvents 1.0.
// The CloudEvents id (event_id, UUID v4) is minted ONCE here and persisted,
// so a relay retry republishes the same id.
type OutboxPublisher struct {
	pool *pgxpool.Pool
	// NewID mints each row's CloudEvents id; uuid.NewString when nil. The
	// seam exists so the integration proof can pin the golden ids.
	NewID func() string
}

// NewOutboxPublisher constructs an OutboxPublisher over pool.
func NewOutboxPublisher(pool *pgxpool.Pool) *OutboxPublisher {
	return &OutboxPublisher{pool: pool}
}

func (p *OutboxPublisher) mintID() string {
	if p.NewID != nil {
		return p.NewID()
	}
	return uuid.NewString()
}

// outboxEnvelope is the JSON payload of one outbox row: which fact happened,
// to which aggregate, when, and the fact's data already in the wire shape of
// ADR 0004 (the relay builds the CloudEvent from these fields; it never
// re-derives domain state).
type outboxEnvelope struct {
	Event       string    `json:"event"`
	AggregateID string    `json:"aggregate_id"`
	OccurredAt  time.Time `json:"occurred_at"`
	Data        any       `json:"data"`
}

// PublishAppointmentEvents implements ports.EventPublisher.
func (p *OutboxPublisher) PublishAppointmentEvents(ctx context.Context, events ...dockappointment.Event) error {
	for _, e := range events {
		data, err := outbox.AppointmentFactData(e, p.glnLookup(ctx))
		if err != nil {
			return err
		}
		env := outboxEnvelope{Event: e.EventName(), AggregateID: e.AppointmentID(), OccurredAt: e.OccurredAt().UTC(), Data: data}
		if err := p.enqueue(ctx, aggregDockAppointment, e.AppointmentID(), env); err != nil {
			return err
		}
	}
	return nil
}

// PublishCalendarEvents implements ports.EventPublisher.
func (p *OutboxPublisher) PublishCalendarEvents(ctx context.Context, events ...dockcalendar.Event) error {
	for _, e := range events {
		data, err := outbox.CalendarFactData(e, p.glnLookup(ctx))
		if err != nil {
			return err
		}
		env := outboxEnvelope{Event: e.EventName(), AggregateID: e.SiteID(), OccurredAt: e.OccurredAt().UTC(), Data: data}
		if err := p.enqueue(ctx, aggregDockCalendar, e.SiteID(), env); err != nil {
			return err
		}
	}
	return nil
}

// PublishCarrierEvents implements ports.EventPublisher. A carrier fact raised
// by an agent write (MCP) carries the attribution the use case's context holds
// as `initiated_by` (ADR 0012); REST writes carry none.
func (p *OutboxPublisher) PublishCarrierEvents(ctx context.Context, events ...carrier.Event) error {
	by := initiatedBy(usecases.InitiatorFrom(ctx))
	for _, e := range events {
		data, err := carrierFactData(e, by)
		if err != nil {
			return err
		}
		env := outboxEnvelope{Event: e.EventName(), AggregateID: e.SCAC(), OccurredAt: e.OccurredAt().UTC(), Data: data}
		if err := p.enqueue(ctx, aggregCarrier, e.SCAC(), env); err != nil {
			return err
		}
	}
	return nil
}

// glnLookup resolves a door's GS1 GLN from the doors local copy (ADR 0003,
// ADR 0013) in the transaction the outbox row is written in, so the read point
// a fact carries is the one known when the fact was recorded. An unknown door
// and a door with no GLN both give "": the fact then omits `read_point_gln`.
func (p *OutboxPublisher) glnLookup(ctx context.Context) outbox.GLNLookup {
	return func(doorCode string) (string, error) {
		var gln string
		err := queryFor(ctx, p.pool).QueryRow(ctx, `SELECT COALESCE(gln, '') FROM doors WHERE door_code = $1`, doorCode).Scan(&gln)
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil
		}
		if err != nil {
			return "", fmt.Errorf("read the GLN of door %s: %w", doorCode, err)
		}
		return gln, nil
	}
}

func (p *OutboxPublisher) enqueue(ctx context.Context, aggregType, aggregID string, env outboxEnvelope) error {
	payload, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("encode outbox row for %s %s: %w", aggregType, aggregID, err)
	}
	_, err = queryFor(ctx, p.pool).Exec(ctx, `
		INSERT INTO outbox (event_id, aggreg_type, aggreg_id, payload, occurred_at)
		VALUES ($1, $2, $3, $4, $5)
	`, p.mintID(), aggregType, aggregID, json.RawMessage(payload), env.OccurredAt)
	if err != nil {
		return fmt.Errorf("enqueue %s fact for %s %s: %w", env.Event, aggregType, aggregID, err)
	}
	return nil
}
