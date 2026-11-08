//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	tckafka "github.com/testcontainers/testcontainers-go/modules/kafka"

	"github.com/claudioed/dock-scheduling/internal/adapters/kafka/cloudevents"
	outboundkafka "github.com/claudioed/dock-scheduling/internal/adapters/outbound/kafka"
	"github.com/claudioed/dock-scheduling/internal/testing/kafkatest"
	"github.com/claudioed/dock-scheduling/internal/testing/pgtest"
)

// The REAL composition root against REAL infrastructure, both from
// testcontainers: run(ctx) reads the environment exactly as in production,
// migrates a fresh Postgres, runs all five Kafka consumers, serves the REST
// router and drains the outbox to a real broker. It is driven only through REST
// and Kafka: the doors, zones and ASNs, the gate arrival, the dock and the door
// release, a rejected visit and an out-of-order release all arrive as
// CloudEvents on the producers' topics, nothing is seeded into Postgres. The
// assertions are the REST views, the Postgres rows and the exact CloudEvents
// on both topics. The Kafka broker comes from internal/testing/kafkatest,
// which starts it through testcontainers-go/modules/kafka (referenced here so
// the module import stays reachable from this Kafka-touching file).
var _ = tckafka.Run

const (
	door     = "WH1-DOCK-IN-01"
	asn      = "ASN-IT-1"
	problems = "https://errors.dock-scheduling.warehouse-systems.dev/"
)

var uuidRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// api is a started cmd/api.
type api struct {
	base string
	stop context.CancelFunc
	done chan error
}

// freeAddr reserves a loopback port for HTTP_ADDR.
func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	return ln.Addr().String()
}

// startAPI boots run(ctx) with the environment of a deployment and waits until
// the listener answers.
func startAPI(t *testing.T, databaseURL string, brokers []string) *api {
	t.Helper()
	addr := freeAddr(t)
	t.Setenv("HTTP_ADDR", addr)
	t.Setenv("DATABASE_URL", databaseURL)
	t.Setenv("KAFKA_BROKERS", strings.Join(brokers, ","))
	t.Setenv("OUTBOX_RELAY_INTERVAL", "200ms")
	t.Setenv("SHUTDOWN_DRAIN_DELAY", "0s")
	consumerEnv(t)
	ctx, stop := context.WithCancel(context.Background())
	a := &api{base: "http://" + addr, stop: stop, done: make(chan error, 1)}
	go func() { a.done <- run(ctx) }()
	t.Cleanup(func() {
		stop()
		select {
		case <-a.done:
		case <-time.After(60 * time.Second):
			t.Error("run did not return after the context was cancelled")
		}
	})
	waitFor(t, "the API to answer /healthz", 60*time.Second, func() bool {
		status, _, _ := a.do(t, "GET", "/healthz", nil, nil)
		return status == http.StatusOK
	})
	return a
}

// do sends one request and returns the status, headers and body; transport
// errors read as status 0 so callers can poll.
func (a *api) do(t *testing.T, method, path string, body any, headers map[string]string) (int, http.Header, []byte) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, a.base+path, rd)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, nil
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, raw
}

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

func decode(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("not a JSON object: %v\n%s", err, raw)
	}
	return m
}

// envelope is the CloudEvents a published fact must be, with the two fields the
// service mints (id, time) checked separately by assertMinted.
func envelope(eventType, subject, stream, eventName string, data map[string]any) map[string]any {
	return map[string]any{
		"specversion":     "1.0",
		"source":          "/warehouse/dock-scheduling",
		"type":            "com.warehouse.wms.dock-scheduling." + eventType,
		"subject":         subject,
		"datacontenttype": "application/json",
		"dataschema":      "urn:warehouse:dock-scheduling:" + stream + ":" + eventName + ":v1",
		"data":            data,
	}
}

