//go:build integration

package main

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	// Real Postgres and Kafka come from the shared testcontainers helpers
	// (this file is the package's anchor for the
	// TestPostgresIntegrationTestsUseTestcontainers and
	// TestKafkaIntegrationTestsUseTestcontainers sensors).
	_ "github.com/testcontainers/testcontainers-go/modules/kafka"
	_ "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/jackc/pgx/v5/pgxpool"

	outboundkafka "github.com/claudioed/dock-scheduling/internal/adapters/outbound/kafka"
	"github.com/claudioed/dock-scheduling/internal/adapters/outbound/postgres"
	"github.com/claudioed/dock-scheduling/internal/adapters/outbound/system"
	"github.com/claudioed/dock-scheduling/internal/adapters/outbound/telemetry"
	"github.com/claudioed/dock-scheduling/internal/application/usecases"
	"github.com/claudioed/dock-scheduling/internal/testing/kafkatest"
	"github.com/claudioed/dock-scheduling/internal/testing/pgtest"
)

func TestMain(m *testing.M) {
	code := m.Run()
	kafkatest.Shutdown()
	pgtest.Shutdown()
	os.Exit(code)
}

// wireUseCases builds the REAL write use cases over the pool with the same
// adapters cmd/api wires (buildAdapters): every seed below is a genuine use
// case whose aggregate rows and outbox rows commit in one transaction.
func wireUseCases(t *testing.T, pool *pgxpool.Pool) *usecases.Service {
	t.Helper()
	metrics, err := telemetry.New()
	if err != nil {
		t.Fatalf("metrics: %v", err)
	}
	return usecases.New(usecases.Deps{
		Appointments: postgres.NewAppointmentRepo(pool), Calendars: postgres.NewCalendarRepo(pool), Carriers: postgres.NewCarrierRepo(pool),
		Doors: postgres.NewDoorStore(pool), Asns: postgres.NewAsnStore(pool), Cutoffs: postgres.NewCutoffStore(pool),
		Processed: postgres.NewProcessedEventRepo(pool), Events: postgres.NewOutboxPublisher(pool), Metrics: metrics,
		IDs: system.IDs{}, Clock: system.Clock{}, UoW: postgres.NewUnitOfWork(pool),
	})
}

