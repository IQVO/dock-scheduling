//go:build integration

package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	tckafka "github.com/testcontainers/testcontainers-go/modules/kafka"

	outboundkafka "github.com/claudioed/dock-scheduling/internal/adapters/outbound/kafka"
	"github.com/claudioed/dock-scheduling/internal/adapters/outbound/postgres"
	"github.com/claudioed/dock-scheduling/internal/domain/shared"
	"github.com/claudioed/dock-scheduling/internal/testing/kafkatest"
	"github.com/claudioed/dock-scheduling/internal/testing/pgtest"
)

// The REAL composition root against REAL infrastructure, both from
// testcontainers: runWith reads the environment exactly as in production,
// migrates a fresh Postgres and serves MCP over Streamable HTTP. The client is
// the official SDK over the wire (plus raw POSTs for the stateless handshake);
// the assertions are the Postgres rows. The Kafka broker comes from
// internal/testing/kafkatest (testcontainers-go/modules/kafka, referenced here
// so the module import stays reachable from this Kafka-touching file).
var _ = tckafka.Run

const (
	door = "WH1-DOCK-IN-01"
	asn  = "ASN-IT-1"
	asn2 = "ASN-IT-2"
)

// server is a started cmd/mcp.
type server struct {
	base string
	stop context.CancelFunc
	done chan error
}

// startMCP boots runWith with the environment of a deployment and waits until
// the listener answers /healthz.
func startMCP(t *testing.T, databaseURL string, brokers []string) *server {
	t.Helper()
	env := map[string]string{
		"MCP_ADDR": "127.0.0.1:0", "DATABASE_URL": databaseURL, "KAFKA_BROKERS": strings.Join(brokers, ","),
		"SHUTDOWN_DRAIN_DELAY": "0s",
	}
	ctx, stop := context.WithCancel(context.Background())
	s := &server{stop: stop, done: make(chan error, 1)}
	bound := make(chan string, 1)
	go func() { s.done <- runWith(ctx, envOf(env), func(addr string) { bound <- addr }) }()
	t.Cleanup(func() {
		stop()
		select {
		case <-s.done:
		case <-time.After(60 * time.Second):
			t.Error("runWith did not return after the context was cancelled")
		}
	})
	select {
	case addr := <-bound:
		s.base = "http://" + addr
	case err := <-s.done:
		t.Fatalf("cmd/mcp exited during boot: %v", err)
	case <-time.After(90 * time.Second):
		t.Fatal("cmd/mcp did not start listening within 90s")
	}
	return s
}

