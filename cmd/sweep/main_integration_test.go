//go:build integration

package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
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

	inboundhttp "github.com/claudioed/dock-scheduling/internal/adapters/inbound/http"
	outboundkafka "github.com/claudioed/dock-scheduling/internal/adapters/outbound/kafka"
	outboxrelay "github.com/claudioed/dock-scheduling/internal/adapters/outbound/outbox"
	"github.com/claudioed/dock-scheduling/internal/adapters/outbound/postgres"
	"github.com/claudioed/dock-scheduling/internal/adapters/outbound/system"
	"github.com/claudioed/dock-scheduling/internal/adapters/outbound/telemetry"
	"github.com/claudioed/dock-scheduling/internal/application/usecases"
	"github.com/claudioed/dock-scheduling/internal/testing/kafkatest"
	"github.com/claudioed/dock-scheduling/internal/testing/pgtest"
)

const noShowType = "com.warehouse.wms.dock-scheduling.dockappointment.DockAppointmentNoShow"

func TestMain(m *testing.M) {
	code := m.Run()
	kafkatest.Shutdown()
	pgtest.Shutdown()
	os.Exit(code)
}

// TestRunMainSweepsDueBookedAppointmentsToNoShow drives the REAL entrypoint
// (runner.runMain — the exact call main makes, per-attempt timeout, retries,
// Idempotency-Key) over real HTTP against the REST surface cmd/api serves, on
// a migrated testcontainers Postgres and a testcontainers Kafka. The state it
// sweeps is seeded through the REAL use cases (the consumers' effects for the
// local copies, BookAppointment for the appointment; only the window is then
// moved into the past, because the real clock cannot advance — the BDD suite
// fakes it, a deployment simply waits out the grace). Documented outcome (ADR
// 0002 A13, the run-no-show-sweep contract, charts/.../sweeps-cronjob.yaml):
// the CronJob's POST marks the overdue BOOKED appointment NO_SHOW, the
// DockAppointmentNoShow CloudEvent of ADR 0004 reaches the events topic with
// its analytics twin, and a second pass marks nothing new (naturally
// idempotent).
func TestRunMainSweepsDueBookedAppointmentsToNoShow(t *testing.T) {
	for _, topic := range outboundkafka.Topics() {
		kafkatest.CreateTopic(t, topic)
	}
	pool := pgtest.Open(t, pgtest.URL(t))
	svc := wireService(t, pool)
	ctx := context.Background()

	// The event-fed local copies through their real use cases (ADR 0003):
	// a zone, a Dock door and an ASN.
	seed(t,
		func() error { return svc.ConsumeZoneRegistered(ctx, "it-sweep-zone", "zone-it", "WH1") },
		func() error {
			return svc.ConsumeSlotRegistered(ctx, "it-sweep-door", "WH1-DOCK-IT-01", "zone-it", "Dock", "Both", "")
		},
		func() error {
			return svc.ConsumeAsnRegistered(ctx, "it-sweep-asn", "ASN-IT-1", time.Now().UTC().Add(24*time.Hour))
		},
	)

	// The REAL write use case books a genuine appointment (aggregate,
	// asn_claims and the DockAppointmentBooked outbox row in one transaction).
	tomorrow := time.Now().UTC().AddDate(0, 0, 1)
	windowStart := time.Date(tomorrow.Year(), tomorrow.Month(), tomorrow.Day(), 8, 0, 0, 0, time.UTC)
	booked, err := svc.BookAppointment(ctx, usecases.BookCommand{
		DoorCode: "WH1-DOCK-IT-01", Direction: "INBOUND", Carrier: "Integration Sweep Carrier", CarrierSCAC: "ITRL",
		WindowStart: windowStart, WindowEnd: windowStart.Add(2 * time.Hour), ASNNumbers: []string{"ASN-IT-1"},
	})
	if err != nil {
		t.Fatalf("BookAppointment: %v", err)
	}
	appointmentID := booked.AppointmentID

	// Move only the window into the past so the site's default 60-minute
	// no-show grace (ADR 0002) is over; a restored row may show a past window.
	now := time.Now().UTC()
	windowEnd := now.Add(-2 * time.Hour)
	if _, err := pool.Exec(ctx,
		`UPDATE dock_appointments SET window_start = $1, window_end = $2 WHERE appointment_id = $3`,
		now.Add(-3*time.Hour), windowEnd, appointmentID,
	); err != nil {
		t.Fatalf("move the booked window into the past: %v", err)
	}

	api := startRouter(t, svc, pool)
	startEmbeddedRelay(t, pool)
	lookup := env(map[string]string{
		"SWEEP_URL":             api.URL + "/no-show-sweeps",
		"SIDECAR_QUIT_URL":      "", // no mesh beside the test: empty disables the quit POST
		"SWEEP_REQUEST_TIMEOUT": "10s",
		"SWEEP_MAX_ATTEMPTS":    "2",
		"SWEEP_RETRY_BACKOFF":   "100ms",
		"SWEEP_MAX_BACKOFF":     "200ms",
	})
	r := runner{client: &http.Client{Timeout: 10 * time.Second}, logger: slog.New(slog.NewTextHandler(io.Discard, nil)), sleep: sleepCtx}
	if code := r.runMain(ctx, lookup); code != exitOK {
		t.Fatalf("runMain exit code = %d, want %d", code, exitOK)
	}

	// The documented transition: BOOKED -> NO_SHOW, version 2, terminal.
	after, err := svc.GetAppointment(ctx, appointmentID)
	if err != nil {
		t.Fatalf("GetAppointment: %v", err)
	}
	if after.State != "NO_SHOW" || after.Version != 2 {
		t.Fatalf("after the sweep the appointment is %s at version %d, want NO_SHOW at version 2", after.State, after.Version)
	}

	// The documented event: DockAppointmentNoShow on the events topic, twin
	// with the same id on the analytics topic (ADR 0004).
	events := kafkatest.ReadN(t, outboundkafka.TopicEvents, 2, 60*time.Second)
	analytics := kafkatest.ReadN(t, outboundkafka.TopicAnalytics, 2, 60*time.Second)
	noShow := envelopeOf(t, messageOfType(t, events, noShowType), noShowType, appointmentID)
	twin := envelopeOf(t, messageOfType(t, analytics, noShowType), "", appointmentID)
	if twin["id"] != noShow["id"] {
		t.Fatalf("the analytics twin id %v differs from the events id %v", twin["id"], noShow["id"])
	}
	data, _ := noShow["data"].(map[string]any)
	if data["appointment_id"] != appointmentID || data["door_code"] != "WH1-DOCK-IT-01" || data["direction"] != "INBOUND" {
		t.Fatalf("DockAppointmentNoShow data = %v", data)
	}
	if data["version"] != float64(2) {
		t.Fatalf("DockAppointmentNoShow version = %v, want 2", data["version"])
	}
	if windowEndJSON, _ := data["window_end"].(string); windowEndJSON != windowEnd.Format(time.RFC3339Nano) {
		t.Fatalf("DockAppointmentNoShow window_end = %v, want %s", data["window_end"], windowEnd.Format(time.RFC3339Nano))
	}
	waitForEmptyOutbox(t, pool)

	// A second pass marks nothing new and publishes nothing new: the sweep is
	// naturally idempotent (the run-no-show-sweep contract).
	if code := r.runMain(ctx, lookup); code != exitOK {
		t.Fatalf("second runMain exit code = %d, want %d", code, exitOK)
	}
	waitForEmptyOutbox(t, pool)
	if n := kafkatest.EndOffset(t, outboundkafka.TopicEvents); n != 2 {
		t.Fatalf("a second sweep published again: events end offset = %d, want 2", n)
	}
}

