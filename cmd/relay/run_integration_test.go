//go:build integration

package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/claudioed/dock-scheduling/internal/adapters/kafka/cloudevents"
	outboundkafka "github.com/claudioed/dock-scheduling/internal/adapters/outbound/kafka"
	"github.com/claudioed/dock-scheduling/internal/adapters/outbound/postgres"
	"github.com/claudioed/dock-scheduling/internal/adapters/outbound/system"
	"github.com/claudioed/dock-scheduling/internal/adapters/outbound/telemetry"
	"github.com/claudioed/dock-scheduling/internal/application/usecases"
	"github.com/claudioed/dock-scheduling/internal/domain/shared"
	"github.com/claudioed/dock-scheduling/internal/testing/kafkatest"
	"github.com/claudioed/dock-scheduling/internal/testing/pgtest"
)

// TestRunDrainsTheOutboxToBothKafkaTopicsAndShutsDownCleanly boots the real
// relay run() against a migrated testcontainers Postgres and a real Kafka: a
// booking made through the real use case (the same Deps cmd/api wires in
// wire.go, over the same pool) leaves its CloudEvents row in the outbox, the
// relay drains it to the events topic AND the analytics topic with the exact
// envelope of ADR 0004 (including the door GLN ADR 0013 resolves at enqueue
// time), GET /healthz answers while it runs, and a cancelled context is the
// clean exit.
func TestRunDrainsTheOutboxToBothKafkaTopicsAndShutsDownCleanly(t *testing.T) {
	ctx := context.Background()
	url := pgtest.URL(t) // the schema belongs to the API's boot; the relay never migrates
	pool := pgtest.Open(t, url)
	defer pool.Close()

	// The reference data the consumers would have folded: a door (with its
	// ADR 0045 GLN) and a known ASN, written through the same stores the
	// Kafka effects use.
	doors := postgres.NewDoorStore(pool)
	if err := doors.UpsertZone(ctx, "zone-it-relay", "WH1"); err != nil {
		t.Fatal(err)
	}
	const relayDoor, relayGLN, relayASN = "WH1-DOCK-IT-RELAY", "4006381333931", "ASN-IT-RELAY"
	if err := doors.UpsertDoor(ctx, relayDoor, "zone-it-relay", shared.FlowBoth, relayGLN); err != nil {
		t.Fatal(err)
	}
	if err := postgres.NewAsnStore(pool).Register(ctx, relayASN, time.Now().UTC().Add(48*time.Hour)); err != nil {
		t.Fatal(err)
	}

	// The real use cases over the real adapters (wire.go's Deps, verbatim):
	// booking this way is the production path minus the HTTP edge, and it
	// commits the aggregate and its outbox row in ONE transaction (ADR 0005).
	metrics, err := telemetry.New()
	if err != nil {
		t.Fatal(err)
	}
	svc := usecases.New(usecases.Deps{
		Appointments: postgres.NewAppointmentRepo(pool), Calendars: postgres.NewCalendarRepo(pool), Carriers: postgres.NewCarrierRepo(pool),
		Doors: postgres.NewDoorStore(pool), Asns: postgres.NewAsnStore(pool), Cutoffs: postgres.NewCutoffStore(pool),
		Processed: postgres.NewProcessedEventRepo(pool), Events: postgres.NewOutboxPublisher(pool), Metrics: metrics,
		IDs: system.IDs{}, Clock: system.Clock{}, UoW: postgres.NewUnitOfWork(pool),
	})
	windowStart := time.Now().UTC().Add(48 * time.Hour).Truncate(time.Hour)
	view, err := svc.BookAppointment(ctx, usecases.BookCommand{
		DoorCode: relayDoor, Direction: "INBOUND", Carrier: "ACME Freight", CarrierSCAC: "ACMF",
		WindowStart: windowStart, WindowEnd: windowStart.Add(time.Hour), ASNNumbers: []string{relayASN},
	})
	if err != nil {
		t.Fatalf("book through the real use case: %v", err)
	}
	if got := pgtest.Count(t, pool, "outbox"); got != 1 {
		t.Fatalf("outbox rows after the booking = %d, want 1", got)
	}

	// The broker has auto-creation OFF: both topics the relay writes to must
	// exist before the drain (cmd/api's journey does the same).
	brokers := kafkatest.Brokers(t)
	kafkatest.CreateTopic(t, outboundkafka.TopicEvents)
	kafkatest.CreateTopic(t, outboundkafka.TopicAnalytics)

	adminAddr := freeAdminPort(t)
	getenv := env(map[string]string{
		"DATABASE_URL": url, "KAFKA_BROKERS": brokers[0],
		"OUTBOX_RELAY_INTERVAL": "100ms", "ADMIN_ADDR": adminAddr,
	})
	runCtx, cancel := context.WithCancel(ctx)
	finished := make(chan error, 1)
	go func() { finished <- run(runCtx, getenv, quiet()) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-finished:
		case <-time.After(30 * time.Second):
			t.Error("run did not return after the context was cancelled")
		}
	})

	waitFor(t, "the relay to answer /healthz", 30*time.Second, func() bool {
		resp, err := http.Get("http://" + adminAddr + "/healthz")
		if err != nil {
			return false
		}
		defer func() { _ = resp.Body.Close() }()
		_, _ = io.Copy(io.Discard, resp.Body)
		return resp.StatusCode == http.StatusOK
	})
	waitFor(t, "the relay to drain the outbox", 90*time.Second, func() bool {
		return pgtest.Count(t, pool, "outbox") == 0
	})

	assertDrained := func(topic, stream string) {
		t.Helper()
		msgs := kafkatest.ReadN(t, topic, 1, 60*time.Second)
		msg := msgs[0]
		if msg.Key != view.AppointmentID {
			t.Errorf("%s: key = %q, want the appointment id %q", topic, msg.Key, view.AppointmentID)
		}
		if len(msg.Headers) != 1 || msg.Headers["content-type"] != cloudevents.MediaType {
			t.Errorf("%s: headers = %v, want exactly the content-type %q", topic, msg.Headers, cloudevents.MediaType)
		}
		if _, err := cloudevents.Decode(msg.Value); err != nil {
			t.Fatalf("%s: not a valid CloudEvent: %v\n%s", topic, err, msg.Value)
		}
		var env struct {
			Specversion string         `json:"specversion"`
			Source      string         `json:"source"`
			Type        string         `json:"type"`
			Subject     string         `json:"subject"`
			Dataschema  string         `json:"dataschema"`
			Data        map[string]any `json:"data"`
		}
		if err := json.Unmarshal(msg.Value, &env); err != nil {
			t.Fatalf("%s: decode the drained event: %v", topic, err)
		}
		wantType := "com.warehouse.wms.dock-scheduling.dockappointment.DockAppointmentBooked"
		wantSchema := "urn:warehouse:dock-scheduling:" + stream + ":DockAppointmentBooked:v1"
		if env.Specversion != "1.0" || env.Source != "/warehouse/dock-scheduling" || env.Type != wantType ||
			env.Subject != view.AppointmentID || env.Dataschema != wantSchema {
			t.Errorf("%s: envelope = %s %s %s %s %s", topic, env.Specversion, env.Source, env.Type, env.Subject, env.Dataschema)
		}
		if env.Data["appointment_id"] != view.AppointmentID || env.Data["read_point_gln"] != relayGLN || env.Data["x12_status"] != "AB" {
			t.Errorf("%s: data = %v, want the booked appointment with its door's GLN and the AB status", topic, env.Data)
		}
		if end := kafkatest.EndOffset(t, topic); end != 1 {
			t.Errorf("%s holds %d messages, want exactly the drained one", topic, end)
		}
	}
	assertDrained(outboundkafka.TopicEvents, "events")
	assertDrained(outboundkafka.TopicAnalytics, "analytics")

	cancel()
	select {
	case err := <-finished:
		if err != nil {
			t.Fatalf("a cancelled context is the clean exit, got %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the relay did not stop after its context ended")
	}
	finished <- nil // the cleanup waits for one more value
}

// freeAdminPort reserves a loopback port for ADMIN_ADDR.
func freeAdminPort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	return ln.Addr().String()
}

// waitFor polls cond every 200ms until it holds or the deadline passes.
func waitFor(t *testing.T, what string, deadline time.Duration, cond func() bool) {
	t.Helper()
	end := time.Now().Add(deadline)
	for time.Now().Before(end) {
		if cond() {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
