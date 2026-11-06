// Package features_test hosts the godog (Cucumber for Go) acceptance suite
// next to the .feature files it runs (no Go file lives at the repo root). It
// drives the REAL chi router over HTTP and the REAL use cases over the
// in-memory doubles, wired the way cmd/api wires them over Postgres and
// Kafka. The yard and ASN facts reach the use cases the way the Kafka
// consumers (a later step) will hand them over: through the Consume* methods
// of the same Service. The storage invariants the doubles enforce are the
// ones the Postgres adapters prove in the repocontract suite.
package features_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/cucumber/godog"

	inboundhttp "github.com/claudioed/dock-scheduling/internal/adapters/inbound/http"
	"github.com/claudioed/dock-scheduling/internal/adapters/outbound/memory"
	"github.com/claudioed/dock-scheduling/internal/application/usecases"
)

// TestFeatures runs every Gherkin feature in this directory.
func TestFeatures(t *testing.T) {
	suite := godog.TestSuite{
		ScenarioInitializer: InitializeScenario,
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"."},
			Strict:   true,
			TestingT: t,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("non-zero status returned, failed to run feature tests")
	}
}

// scenarioDay is the calendar day every HH:MM of a scenario refers to (a Monday).
const scenarioDay = "2030-01-07"

// wall is the scenario's mutable service clock.
type wall struct{ now time.Time }

func (c *wall) Now() time.Time { return c.now }

// sequentialIDs mints appt-00000001-..., clo-00000001-... so scenarios have a
// stable order of ids.
type sequentialIDs struct{ appt, closure int }

func (f *sequentialIDs) NewAppointmentID() string {
	f.appt++
	return fmt.Sprintf("appt-%08x-e89b-12d3-a456-426614174000", f.appt)
}

func (f *sequentialIDs) NewClosureID() string {
	f.closure++
	return fmt.Sprintf("clo-%08x-e89b-12d3-a456-426614174001", f.closure)
}

// response is the last HTTP answer.
type response struct {
	status int
	header http.Header
	body   []byte
}

// world is the per-scenario state.
type world struct {
	ctx     context.Context
	store   *memory.Store
	metrics *memory.Metrics
	clock   *wall
	svc     *usecases.Service
	server  *httptest.Server

	appointments map[string]string // alias -> appointment id
	closures     map[string]string // alias -> closure id
	hours        map[string][]map[string]any
	last         response
	prev         response // the answer before last
	nextKey      string   // Idempotency-Key of the next POST
	ifMatch      string   // If-Match of the next request
	sweep        []string
	evSeq        int
	keySeq       int
}

// start builds the application over fresh in-memory doubles, as cmd/api does
// over Postgres and Kafka.
func (w *world) start() {
	w.ctx = context.Background()
	w.store, w.metrics = memory.NewStore(), &memory.Metrics{}
	w.clock = &wall{now: time.Date(2030, 1, 7, 6, 0, 0, 0, time.UTC)}
	w.svc = usecases.New(usecases.Deps{
		Appointments: w.store.Appointments(), Calendars: w.store.Calendars(), Doors: w.store.Doors(), Asns: w.store.Asns(),
		Cutoffs: w.store.Cutoffs(), Processed: w.store, Events: w.store, Metrics: w.metrics, IDs: &sequentialIDs{}, Clock: w.clock, UoW: w.store,
	})
	w.server = httptest.NewServer(inboundhttp.NewRouter(&inboundhttp.Server{
		Service: w.svc, Idempotency: w.store.Idempotency(), UoW: w.store, Now: w.clock.Now,
	}))
	w.appointments, w.closures, w.hours = map[string]string{}, map[string]string{}, map[string][]map[string]any{}
	w.last, w.prev, w.nextKey, w.ifMatch, w.sweep, w.evSeq, w.keySeq = response{}, response{}, "", "", nil, 0, 0
}

func (w *world) stop() {
	if w.server != nil {
		w.server.Close()
		w.server = nil
	}
}

// call sends one request; POSTs get a fresh Idempotency-Key. The answer is
// kept as the last response.
func (w *world) call(method, path string, body any) error {
	var reader io.Reader = http.NoBody
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(w.ctx, method, w.server.URL+path, reader)
	if err != nil {
		return err
	}
	if method == http.MethodPost {
		w.keySeq++
		req.Header.Set("Idempotency-Key", fmt.Sprintf("bdd-%d", w.keySeq))
	}
	if w.nextKey != "" {
		req.Header.Set("Idempotency-Key", w.nextKey)
		w.nextKey = ""
	}
	if w.ifMatch != "" {
		req.Header.Set("If-Match", w.ifMatch)
		w.ifMatch = ""
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	w.prev = w.last
	w.last = response{status: resp.StatusCode, header: resp.Header, body: raw}
	return nil
}

// json decodes the last body into a generic object.
func (w *world) json() (map[string]any, error) {
	var m map[string]any
	if err := json.Unmarshal(w.last.body, &m); err != nil {
		return nil, fmt.Errorf("the last body is not a JSON object (%d): %s", w.last.status, w.last.body)
	}
	return m, nil
}

// eventID mints a fresh CloudEvents id for a consumed fact.
func (w *world) eventID() string {
	w.evSeq++
	return fmt.Sprintf("evt-%d", w.evSeq)
}

// at turns "HH:MM" into the scenario day's RFC 3339 time.
func at(hhmm string) string { return scenarioDay + "T" + hhmm + ":00Z" }

func atTime(hhmm string) (time.Time, error) { return time.Parse(time.RFC3339, at(hhmm)) }

// InitializeScenario wires the steps and the per-scenario lifecycle.
func InitializeScenario(sc *godog.ScenarioContext) {
	w := &world{}
	sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
		w.start()
		return ctx, nil
	})
	sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
		w.stop()
		return ctx, nil
	})
	registerSetupSteps(sc, w)
	registerBookingSteps(sc, w)
	registerCalendarSteps(sc, w)
	registerYardSteps(sc, w)
	registerAssertions(sc, w)
}
