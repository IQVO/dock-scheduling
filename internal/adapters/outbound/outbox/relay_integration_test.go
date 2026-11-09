//go:build integration

package outbox_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	tckafka "github.com/testcontainers/testcontainers-go/modules/kafka"

	"github.com/claudioed/dock-scheduling/internal/adapters/kafka/cloudevents"
	outboundkafka "github.com/claudioed/dock-scheduling/internal/adapters/outbound/kafka"
	relay "github.com/claudioed/dock-scheduling/internal/adapters/outbound/outbox"
	"github.com/claudioed/dock-scheduling/internal/adapters/outbound/postgres"
	"github.com/claudioed/dock-scheduling/internal/domain/dockappointment"
	"github.com/claudioed/dock-scheduling/internal/domain/dockcalendar"
	"github.com/claudioed/dock-scheduling/internal/domain/shared"
	"github.com/claudioed/dock-scheduling/internal/testing/kafkatest"
	"github.com/claudioed/dock-scheduling/internal/testing/pgtest"
)

// The outbox path end to end against REAL infrastructure, both started with
// testcontainers (never skip-gated, never a hardcoded broker address, topics
// created explicitly with the leader waited for): the production
// OutboxPublisher writes every published fact inside real UnitOfWork
// transactions, the production relay + RelaySink drain them to a real broker on
// the REAL topic names, and the consumed CloudEvents are compared — every
// attribute and the whole data object — with the encoder's golden files, the
// single source of truth of the wire contract. The DLQ path is proven the same
// way on a poison row.

// The broker comes from internal/testing/kafkatest, which starts it through
// testcontainers-go/modules/kafka (referenced here so the module import
// stays reachable from this Kafka-touching file).
var _ = tckafka.Run

const (
	apptID = "appt-123e4567-e89b-12d3-a456-426614174000"
	door1  = "WH1-DOCK-IN-01"
	door2  = "WH1-DOCK-IN-02"
)

func at(day, h, m, s int) time.Time { return time.Date(2026, 11, day, h, m, s, 0, time.UTC) }

// golden loads one of the kafka encoder's golden envelopes, so the wire
// contract has ONE source of truth (the same bytes its unit test pins).
func golden(t *testing.T, name string) any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "kafka", "testdata", name))
	if err != nil {
		t.Fatalf("read golden %s: %v", name, err)
	}
	return decodeJSON(t, name, raw)
}

func decodeJSON(t *testing.T, what string, raw []byte) any {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("%s is not JSON: %v\n%s", what, err, raw)
	}
	return v
}

// assertEnvelope checks one consumed message: the Kafka key, exactly the
// CloudEvents content-type header, a valid CloudEvents 1.0 value, and the whole
// envelope against its golden file.
func assertEnvelope(t *testing.T, msg kafkatest.Message, wantKey, goldenName string) {
	t.Helper()
	if msg.Key != wantKey {
		t.Errorf("%s: key = %q, want %q", goldenName, msg.Key, wantKey)
	}
	if len(msg.Headers) != 1 || msg.Headers["content-type"] != cloudevents.MediaType {
		t.Errorf("%s: headers = %v, want exactly the content-type %q", goldenName, msg.Headers, cloudevents.MediaType)
	}
	if _, err := cloudevents.Decode(msg.Value); err != nil {
		t.Fatalf("%s: not a valid CloudEvent: %v", goldenName, err)
	}
	if got, want := decodeJSON(t, goldenName, msg.Value), golden(t, goldenName); !reflect.DeepEqual(got, want) {
		t.Errorf("%s: envelope differs from the golden\n got: %s\nwant: %v", goldenName, msg.Value, want)
	}
}

// ids hands out the golden files' CloudEvents ids, in publication order.
func ids(list ...string) func() string {
	i := 0
	return func() string {
		id := list[i%len(list)]
		i++
		return id
	}
}

func quiet() *slog.Logger { return slog.New(slog.NewJSONHandler(io.Discard, nil)) }

// runRelay starts the relay's Run loop and stops it (and closes the sink) when
// the test ends.
func runRelay(t *testing.T, r *relay.Relay, sink *outboundkafka.RelaySink) {
	t.Helper()
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = r.Run(ctx)
	}()
	t.Cleanup(func() {
		stop()
		<-done
		if err := sink.Close(); err != nil {
			t.Errorf("close relay sink: %v", err)
		}
	})
}