// assertEnvelope checks one consumed message against the expected envelope:
// the Kafka key, exactly the CloudEvents content-type header, a valid
// CloudEvents 1.0 value, a UUID id, a time inside [from, to], and every other
// attribute and the whole data object exactly.
func assertEnvelope(t *testing.T, msg kafkatest.Message, wantKey string, want map[string]any, from, to time.Time) time.Time {
	t.Helper()
	name := fmt.Sprint(want["type"])
	if msg.Key != wantKey {
		t.Errorf("%s: key = %q, want %q", name, msg.Key, wantKey)
	}
	if len(msg.Headers) != 1 || msg.Headers["content-type"] != cloudevents.MediaType {
		t.Errorf("%s: headers = %v, want exactly the content-type %q", name, msg.Headers, cloudevents.MediaType)
	}
	if _, err := cloudevents.Decode(msg.Value); err != nil {
		t.Fatalf("%s: not a valid CloudEvent: %v\n%s", name, err, msg.Value)
	}
	got := decode(t, msg.Value)
	if id, _ := got["id"].(string); !uuidRE.MatchString(id) {
		t.Errorf("%s: id = %v, want a lowercase UUID", name, got["id"])
	}
	occurred, err := time.Parse(time.RFC3339, fmt.Sprint(got["time"]))
	if err != nil || occurred.Before(from) || occurred.After(to) {
		t.Errorf("%s: time = %v, want an instant within [%s, %s]", name, got["time"], from, to)
	}
	delete(got, "id")
	delete(got, "time")
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s: envelope differs\n got: %v\nwant: %v", name, got, want)
	}
	return occurred
}

// count runs a single-value count query on the database.
func count(t *testing.T, databaseURL, sql string, args ...any) int {
	t.Helper()
	pool := pgtest.Open(t, databaseURL)
	var n int
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return n
}

// journey is the state the steps of TestAPI_RealPostgresAndKafka share.
type journey struct {
	a           *api
	databaseURL string
	body        map[string]any
	windowStart time.Time
	windowEnd   time.Time
	from        time.Time
	apptID      string
	// the appointments the Kafka-fed steps drive
	completed, turnedAway, implicit, asnCancelled, legacy string
}

// TestAPI_RealPostgresAndKafka boots the entry point once and walks the
// service through its life: rows in Postgres, exact CloudEvents on Kafka, an
// idempotent replay that changes nothing, then the broker going away.
func TestAPI_RealPostgresAndKafka(t *testing.T) {
	brokers := kafkatest.Brokers(t)
	kafkatest.CreateTopic(t, outboundkafka.TopicEvents)
	// The broker has auto-creation OFF, so the analytics topic must exist before the drain.
	kafkatest.CreateTopic(t, outboundkafka.TopicAnalytics)
	createConsumedTopics(t)
	databaseURL := pgtest.URL(t)
	a := startAPI(t, databaseURL, brokers)

	waitFor(t, "/readyz to be ready", 30*time.Second, func() bool {
		status, _, _ := a.do(t, "GET", "/readyz", nil, nil)
		return status == http.StatusOK
	})

	start := time.Now().UTC().Add(48 * time.Hour).Truncate(time.Hour)
	j := &journey{
		a: a, databaseURL: databaseURL, windowStart: start, windowEnd: start.Add(2 * time.Hour), from: time.Now().UTC().Add(-time.Second),
		body: map[string]any{
			"direction": "INBOUND", "doorCode": door, "carrier": "ACME Freight", "carrierScac": "ACMF",
			"windowStart": start.Format(time.RFC3339), "windowEnd": start.Add(2 * time.Hour).Format(time.RFC3339),
			"asnNumbers": []string{asn}, "expectedSealNumber": "SEAL-77120",
		},
	}
	t.Run("the facility layout and the ASNs arrive through Kafka (a door before its zone is held pending)", j.referenceData)
	t.Run("a booking writes the aggregate and the outbox rows", j.book)
	t.Run("the same Idempotency-Key replays the answer and changes nothing", j.replay)
	t.Run("a stale If-Match is refused and a fresh one cancels", j.cancel)
	t.Run("an empty no-show sweep answers 200", j.sweep)
	t.Run("the schedule-adherence report is not served by the OLTP API", j.report)
	t.Run("the relay publishes the exact CloudEvents to both topics and the outbox drains", j.publishedEvents)
	t.Run("yard, ASN and legacy facts through Kafka drive the appointments", j.facts)
	t.Run("the fact-driven transitions publish their events", j.factEvents)
	t.Run("the analytics projection counts the arrivals the service published", j.adherence)
	t.Run("stopping Kafka turns /readyz into a dependency-unavailable problem", j.kafkaOutage)
	t.Run("run returns nil within a bound once the context is cancelled, broker or not", j.shutdown)
}

