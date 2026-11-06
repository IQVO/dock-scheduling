package http_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	inboundhttp "github.com/claudioed/dock-scheduling/internal/adapters/inbound/http"
	"github.com/claudioed/dock-scheduling/internal/adapters/outbound/memory"
	"github.com/claudioed/dock-scheduling/internal/application/ports"
	"github.com/claudioed/dock-scheduling/internal/application/repository"
	"github.com/claudioed/dock-scheduling/internal/application/usecases"
	"github.com/claudioed/dock-scheduling/internal/domain/shared"
)

var (
	t0   = time.Date(2030, 1, 7, 6, 0, 0, 0, time.UTC) // a Monday, "now"
	at8  = time.Date(2030, 1, 7, 8, 0, 0, 0, time.UTC)
	at10 = at8.Add(2 * time.Hour)
	at12 = at8.Add(4 * time.Hour)
)

type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time { return c.now }

type fakeIDs struct{ appt, closure int }

func (f *fakeIDs) NewAppointmentID() string {
	f.appt++
	return fmt.Sprintf("appt-%08x-e89b-12d3-a456-426614174000", f.appt)
}

func (f *fakeIDs) NewClosureID() string {
	f.closure++
	return fmt.Sprintf("clo-%08x-e89b-12d3-a456-426614174001", f.closure)
}

// env is one service over the in-memory store and its REST router.
type env struct {
	t      *testing.T
	store  *memory.Store
	clock  *fakeClock
	svc    *usecases.Service
	server *inboundhttp.Server
	router http.Handler
}

func newEnv(t *testing.T) *env {
	t.Helper()
	s := memory.NewStore()
	e := &env{t: t, store: s, clock: &fakeClock{now: t0}}
	e.svc = newService(e, nil)
	e.server = &inboundhttp.Server{Service: e.svc, Idempotency: s.Idempotency(), UoW: s, Now: e.clock.Now}
	e.router = inboundhttp.NewRouter(e.server)
	ctx := context.Background()
	must(t, s.Doors().UpsertZone(ctx, "zone-dock", "WH1"))
	must(t, s.Doors().UpsertDoor(ctx, "D-IN", "zone-dock", shared.FlowInbound))
	must(t, s.Doors().UpsertDoor(ctx, "D-OUT", "zone-dock", shared.FlowOutbound))
	must(t, s.Doors().UpsertDoor(ctx, "D-BOTH", "zone-dock", shared.FlowBoth))
	for _, n := range []string{"ASN-1", "ASN-2", "ASN-3"} {
		must(t, s.Asns().Register(ctx, n, at8))
	}
	must(t, s.Cutoffs().Replace(ctx, "WH1", "UTC", []repository.Cutoff{{ID: "WH1-1500"}}))
	return e
}

// depsOverride replaces a port of the service under test.
type depsOverride struct{ calendars ports.CalendarRepo }

// newService builds the use cases over the env's store; mod swaps ports.
func newService(e *env, mod func(*depsOverride)) *usecases.Service {
	d := depsOverride{calendars: e.store.Calendars()}
	if mod != nil {
		mod(&d)
	}
	s := e.store
	return usecases.New(usecases.Deps{
		Appointments: s.Appointments(), Calendars: d.calendars, Doors: s.Doors(), Asns: s.Asns(), Cutoffs: s.Cutoffs(),
		Processed: s, Events: s, Metrics: &memory.Metrics{}, IDs: &fakeIDs{}, Clock: e.clock, UoW: s,
	})
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// req is one HTTP call; a POST gets a fresh Idempotency-Key unless it sets its own.
type req struct {
	method, path, body string
	headers            map[string]string
}

var keySeq int

func (e *env) do(r req) *httptest.ResponseRecorder {
	e.t.Helper()
	var body *strings.Reader
	if r.body != "" {
		body = strings.NewReader(r.body)
	} else {
		body = strings.NewReader("")
	}
	hr := httptest.NewRequest(r.method, r.path, body)
	if _, explicit := r.headers["Idempotency-Key"]; r.method == http.MethodPost && !explicit {
		keySeq++
		hr.Header.Set("Idempotency-Key", fmt.Sprintf("key-%d", keySeq))
	}
	for k, v := range r.headers {
		if v == "" {
			hr.Header.Del(k)
			continue
		}
		hr.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, hr)
	return rec
}

func (e *env) post(path, body string) *httptest.ResponseRecorder {
	return e.do(req{method: http.MethodPost, path: path, body: body})
}

func (e *env) get(path string) *httptest.ResponseRecorder {
	return e.do(req{method: http.MethodGet, path: path})
}

func (e *env) put(path, body string) *httptest.ResponseRecorder {
	return e.do(req{method: http.MethodPut, path: path, body: body})
}

// decode unmarshals the recorder body into a generic map.
func decode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("body is not a JSON object: %v\n%s", err, rec.Body.String())
	}
	return m
}

// wantStatus fails unless the recorder holds status.
func wantStatus(t *testing.T, rec *httptest.ResponseRecorder, status int) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, status, rec.Body.String())
	}
}

// wantProblem fails unless the recorder is a problem+json of status and slug.
func wantProblem(t *testing.T, rec *httptest.ResponseRecorder, status int, slug string) map[string]any {
	t.Helper()
	wantStatus(t, rec, status)
	if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("content type = %q, want application/problem+json", ct)
	}
	p := decode(t, rec)
	if want := "https://errors.dock-scheduling.warehouse-systems.dev/" + slug; p["type"] != want {
		t.Fatalf("problem type = %v, want %s; body: %s", p["type"], want, rec.Body.String())
	}
	for _, f := range []string{"title", "detail", "instance"} {
		if s, _ := p[f].(string); s == "" {
			t.Fatalf("problem has no %s: %s", f, rec.Body.String())
		}
	}
	if int(p["status"].(float64)) != status {
		t.Fatalf("problem status field = %v, want %d", p["status"], status)
	}
	return p
}

func inboundBody(door string, start, end time.Time, asn string) string {
	return fmt.Sprintf(`{"direction":"INBOUND","doorCode":%q,"carrier":"ACME Freight","carrierScac":"ACMF","windowStart":%q,"windowEnd":%q,"asnNumbers":[%q],"expectedSealNumber":"SEAL-1"}`,
		door, start.Format(time.RFC3339), end.Format(time.RFC3339), asn)
}

func outboundBody(door string, start, end time.Time) string {
	return fmt.Sprintf(`{"direction":"OUTBOUND","doorCode":%q,"carrier":"ACME Freight","windowStart":%q,"windowEnd":%q,"cptId":"WH1-1500"}`,
		door, start.Format(time.RFC3339), end.Format(time.RFC3339))
}

// book books an inbound appointment and returns its id.
func (e *env) book(door string, start, end time.Time, asn string) string {
	e.t.Helper()
	rec := e.post("/appointments", inboundBody(door, start, end, asn))
	wantStatus(e.t, rec, http.StatusCreated)
	return decode(e.t, rec)["appointmentId"].(string)
}