// waitFor polls until cond is true, failing the test at the deadline.
func waitFor(t *testing.T, what string, deadline time.Duration, cond func() bool) {
	t.Helper()
	end := time.Now().Add(deadline)
	for time.Now().Before(end) {
		if cond() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func apptHeader(version int64, when time.Time) dockappointment.Header {
	return dockappointment.Header{Appointment: apptID, Site: "WH1", Version: version, At: when}
}

// appointmentFacts are the eight appointment facts, in publication order, with
// the occurrence instants of their golden files.
func appointmentFacts() []dockappointment.Event {
	return []dockappointment.Event{
		dockappointment.BookedEvent{
			Header: apptHeader(1, at(5, 12, 0, 0)), Direction: shared.Inbound, DoorCode: door1, Carrier: "ACME Freight", CarrierSCAC: "ACMF",
			WindowStart: at(6, 8, 0, 0), WindowEnd: at(6, 10, 0, 0), ASNNumbers: []string{"ASN-1001"}, ExpectedSeal: "SEAL-77120", ToleranceMinutes: 15, Provenance: dockappointment.Native,
		},
		dockappointment.RescheduledEvent{
			Header: apptHeader(2, at(5, 13, 10, 0)), DoorCode: door2, PreviousDoorCode: door1, WindowStart: at(6, 9, 0, 0), WindowEnd: at(6, 11, 0, 0),
			PreviousWindowStart: at(6, 8, 0, 0), PreviousWindowEnd: at(6, 10, 0, 0), ToleranceMinutes: 15,
		},
		dockappointment.CancelledEvent{Header: apptHeader(2, at(5, 14, 20, 0)), DoorCode: door1, Reason: dockappointment.CarrierRequest},
		dockappointment.NoShowEvent{Header: apptHeader(2, at(6, 11, 5, 0)), DoorCode: door1, Direction: shared.Inbound, CarrierSCAC: "ACMF", WindowEnd: at(6, 10, 0, 0)},
		dockappointment.CheckedInEvent{
			Header: apptHeader(2, at(6, 7, 55, 30)), DoorCode: door1, CheckedInAt: at(6, 7, 55, 0), ArrivalStatus: dockappointment.OnTime, VisitID: "visit-0b5c1f2e-7a9d-4c31-8e64-5d2f9a7b3c10",
		},
		dockappointment.DoorReassigned{Header: apptHeader(3, at(6, 8, 20, 0)), FromDoorCode: door1, ToDoorCode: door2},
		dockappointment.CompletedEvent{Header: apptHeader(3, at(6, 9, 40, 10)), DoorCode: door1, CompletedAt: at(6, 9, 40, 0)},
		dockappointment.AtRisk{Header: apptHeader(2, at(6, 7, 30, 0)), DoorCode: door1, EtaAt: at(6, 9, 10, 0), WindowStart: at(6, 8, 0, 0), WindowEnd: at(6, 10, 0, 0), Cause: dockappointment.LateEta},
	}
}

func calendarFacts(t *testing.T) []dockcalendar.Event {
	t.Helper()
	closure := dockcalendar.Closure{ID: "clo-123e4567-e89b-12d3-a456-426614174001", Door: door1, From: at(10, 0, 0, 0), To: at(11, 0, 0, 0), Reason: "Leveller maintenance"}
	monday, err := dockcalendar.NewOperatingHours("", dockcalendar.Mon, "06:00", "22:00", "America/Sao_Paulo")
	if err != nil {
		t.Fatal(err)
	}
	tuesday, err := dockcalendar.NewOperatingHours(door1, dockcalendar.Tue, "08:00", "24:00", "America/Sao_Paulo")
	if err != nil {
		t.Fatal(err)
	}
	return []dockcalendar.Event{
		dockcalendar.ClosureScheduled{Header: dockcalendar.Header{Site: "WH1", Version: 3, At: at(5, 15, 0, 0)}, Closure: closure},
		dockcalendar.ClosureLifted{Header: dockcalendar.Header{Site: "WH1", Version: 4, At: at(7, 9, 0, 0)}, Closure: closure},
		dockcalendar.OperatingHoursDeclared{Header: dockcalendar.Header{Site: "WH1", Version: 2, At: at(5, 11, 0, 0)}, Rows: []dockcalendar.OperatingHours{monday, tuesday}},
	}
}

// TestRelay_RealPostgresAndKafka_PublishesTheExactEnvelopes: all eleven
// published facts of ADR 0004 are written through the production
// OutboxPublisher inside real unit-of-work transactions; the production relay
// and RelaySink publish them, in outbox order. The events topic carries
// exactly the seven integration facts (ADR 0010 added the door reassignment)
// and the analytics topic all eleven; every
// message matches its golden envelope exactly.
func TestRelay_RealPostgresAndKafka_PublishesTheExactEnvelopes(t *testing.T) {
	ctx := context.Background()
	brokers := kafkatest.Brokers(t)
	pool := pgtest.NewPool(t)
	kafkatest.CreateTopic(t, outboundkafka.TopicEvents)
	// The broker has auto-creation OFF, so the analytics topic must exist before the drain.
	kafkatest.CreateTopic(t, outboundkafka.TopicAnalytics)

	pub := postgres.NewOutboxPublisher(pool)
	pub.NewID = ids(
		"6f1d2c3b-4a5e-4b7c-8d9e-0a1b2c3d4e5f", // DockAppointmentBooked
		"0b9a8c7d-6e5f-4a3b-9c2d-1e0f9a8b7c6d", // DockAppointmentRescheduled
		"2b1a0f9e-8d7c-4b6a-a594-837261504f3e", // DockAppointmentCancelled
		"4d3c2b1a-0f9e-4d8c-8b7a-6f5e4d3c2b1a", // DockAppointmentNoShow
		"8c7d6e5f-4a3b-4c2d-9e0f-9a8b7c6d5e4f", // DockAppointmentCheckedIn
		"a5948372-6150-4f3e-8d7c-4b6a2b1a0f9e", // DockAppointmentDoorReassigned
		"d4e5f6a7-b8c9-4d0e-8f2a-3b4c5d6e7f80", // DockAppointmentCompleted
		"f3e2d1c0-9b8a-4c5d-8e7f-8a9b0c1d2e3f", // DockAppointmentAtRisk
		"1a2b3c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5d", // DoorClosureScheduled
		"5d4c3b2a-1f0e-4d9c-ab8a-7f6e5d4c3b2a", // DoorClosureLifted
		"9e8d7c6b-5a4f-4e3d-8c2b-1a0f9e8d7c6b", // DoorOperatingHoursDeclared
	)
	uow := postgres.NewUnitOfWork(pool)
	if err := uow.Do(ctx, func(ctx context.Context) error { return pub.PublishAppointmentEvents(ctx, appointmentFacts()...) }); err != nil {
		t.Fatalf("appointment unit of work: %v", err)
	}
	if err := uow.Do(ctx, func(ctx context.Context) error { return pub.PublishCalendarEvents(ctx, calendarFacts(t)...) }); err != nil {
		t.Fatalf("calendar unit of work: %v", err)
	}

	sink := outboundkafka.NewRelaySink(strings.Join(brokers, ","))
	r := relay.NewOutboxRelay(postgres.NewOutboxStore(pool), sink, quiet(), relay.WithMaxAttempts(relay.DefaultMaxAttempts))
	runRelay(t, r, sink)

	integration := kafkatest.ReadN(t, outboundkafka.TopicEvents, 7, 60*time.Second)
	for i, w := range []struct{ key, golden string }{
		{apptID, "dock_appointment_booked.golden.json"},
		{apptID, "dock_appointment_rescheduled.golden.json"},
		{apptID, "dock_appointment_cancelled.golden.json"},
		{apptID, "dock_appointment_no_show.golden.json"},
		{apptID, "dock_appointment_door_reassigned.golden.json"}, // ADR 0010
		{"WH1", "door_closure_scheduled.golden.json"},
		{"WH1", "door_closure_lifted.golden.json"},
	} {
		assertEnvelope(t, integration[i], w.key, w.golden)
	}

	analytics := kafkatest.ReadN(t, outboundkafka.TopicAnalytics, 11, 60*time.Second)
	for i, w := range []struct{ key, golden string }{
		{apptID, "dock_appointment_booked_analytics.golden.json"},
		{apptID, "dock_appointment_rescheduled_analytics.golden.json"},
		{apptID, "dock_appointment_cancelled_analytics.golden.json"},
		{apptID, "dock_appointment_no_show_analytics.golden.json"},
		{apptID, "dock_appointment_checked_in_analytics.golden.json"},
		{apptID, "dock_appointment_door_reassigned_analytics.golden.json"},
		{apptID, "dock_appointment_completed_analytics.golden.json"},
		{apptID, "dock_appointment_at_risk_analytics.golden.json"},
		{"WH1", "door_closure_scheduled_analytics.golden.json"},
		{"WH1", "door_closure_lifted_analytics.golden.json"},
		{"WH1", "door_operating_hours_declared_analytics.golden.json"},
	} {
		assertEnvelope(t, analytics[i], w.key, w.golden)
	}

	// Analytics-only facts never reach the integration topic.
	if n := kafkatest.EndOffset(t, outboundkafka.TopicEvents); n != 7 {
		t.Errorf("the events topic holds %d messages, want exactly the 7 integration facts", n)
	}
	// Every published row left the outbox (delete-on-publish).
	waitFor(t, "the outbox to drain", 10*time.Second, func() bool { return pgtest.Count(t, pool, "outbox") == 0 })
}

// TestRelay_PoisonRowDeadLettersOnRealKafka: a row whose fact has no wire shape
// can never publish; the relay parks its raw payload on
// warehouse.dock-scheduling.events.dlq with the x-dlq-* headers and disposes of
// the row.
func TestRelay_PoisonRowDeadLettersOnRealKafka(t *testing.T) {
	ctx := context.Background()
	brokers := kafkatest.Brokers(t)
	pool := pgtest.NewPool(t)
	dlqTopic := outboundkafka.DeadLetterTopic
	kafkatest.CreateTopic(t, dlqTopic)

	payload := `{"event":"DockAppointmentVanished","aggregate_id":"appt-poison-1","occurred_at":"2026-11-05T09:30:00Z","data":{"appointment_id":"appt-poison-1"}}`
	var id int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO outbox (event_id, aggreg_type, aggreg_id, payload, occurred_at)
		VALUES ('evt-poison-1', 'dockappointment', 'appt-poison-1', $1::jsonb, now())
		RETURNING id
	`, payload).Scan(&id); err != nil {
		t.Fatal(err)
	}

	sink := outboundkafka.NewRelaySink(strings.Join(brokers, ","))
	r := relay.NewOutboxRelay(postgres.NewOutboxStore(pool), sink, quiet(), relay.WithMaxAttempts(relay.DefaultMaxAttempts))
	runRelay(t, r, sink)

	got := kafkatest.ReadN(t, dlqTopic, 1, 60*time.Second)[0]
	if got.Key != "appt-poison-1" {
		t.Errorf("key = %q, want the aggregate id", got.Key)
	}
	if gotValue, want := decodeJSON(t, "dlq value", got.Value), decodeJSON(t, "payload", []byte(payload)); !reflect.DeepEqual(gotValue, want) {
		t.Errorf("dlq value = %s, want the raw row payload %s", got.Value, payload)
	}
	for _, h := range []string{"content-type", "x-dlq-error", "x-dlq-outbox-row", "x-dlq-aggregate", "x-dlq-failed-at"} {
		if got.Headers[h] == "" {
			t.Errorf("header %q missing: %v", h, got.Headers)
		}
	}
	if got.Headers["x-dlq-outbox-row"] != jsonInt(id) || got.Headers["x-dlq-aggregate"] != "appt-poison-1" {
		t.Errorf("x-dlq headers = %s / %s", got.Headers["x-dlq-outbox-row"], got.Headers["x-dlq-aggregate"])
	}

	// The disposed row is gone from the outbox (the DLQ write disposes, like a publish).
	waitFor(t, "the poison row to be disposed", 10*time.Second, func() bool {
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE id = $1`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n == 0
	})
}

func jsonInt(n int64) string {
	b, err := json.Marshal(n)
	if err != nil {
		return ""
	}
	return string(b)
}