func (j *journey) book(t *testing.T) {
	status, header, body := j.a.do(t, "POST", "/appointments", j.body, map[string]string{"Idempotency-Key": "it-book-1"})
	if status != http.StatusCreated {
		t.Fatalf("POST /appointments = %d: %s", status, body)
	}
	view := decode(t, body)
	j.apptID, _ = view["appointmentId"].(string)
	if !strings.HasPrefix(j.apptID, "appt-") || view["state"] != "BOOKED" || view["siteId"] != "WH1" || view["version"] != float64(1) {
		t.Fatalf("unexpected view: %s", body)
	}
	if header.Get("ETag") != `W/"1"` || header.Get("Location") != "/appointments/"+j.apptID {
		t.Errorf("ETag = %q, Location = %q", header.Get("ETag"), header.Get("Location"))
	}
	if n := count(t, j.databaseURL, `SELECT count(*) FROM dock_appointments WHERE appointment_id = $1 AND state = 'BOOKED' AND version = 1 AND door_code = $2 AND site_id = 'WH1'`, j.apptID, door); n != 1 {
		t.Errorf("dock_appointments rows for the booking = %d, want 1", n)
	}
	if n := count(t, j.databaseURL, `SELECT count(*) FROM idempotency_keys WHERE key = 'it-book-1' AND status = 201`); n != 1 {
		t.Errorf("idempotency_keys rows = %d, want 1", n)
	}
}

func (j *journey) replay(t *testing.T) {
	status, header, body := j.a.do(t, "POST", "/appointments", j.body, map[string]string{"Idempotency-Key": "it-book-1"})
	if status != http.StatusCreated || header.Get("Idempotency-Replayed") != "true" {
		t.Fatalf("replay = %d replayed=%q: %s", status, header.Get("Idempotency-Replayed"), body)
	}
	if got := decode(t, body)["appointmentId"]; got != j.apptID {
		t.Errorf("the replay created %v, want %s", got, j.apptID)
	}
	if n := count(t, j.databaseURL, `SELECT count(*) FROM dock_appointments`); n != 1 {
		t.Errorf("dock_appointments rows after the replay = %d, want 1", n)
	}
}

func (j *journey) cancel(t *testing.T) {
	path := "/appointments/" + j.apptID + "/cancellations"
	reason := map[string]string{"reason": "CARRIER_REQUEST"}
	status, _, body := j.a.do(t, "POST", path, reason, map[string]string{"Idempotency-Key": "it-cancel-0", "If-Match": `W/"9"`})
	if status != http.StatusPreconditionFailed || decode(t, body)["type"] != problems+"precondition-failed" {
		t.Fatalf("stale If-Match = %d: %s", status, body)
	}
	status, header, body := j.a.do(t, "POST", path, reason, map[string]string{"Idempotency-Key": "it-cancel-1", "If-Match": `W/"1"`})
	if status != http.StatusOK || header.Get("ETag") != `W/"2"` {
		t.Fatalf("cancel = %d etag=%q: %s", status, header.Get("ETag"), body)
	}
	if n := count(t, j.databaseURL, `SELECT count(*) FROM dock_appointments WHERE appointment_id = $1 AND state = 'CANCELLED' AND cancellation_reason = 'CARRIER_REQUEST' AND version = 2`, j.apptID); n != 1 {
		t.Errorf("cancelled rows = %d, want 1", n)
	}
}

func (j *journey) sweep(t *testing.T) {
	status, _, body := j.a.do(t, "POST", "/no-show-sweeps", nil, map[string]string{"Idempotency-Key": "it-sweep-1"})
	if status != http.StatusOK {
		t.Fatalf("POST /no-show-sweeps = %d: %s", status, body)
	}
}

func (j *journey) report(t *testing.T) {
	// The reports read the separate analytical database and are served by
	// cmd/reports (ADR 0006): the OLTP API must not answer them, least of all
	// with an empty report.
	q := fmt.Sprintf("/reports/schedule-adherence?siteId=WH1&from=%s&to=%s",
		j.windowStart.Add(-24*time.Hour).Format(time.RFC3339), j.windowEnd.Add(24*time.Hour).Format(time.RFC3339))
	if status, _, body := j.a.do(t, "GET", q, nil, nil); status != http.StatusNotFound {
		t.Fatalf("GET report on the OLTP API = %d, want 404 (served by cmd/reports): %s", status, body)
	}
}