// wireService builds the REAL use cases over the pool with the same adapters
// cmd/api wires (buildAdapters), so every seed is a genuine transaction.
func wireService(t *testing.T, pool *pgxpool.Pool) *usecases.Service {
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

// startRouter serves the REST adapter cmd/api serves (the same NewRouter,
// use cases and Idempotency-Key middleware) on a real listener, so the sweep
// entrypoint drives the documented POST /no-show-sweeps over real HTTP.
func startRouter(t *testing.T, svc *usecases.Service, pool *pgxpool.Pool) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(inboundhttp.NewRouter(&inboundhttp.Server{
		Service: svc, Idempotency: postgres.NewIdempotencyStore(pool), UoW: postgres.NewUnitOfWork(pool), Now: time.Now,
	}))
	t.Cleanup(ts.Close)
	return ts
}

// startEmbeddedRelay runs the outbox relay loop the API process runs next to
// the router (ADR 0008 A22): the sweep's effect reaches Kafka exactly as in a
// deployment. stop cancels, awaits the loop and closes the Kafka writer.
func startEmbeddedRelay(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	sink := outboundkafka.NewRelaySink(kafkatest.Brokers(t)[0])
	relay := outboxrelay.NewOutboxRelay(postgres.NewOutboxStore(pool), sink, logger,
		outboxrelay.WithInterval(100*time.Millisecond), outboxrelay.WithBatchSize(10), outboxrelay.WithMaxAttempts(5))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- relay.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Error("the embedded relay did not stop after the context was cancelled")
		}
		if err := sink.Close(); err != nil {
			t.Logf("kafka sink close: %v", err)
		}
	})
}

func seed(t *testing.T, steps ...func() error) {
	t.Helper()
	for _, step := range steps {
		if err := step(); err != nil {
			t.Fatal(err)
		}
	}
}

// waitForEmptyOutbox bounds the wait for the relay to drain the outbox.
func waitForEmptyOutbox(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for pgtest.Count(t, pool, "outbox") != 0 {
		if time.Now().After(deadline) {
			t.Fatal("the outbox was never drained")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// messageOfType returns the one message of msgs whose CloudEvents type matches.
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
	t.Fatalf("no %s among %d messages", eventType, len(msgs))
	return kafkatest.Message{}
}

// envelopeOf decodes one message and checks the CloudEvents fields every
// published fact carries (ADR 0004): specversion, source, the Kafka key =
// subject and — when wantType is not empty — the type. It returns the decoded
// envelope for the caller's per-event assertions.
func envelopeOf(t *testing.T, m kafkatest.Message, wantType, wantSubject string) map[string]any {
	t.Helper()
	var envelope map[string]any
	if err := json.Unmarshal(m.Value, &envelope); err != nil {
		t.Fatalf("not a CloudEvents envelope: %v\n%s", err, m.Value)
	}
	checks := map[string]string{"specversion": "1.0", "source": "/warehouse/dock-scheduling"}
	if wantType != "" {
		checks["type"] = wantType
	}
	if wantSubject != "" {
		checks["subject"] = wantSubject
		if m.Key != wantSubject {
			t.Fatalf("kafka key = %q, want the subject %q", m.Key, wantSubject)
		}
	}
	for field, want := range checks {
		if envelope[field] != want {
			t.Fatalf("%s = %v, want %q (%s)", field, envelope[field], want, m.Value)
		}
	}
	return envelope
}