// TestRunRelaysOutboxRowsSeededByTheWriteUseCases runs the real run() over a
// migrated testcontainers Postgres and a testcontainers Kafka after the REAL
// write use cases seeded the outbox in their own transactions. ADR 0004 and
// ADR 0005: the integration fact (CarrierRegistered) reaches the events topic
// and, as a twin with the SAME CloudEvents id, the analytics topic; the
// analytics-only fact (DoorOperatingHoursDeclared) reaches ONLY the analytics
// topic; the published rows are deleted from the outbox; a cancelled context
// stops the binary cleanly (run returns nil).
func TestRunRelaysOutboxRowsSeededByTheWriteUseCases(t *testing.T) {
	for _, topic := range outboundkafka.Topics() {
		kafkatest.CreateTopic(t, topic)
	}
	dsn := pgtest.URL(t)
	pool := pgtest.Open(t, dsn)
	svc := wireUseCases(t, pool)

	ctx := context.Background()
	if _, err := svc.RegisterCarrier(ctx, usecases.RegisterCarrierCommand{
		SCAC: "ITRL", Name: "Integration Relay Lines", USDOT: "1234567", MC: "MC7654321",
	}); err != nil {
		t.Fatalf("RegisterCarrier: %v", err)
	}
	if _, err := svc.DeclareOperatingHours(ctx, "WH1", []usecases.HoursInput{
		{DoorCode: "WH1-DOCK-IN-01", Weekday: "MON", OpensAt: "06:00", ClosesAt: "22:00", Timezone: "UTC"},
	}); err != nil {
		t.Fatalf("DeclareOperatingHours: %v", err)
	}
	if n := pgtest.Count(t, pool, "outbox"); n != 2 {
		t.Fatalf("outbox rows after the use cases = %d, want 2", n)
	}

	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- run(runCtx, env(map[string]string{
			"DATABASE_URL":          dsn,
			"KAFKA_BROKERS":         kafkatest.Brokers(t)[0],
			"OUTBOX_RELAY_INTERVAL": "100ms",
			"OUTBOX_BATCH_SIZE":     "10",
			"OUTBOX_MAX_ATTEMPTS":   "5",
			"ADMIN_ADDR":            "127.0.0.1:0",
		}), quiet())
	}()

	events := kafkatest.ReadN(t, outboundkafka.TopicEvents, 1, 60*time.Second)
	analytics := kafkatest.ReadN(t, outboundkafka.TopicAnalytics, 2, 60*time.Second)

	registered := envelopeOf(t, events[0], "com.warehouse.wms.dock-scheduling.carrier.CarrierRegistered", "ITRL")
	if got := registered["dataschema"]; got != "urn:warehouse:dock-scheduling:events:CarrierRegistered:v1" {
		t.Fatalf("events dataschema = %v", got)
	}
	twin := envelopeOf(t, messageOfType(t, analytics, "com.warehouse.wms.dock-scheduling.carrier.CarrierRegistered"), "", "ITRL")
	if twin["id"] != registered["id"] {
		t.Fatalf("the analytics twin id %v differs from the events id %v", twin["id"], registered["id"])
	}
	hours := envelopeOf(t, messageOfType(t, analytics, "com.warehouse.wms.dock-scheduling.dockcalendar.DoorOperatingHoursDeclared"), "", "WH1")
	if got := hours["dataschema"]; got != "urn:warehouse:dock-scheduling:analytics:DoorOperatingHoursDeclared:v1" {
		t.Fatalf("analytics dataschema = %v", got)
	}
	if n := kafkatest.EndOffset(t, outboundkafka.TopicEvents); n != 1 {
		t.Fatalf("the analytics-only fact leaked to the events topic: end offset = %d, want 1", n)
	}

	deadline := time.Now().Add(30 * time.Second)
	for pgtest.Count(t, pool, "outbox") != 0 {
		if time.Now().After(deadline) {
			t.Fatal("the relayed rows were never deleted from the outbox")
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run = %v, want nil after the context was cancelled", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("run did not stop after the context was cancelled")
	}
}

// messageOfType returns the one message of analytics whose type matches.
func messageOfType(t *testing.T, msgs []kafkatest.Message, eventType string) kafkatest.Message {
	t.Helper()
	for _, m := range msgs {
		var envelope map[string]any
		if err := json.Unmarshal(m.Value, &envelope); err != nil {
			t.Fatalf("not a CloudEvents envelope: %v\n%s", err, m.Value)
		}
		if envelope["type"] == eventType {
			return m
		}
	}
	t.Fatalf("no %s among %d analytics messages", eventType, len(msgs))
	return kafkatest.Message{}
}

// envelopeOf decodes one message and checks the CloudEvents fields every
// published fact carries (ADR 0004): specversion, source, the Kafka key =
// subject, and — when wantType is not empty — the type. It returns the
// envelope for the caller's per-event assertions.
func envelopeOf(t *testing.T, m kafkatest.Message, wantType, wantSubject string) map[string]any {
	t.Helper()
	var envelope map[string]any
	if err := json.Unmarshal(m.Value, &envelope); err != nil {
		t.Fatalf("not a CloudEvents envelope: %v\n%s", err, m.Value)
	}
	for field, want := range map[string]string{
		"specversion": "1.0", "source": "/warehouse/dock-scheduling", "type": wantType, "subject": wantSubject,
	} {
		if want != "" && envelope[field] != want {
			t.Fatalf("%s = %v, want %q (%s)", field, envelope[field], want, m.Value)
		}
	}
	if m.Key != wantSubject {
		t.Fatalf("kafka key = %q, want the subject %q", m.Key, wantSubject)
	}
	return envelope
}