func (j *journey) publishedEvents(t *testing.T) {
	bookedData := map[string]any{
		"appointment_id": j.apptID, "site_id": "WH1", "direction": "INBOUND", "door_code": door,
		"carrier": "ACME Freight", "carrier_scac": "ACMF",
		"window_start": j.windowStart.Format(time.RFC3339), "window_end": j.windowEnd.Format(time.RFC3339),
		"asn_numbers": []any{asn}, "cpt_id": nil, "shipment_refs": []any{},
		"expected_seal_number": "SEAL-77120", "arrival_tolerance_minutes": float64(15), "provenance": "native", "version": float64(1),
	}
	cancelledData := map[string]any{
		"appointment_id": j.apptID, "site_id": "WH1", "door_code": door, "reason": "CARRIER_REQUEST", "version": float64(2),
	}
	booked := func(stream string, data map[string]any) map[string]any {
		return envelope("dockappointment.DockAppointmentBooked", j.apptID, stream, "DockAppointmentBooked", data)
	}
	cancelled := func(stream string, data map[string]any) map[string]any {
		return envelope("dockappointment.DockAppointmentCancelled", j.apptID, stream, "DockAppointmentCancelled", data)
	}
	until := time.Now().UTC().Add(time.Minute)
	events := kafkatest.ReadN(t, outboundkafka.TopicEvents, 2, 60*time.Second)
	bookedAt := assertEnvelope(t, events[0], j.apptID, booked("events", bookedData), j.from, until)
	cancelledAt := assertEnvelope(t, events[1], j.apptID, cancelled("events", cancelledData), j.from, until)
	if cancelledAt.Before(bookedAt) {
		t.Errorf("cancelled at %s before booked at %s", cancelledAt, bookedAt)
	}
	analytics := kafkatest.ReadN(t, outboundkafka.TopicAnalytics, 2, 60*time.Second)
	assertEnvelope(t, analytics[0], j.apptID, booked("analytics", bookedData), j.from, until)
	assertEnvelope(t, analytics[1], j.apptID, cancelled("analytics", cancelledData), j.from, until)

	// The replay, the stale If-Match and the sweep published nothing more.
	if n := kafkatest.EndOffset(t, outboundkafka.TopicEvents); n != 2 {
		t.Errorf("the events topic holds %d messages, want exactly 2", n)
	}
	waitFor(t, "the outbox to drain", 15*time.Second, func() bool { return count(t, j.databaseURL, `SELECT count(*) FROM outbox`) == 0 })
}

func (j *journey) kafkaOutage(t *testing.T) {
	if status, _, body := j.a.do(t, "GET", "/readyz", nil, nil); status != http.StatusOK {
		t.Fatalf("/readyz before the outage = %d: %s", status, body)
	}
	kafkatest.Stop(t)
	var header http.Header
	var body []byte
	// The verdict is cached for 3s and bounded by 2s: poll past both.
	waitFor(t, "/readyz to report the outage", 30*time.Second, func() bool {
		var status int
		status, header, body = j.a.do(t, "GET", "/readyz", nil, nil)
		return status == http.StatusServiceUnavailable
	})
	if ct := header.Get("Content-Type"); ct != "application/problem+json" {
		t.Errorf("content type = %q, want application/problem+json", ct)
	}
	problem := decode(t, body)
	if problem["type"] != problems+"dependency-unavailable" || problem["status"] != float64(503) {
		t.Errorf("problem = %s", body)
	}
	if detail, _ := problem["detail"].(string); !strings.Contains(detail, "kafka") || strings.Contains(detail, "postgres") {
		t.Errorf("detail = %q, want it to name kafka only", detail)
	}
	// Liveness is not readiness: the process is alive and Postgres still serves reads.
	if status, _, body := j.a.do(t, "GET", "/healthz", nil, nil); status != http.StatusOK {
		t.Errorf("/healthz during the outage = %d: %s", status, body)
	}
	if status, _, body := j.a.do(t, "GET", "/appointments/"+j.apptID, nil, nil); status != http.StatusOK {
		t.Errorf("GET appointment during the outage = %d: %s", status, body)
	}
}

func (j *journey) shutdown(t *testing.T) {
	j.a.stop()
	select {
	case err := <-j.a.done:
		if err != nil {
			t.Errorf("run = %v, want nil on a clean stop", err)
		}
	case <-time.After(60 * time.Second):
		t.Fatal("run did not return within 60s")
	}
	j.a.done <- nil // the cleanup waits for one more value
}
