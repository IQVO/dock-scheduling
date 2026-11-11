//go:build integration

package kafka

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/claudioed/dock-scheduling/internal/adapters/outbound/memory"
	"github.com/claudioed/dock-scheduling/internal/adapters/outbound/postgres"
	"github.com/claudioed/dock-scheduling/internal/application/usecases"
	"github.com/claudioed/dock-scheduling/internal/testing/kafkatest"
	"github.com/claudioed/dock-scheduling/internal/testing/pgtest"
)

// itWorld is the real stack under the consumers: a Postgres cloned from the
// migrated template, the production repositories, unit of work and outbox, and
// a real Kafka broker through the production Transport. Only the metrics sink
// and the clock/ids are test doubles.
type itWorld struct {
	t       *testing.T
	pool    *pgxpool.Pool
	svc     *usecases.Service
	metrics *memory.Metrics
	ids     *fixedIDs
	tr      Transport
	ctx     context.Context
	// the production unit of work and claim repository, for the tests that
	// exercise them without a use case
	uow       *postgres.UnitOfWork
	processed *postgres.ProcessedEventRepo
}

func newITWorld(t *testing.T) *itWorld {
	t.Helper()
	pool := pgtest.NewPool(t)
	w := &itWorld{t: t, pool: pool, metrics: &memory.Metrics{}, ids: &fixedIDs{}, ctx: context.Background(),
		uow: postgres.NewUnitOfWork(pool), processed: postgres.NewProcessedEventRepo(pool)}
	w.svc = usecases.New(usecases.Deps{
		Appointments: postgres.NewAppointmentRepo(pool), Calendars: postgres.NewCalendarRepo(pool), Carriers: postgres.NewCarrierRepo(pool), Doors: postgres.NewDoorStore(pool),
		Asns: postgres.NewAsnStore(pool), Cutoffs: postgres.NewCutoffStore(pool), Processed: w.processed,
		Events: postgres.NewOutboxPublisher(pool), Metrics: w.metrics, IDs: w.ids, Clock: &fixedClock{now: testNow},
		UoW: w.uow,
	})
	w.tr = NewTransport(kafkatest.Brokers(t), slog.New(slog.NewTextHandler(io.Discard, nil)))
	w.tr.Retry = RetryPolicy{Initial: 50 * time.Millisecond, Max: 200 * time.Millisecond}
	return w
}

// topics creates a unique topic and its dead letter topic (the broker has
// topic auto-creation off).
func (w *itWorld) topics(prefix string) (topic, dlq string) {
	w.t.Helper()
	topic = kafkatest.Topic(w.t, prefix)
	dlq = DeadLetterTopic(topic)
	kafkatest.CreateTopic(w.t, dlq)
	return topic, dlq
}

func (w *itWorld) group(name string) string {
	return "it-" + name + "-" + time.Now().Format("150405.000000000")
}

func (w *itWorld) produce(topic string, values ...[]byte) {
	w.t.Helper()
	msgs := make([]kafkatest.Message, len(values))
	for i, v := range values {
		msgs[i] = kafkatest.Message{Key: "k", Value: v}
	}
	kafkatest.Produce(w.t, topic, msgs...)
}

// run starts the consumers and stops them when the test ends.
func (w *itWorld) run(cs ...*Consumer) *Running {
	w.t.Helper()
	r := Start(w.ctx, cs...)
	w.t.Cleanup(func() { _ = r.Stop() })
	return r
}

func (w *itWorld) count(table string) int { return pgtest.Count(w.t, w.pool, table) }

func (w *itWorld) eventually(what string, cond func() bool) {
	w.t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	w.t.Fatalf("timed out waiting for %s", what)
}

// committedPast waits until group committed n messages of topic.
func (w *itWorld) committedPast(group, topic string, n int64) {
	w.t.Helper()
	w.eventually("the offset of "+group+" to reach the end of the topic", func() bool {
		offset, ok := kafkatest.TryCommittedOffset(w.t, group, topic)
		return ok && offset == n
	})
}

// seedFacility registers the zone and the two doors the yard fixtures name,
// through the use cases of the real stack.
func (w *itWorld) seedFacility() {
	w.t.Helper()
	must := func(err error) {
		w.t.Helper()
		if err != nil {
			w.t.Fatal(err)
		}
	}
	must(w.svc.ConsumeZoneRegistered(w.ctx, "seed-zone", "zone-dock-wh1", fixtureSite))
	must(w.svc.ConsumeSlotRegistered(w.ctx, "seed-d01", fixtureDoor, "zone-dock-wh1", "Dock", "Both"))
	must(w.svc.ConsumeSlotRegistered(w.ctx, "seed-d02", "WH1-DOCK-D02", "zone-dock-wh1", "Dock", "Inbound"))
	must(w.svc.ConsumeAsnRegistered(w.ctx, "seed-asn", fixtureASN, bookedStart))
}

// seedBooked is seedFacility plus the BOOKED fixture appointment on D01.
func (w *itWorld) seedBooked() {
	w.t.Helper()
	w.seedFacility()
	w.book(fixtureAppointment, fixtureDoor, fixtureASN)
}

func (w *itWorld) book(id, door string, asns ...string) {
	w.t.Helper()
	w.ids.ids = []string{id}
	cmd := usecases.BookCommand{
		DoorCode: door, Direction: "INBOUND", Carrier: "Acme Freight", CarrierSCAC: "ACME",
		WindowStart: bookedStart, WindowEnd: bookedEnd, ASNNumbers: asns,
	}
	if _, err := w.svc.BookAppointment(w.ctx, cmd); err != nil {
		w.t.Fatalf("book %s: %v", id, err)
	}
}

func (w *itWorld) appt(id string) apptState {
	w.t.Helper()
	v, err := w.svc.GetAppointment(w.ctx, id)
	if err != nil {
		w.t.Fatalf("GetAppointment(%s): %v", id, err)
	}
	s := apptState{State: v.State, Door: v.DoorCode, Reason: v.CancellationReason, Provenance: v.Provenance, AtRisk: v.AtRisk, Version: v.Version}
	if v.Arrival != nil {
		s.Arrival, s.Implicit = v.Arrival.Status, v.Arrival.Implicit
	}
	return s
}

// flatEnvelope is the retired pre-CloudEvents shape: WARNed and committed past.
var flatEnvelope = []byte(`{"event_id":"e-flat","event_type":"TrailerDocked","occurred_at":"2026-11-05T14:10:00Z"}`)