func (s *server) status(t *testing.T, path string) int {
	t.Helper()
	resp, err := http.Get(s.base + path)
	if err != nil {
		return 0
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
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

func count(t *testing.T, databaseURL, sql string, args ...any) int {
	t.Helper()
	pool := pgtest.Open(t, databaseURL)
	var n int
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return n
}

// seedReferenceData fills the local copies the Kafka consumers fill in
// production: the facility layout's door and inbound-receiving's ASNs.
func seedReferenceData(t *testing.T, databaseURL string) {
	t.Helper()
	ctx := context.Background()
	pool := pgtest.Open(t, databaseURL)
	doors := postgres.NewDoorStore(pool)
	if err := doors.UpsertZone(ctx, "zone-WH1-in", "WH1"); err != nil {
		t.Fatal(err)
	}
	if err := doors.UpsertDoor(ctx, door, "zone-WH1-in", shared.DockFlow("Inbound")); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{asn, asn2} {
		if err := postgres.NewAsnStore(pool).Register(ctx, n, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
}

// journey is the state the steps of TestMCP_RealPostgres share.
type journey struct {
	srv         *server
	session     *sdk.ClientSession
	databaseURL string
	start, end  time.Time
	apptID      string
	version     float64
}

func (j *journey) callRaw(t *testing.T, name string, args map[string]any) *sdk.CallToolResult {
	t.Helper()
	res, err := j.session.CallTool(context.Background(), &sdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: protocol error: %v", name, err)
	}
	return res
}

// ok calls a tool, requires success and returns the structured content.
func (j *journey) ok(t *testing.T, name string, args map[string]any) map[string]any {
	t.Helper()
	res := j.callRaw(t, name, args)
	if res.IsError {
		t.Fatalf("%s failed: %s", name, toolText(res))
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("%s: structured content is not an object: %s", name, raw)
	}
	return out
}

// refused calls a tool, requires an isError result starting with slug.
func (j *journey) refused(t *testing.T, slug, name string, args map[string]any) {
	t.Helper()
	res := j.callRaw(t, name, args)
	if !res.IsError || !strings.HasPrefix(toolText(res), slug+":") {
		t.Fatalf("%s: want an isError result %q, got isError=%v %q", name, slug, res.IsError, toolText(res))
	}
}

func toolText(res *sdk.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*sdk.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

func (j *journey) bookArgs(key string, extra map[string]any) map[string]any {
	args := map[string]any{
		"direction": "INBOUND", "doorCode": door, "carrier": "ACME Freight", "carrierScac": "ACMF",
		"windowStart": j.start.Format(time.RFC3339), "windowEnd": j.end.Format(time.RFC3339),
		"asnNumbers": []string{asn}, "expectedSealNumber": "SEAL-77120",
		"idempotencyKey": key, "proposalId": "prop-1", "approvedBy": "planner-7", "agentId": "ops-agent",
	}
	for k, v := range extra {
		args[k] = v
	}
	return args
}

// TestMCP_RealPostgres boots the entry point once and drives it over HTTP:
// probes, the stateless handshake, the tool list with its authority levels,
// then a booking life with every row asserted in Postgres.
func TestMCP_RealPostgres(t *testing.T) {
	brokers := kafkatest.Brokers(t)
	kafkatest.CreateTopic(t, outboundkafka.TopicEvents)
	kafkatest.CreateTopic(t, outboundkafka.TopicAnalytics)
	databaseURL := pgtest.URL(t)
	seedReferenceData(t, databaseURL)
	srv := startMCP(t, databaseURL, brokers)
	waitFor(t, "/readyz to be ready", 30*time.Second, func() bool { return srv.status(t, "/readyz") == http.StatusOK })

	session, err := sdk.NewClient(&sdk.Implementation{Name: "it-client", Version: "0"}, nil).
		Connect(context.Background(), &sdk.StreamableClientTransport{Endpoint: srv.base + "/mcp"}, nil)
	if err != nil {
		t.Fatalf("connect over Streamable HTTP: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	start := time.Now().UTC().Add(48 * time.Hour).Truncate(time.Hour)
	j := &journey{srv: srv, session: session, databaseURL: databaseURL, start: start, end: start.Add(2 * time.Hour)}

	t.Run("probes answer and the MCP endpoint is on both paths", j.probes)
	t.Run("the stateless 2026-07-28 handshake is served and no session is issued", j.stateless)
	t.Run("tools/list carries nine tools, each with an authority level", j.tools)
	t.Run("a booking writes the aggregate, the outbox rows and the idempotency key", j.book)
	t.Run("the same key replays the stored answer and writes nothing", j.replay)
	t.Run("the same key with other arguments is refused", j.keyReused)
	t.Run("a domain refusal over Postgres is a typed tool error and writes nothing", j.overlap)
	t.Run("reads see the booking", j.reads)
	t.Run("report_eta, a stale expectedVersion and reschedule", j.etaAndReschedule)
	t.Run("cancel closes the appointment and enqueues the fact", j.cancel)
	t.Run("run returns nil within a bound once the context is cancelled", j.shutdown)
}

func (j *journey) probes(t *testing.T) {
	for _, path := range []string{"/healthz", "/readyz", "/metrics"} {
		if got := j.srv.status(t, path); got != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", path, got)
		}
	}
}

// rawPost sends one stateless JSON-RPC request (the 2026-07-28 shape).
func (j *journey) rawPost(t *testing.T, method string, params map[string]any) (*http.Response, map[string]any) {
	t.Helper()
	p := map[string]any{"_meta": map[string]any{
		"io.modelcontextprotocol/protocolVersion":    "2026-07-28",
		"io.modelcontextprotocol/clientCapabilities": map[string]any{},
	}}
	for k, v := range params {
		p[k] = v
	}
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": p})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, j.srv.base+"/mcp", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", "2026-07-28")
	req.Header.Set("Mcp-Method", method)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", method, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	payload := string(raw)
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		for _, line := range strings.Split(payload, "\n") {
			if data, ok := strings.CutPrefix(line, "data:"); ok {
				payload = strings.TrimSpace(data)
				break
			}
		}
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(payload), &out); err != nil {
		t.Fatalf("%s: not JSON (%v): %s", method, err, raw)
	}
	return resp, out
}

func (j *journey) stateless(t *testing.T) {
	resp, out := j.rawPost(t, "tools/list", nil)
	if resp.StatusCode != http.StatusOK || out["error"] != nil {
		t.Fatalf("tools/list with the 2026-07-28 headers = %d: %v", resp.StatusCode, out)
	}
	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		t.Errorf("Mcp-Session-Id = %q, want none (stateless)", sid)
	}
}

func (j *journey) tools(t *testing.T) {
	want := map[string]string{
		"list_appointments": "L1", "get_appointment": "L1", "get_door_availability": "L1", "get_dock_calendar": "L1",
		"list_at_risk_appointments": "L1",
		"book_appointment":          "L3", "reschedule_appointment": "L3", "cancel_appointment": "L3", "report_eta": "L3",
	}
	got := map[string]string{}
	for tool, err := range j.session.Tools(context.Background(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		level, _ := tool.Meta["com.warehouse/authority"].(string)
		got[tool.Name] = level
	}
	if len(got) != len(want) {
		t.Errorf("tools = %v, want %v", got, want)
	}
	for name, level := range want {
		if got[name] != level {
			t.Errorf("tool %s authority = %q, want %q", name, got[name], level)
		}
	}
}

func (j *journey) book(t *testing.T) {
	out := j.ok(t, "book_appointment", j.bookArgs("it-book-1", nil))
	appt, _ := out["appointment"].(map[string]any)
	j.apptID, _ = appt["appointmentId"].(string)
	j.version, _ = appt["version"].(float64)
	if !strings.HasPrefix(j.apptID, "appt-") || appt["state"] != "BOOKED" || appt["siteId"] != "WH1" || j.version != 1 {
		t.Fatalf("unexpected appointment: %v", appt)
	}
	by, _ := out["initiatedBy"].(map[string]any)
	if by["via"] != "mcp" || by["proposalId"] != "prop-1" || by["approvedBy"] != "planner-7" || by["agentId"] != "ops-agent" || out["replayed"] == true {
		t.Errorf("initiatedBy = %v replayed = %v, want via mcp and the three labels, not replayed", by, out["replayed"])
	}
	if n := count(t, j.databaseURL, `SELECT count(*) FROM dock_appointments WHERE appointment_id = $1 AND state = 'BOOKED' AND version = 1 AND door_code = $2 AND site_id = 'WH1'`, j.apptID, door); n != 1 {
		t.Errorf("dock_appointments rows = %d, want 1", n)
	}
	if n := count(t, j.databaseURL, `SELECT count(*) FROM outbox WHERE aggreg_id = $1 AND payload->>'event' = 'DockAppointmentBooked'`, j.apptID); n != 1 {
		t.Errorf("DockAppointmentBooked outbox rows = %d, want 1", n)
	}
	// The key is stored under the agent prefix, so it can never collide with a REST client's.
	if n := count(t, j.databaseURL, `SELECT count(*) FROM idempotency_keys WHERE key = 'mcp:it-book-1' AND status = 200`); n != 1 {
		t.Errorf("idempotency_keys rows for mcp:it-book-1 = %d, want 1", n)
	}
}

func (j *journey) replay(t *testing.T) {
	out := j.ok(t, "book_appointment", j.bookArgs("it-book-1", nil))
	appt, _ := out["appointment"].(map[string]any)
	if out["replayed"] != true || appt["appointmentId"] != j.apptID {
		t.Fatalf("replay = replayed %v appointment %v, want the stored answer for %s", out["replayed"], appt["appointmentId"], j.apptID)
	}
	if n := count(t, j.databaseURL, `SELECT count(*) FROM dock_appointments`); n != 1 {
		t.Errorf("dock_appointments rows after the replay = %d, want 1", n)
	}
	if n := count(t, j.databaseURL, `SELECT count(*) FROM outbox WHERE aggreg_id = $1`, j.apptID); n != 1 {
		t.Errorf("outbox rows after the replay = %d, want 1", n)
	}
}

func (j *journey) keyReused(t *testing.T) {
	j.refused(t, "idempotency-key-reused", "book_appointment", j.bookArgs("it-book-1", map[string]any{"carrier": "Other Freight"}))
}

func (j *journey) overlap(t *testing.T) {
	j.refused(t, "door-double-booked", "book_appointment", j.bookArgs("it-book-overlap", map[string]any{"asnNumbers": []string{asn2}}))
	if n := count(t, j.databaseURL, `SELECT count(*) FROM dock_appointments`); n != 1 {
		t.Errorf("dock_appointments rows after the refused overlap = %d, want 1", n)
	}
}

func (j *journey) reads(t *testing.T) {
	got := j.ok(t, "get_appointment", map[string]any{"appointmentId": j.apptID})
	if got["state"] != "BOOKED" || got["doorCode"] != door {
		t.Errorf("get_appointment = %v", got)
	}
	page := j.ok(t, "list_appointments", map[string]any{"siteId": "WH1", "state": "BOOKED"})
	if items, _ := page["items"].([]any); len(items) != 1 {
		t.Errorf("list_appointments = %v, want the one booking", page)
	}
	atRisk := j.ok(t, "list_at_risk_appointments", map[string]any{"siteId": "WH1"})
	if items, _ := atRisk["items"].([]any); len(items) != 0 {
		t.Errorf("list_at_risk_appointments = %v, want none", atRisk)
	}
	avail := j.ok(t, "get_door_availability", map[string]any{
		"doorCode": door, "from": j.start.Add(-time.Hour).Format(time.RFC3339), "to": j.end.Add(time.Hour).Format(time.RFC3339),
	})
	if busy, _ := avail["busy"].([]any); len(busy) != 1 {
		t.Errorf("get_door_availability busy = %v, want the booked window", avail["busy"])
	}
	if cal := j.ok(t, "get_dock_calendar", map[string]any{"siteId": "WH1"}); cal["siteId"] != "WH1" {
		t.Errorf("get_dock_calendar = %v", cal)
	}
}

func (j *journey) etaAndReschedule(t *testing.T) {
	eta := j.ok(t, "report_eta", map[string]any{
		"appointmentId": j.apptID, "etaAt": j.start.Format(time.RFC3339), "idempotencyKey": "it-eta-1", "agentId": "ops-agent",
	})
	if appt, _ := eta["appointment"].(map[string]any); appt["eta"] == nil {
		t.Errorf("report_eta left no ETA on the appointment: %v", appt)
	}
	// The ETA bumped the version, so version 1 is stale now.
	j.refused(t, "precondition-failed", "reschedule_appointment", map[string]any{
		"appointmentId": j.apptID, "windowStart": j.start.Add(4 * time.Hour).Format(time.RFC3339),
		"windowEnd": j.end.Add(4 * time.Hour).Format(time.RFC3339), "expectedVersion": 1, "idempotencyKey": "it-resched-stale",
	})
	current := j.ok(t, "get_appointment", map[string]any{"appointmentId": j.apptID})
	j.version, _ = current["version"].(float64)
	moved := j.ok(t, "reschedule_appointment", map[string]any{
		"appointmentId": j.apptID, "windowStart": j.start.Add(4 * time.Hour).Format(time.RFC3339),
		"windowEnd": j.end.Add(4 * time.Hour).Format(time.RFC3339), "expectedVersion": j.version, "idempotencyKey": "it-resched-1",
	})
	if appt, _ := moved["appointment"].(map[string]any); appt["version"].(float64) <= j.version {
		t.Errorf("reschedule did not advance the version: %v", appt)
	}
	if n := count(t, j.databaseURL, `SELECT count(*) FROM outbox WHERE aggreg_id = $1 AND payload->>'event' = 'DockAppointmentRescheduled'`, j.apptID); n != 1 {
		t.Errorf("DockAppointmentRescheduled outbox rows = %d, want 1", n)
	}
}

func (j *journey) cancel(t *testing.T) {
	j.refused(t, "invalid-reason", "cancel_appointment", map[string]any{"appointmentId": j.apptID, "reason": "TURNED_AWAY", "idempotencyKey": "it-cancel-0"})
	out := j.ok(t, "cancel_appointment", map[string]any{"appointmentId": j.apptID, "reason": "CARRIER_REQUEST", "idempotencyKey": "it-cancel-1"})
	if appt, _ := out["appointment"].(map[string]any); appt["state"] != "CANCELLED" {
		t.Fatalf("cancel = %v", appt)
	}
	if n := count(t, j.databaseURL, `SELECT count(*) FROM dock_appointments WHERE appointment_id = $1 AND state = 'CANCELLED' AND cancellation_reason = 'CARRIER_REQUEST'`, j.apptID); n != 1 {
		t.Errorf("cancelled rows = %d, want 1", n)
	}
	if n := count(t, j.databaseURL, `SELECT count(*) FROM outbox WHERE aggreg_id = $1 AND payload->>'event' = 'DockAppointmentCancelled'`, j.apptID); n != 1 {
		t.Errorf("DockAppointmentCancelled outbox rows = %d, want 1", n)
	}
	// This binary starts no relay: the facts wait in the outbox for cmd/api / cmd/relay.
	if n := kafkatest.EndOffset(t, outboundkafka.TopicEvents); n != 0 {
		t.Errorf("the events topic holds %d messages, want 0 (cmd/mcp publishes nothing itself)", n)
	}
}

func (j *journey) shutdown(t *testing.T) {
	j.srv.stop()
	select {
	case err := <-j.srv.done:
		if err != nil {
			t.Errorf("runWith = %v, want nil on a clean stop", err)
		}
	case <-time.After(60 * time.Second):
		t.Fatal("runWith did not return within 60s")
	}
	j.srv.done <- nil // the cleanup waits for one more value
	if got := j.srv.status(t, "/healthz"); got != 0 {
		t.Errorf("GET /healthz after the stop = %d, want a refused connection", got)
	}
}
