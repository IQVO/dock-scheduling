//go:build integration

package http_test

// Two real-route RETRYABLE proofs over a real Postgres and the production
// router (docs/adr/0014): the optimistic-lock conflict (concurrent-update)
// and a released conflict (door-double-booked). The loser's key row is
// absent after the refusal, its retry under the same key executes, and the
// success that follows is stored and replayed.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	inboundhttp "github.com/claudioed/dock-scheduling/internal/adapters/inbound/http"
	"github.com/claudioed/dock-scheduling/internal/adapters/outbound/memory"
	"github.com/claudioed/dock-scheduling/internal/adapters/outbound/postgres"
	"github.com/claudioed/dock-scheduling/internal/application/repository"
	"github.com/claudioed/dock-scheduling/internal/application/usecases"
	"github.com/claudioed/dock-scheduling/internal/domain/shared"
	"github.com/claudioed/dock-scheduling/internal/testing/pgtest"
)

var integClock = func() time.Time { return time.Date(2030, 1, 7, 6, 0, 0, 0, time.UTC) }

var (
	integAt8  = time.Date(2030, 1, 7, 8, 0, 0, 0, time.UTC)
	integAt10 = time.Date(2030, 1, 7, 10, 0, 0, 0, time.UTC)
	integAt12 = time.Date(2030, 1, 7, 12, 0, 0, 0, time.UTC)
)

// integIDs mints valid appt-<uuid>/clo-<uuid> shaped ids.
type integIDs struct{ n int }

func (f *integIDs) NewAppointmentID() string {
	f.n++
	return fmt.Sprintf("appt-%08x-e89b-12d3-a456-426614174000", f.n)
}

func (f *integIDs) NewClosureID() string {
	f.n++
	return fmt.Sprintf("clo-%08x-e89b-12d3-a456-426614174001", f.n)
}

type clockFunc struct{ now func() time.Time }

func (c clockFunc) Now() time.Time { return c.now() }

// integEnv is the production router over a fresh Postgres: the real
// repositories, the real unit of work, the real idempotency store and the
// real middleware of every keyed route.
type integEnv struct {
	router http.Handler
	pool   *pgxpool.Pool
}

func newIntegEnv(t *testing.T) *integEnv {
	t.Helper()
	pool := pgtest.NewPool(t)
	uow := postgres.NewUnitOfWork(pool)
	svc := usecases.New(usecases.Deps{
		Appointments: postgres.NewAppointmentRepo(pool),
		Calendars:    postgres.NewCalendarRepo(pool),
		Carriers:     postgres.NewCarrierRepo(pool),
		Doors:        postgres.NewDoorStore(pool),
		Asns:         postgres.NewAsnStore(pool),
		Cutoffs:      postgres.NewCutoffStore(pool),
		Processed:    postgres.NewProcessedEventRepo(pool),
		Events:       postgres.NewOutboxPublisher(pool),
		Metrics:      &memory.Metrics{},
		IDs:          &integIDs{},
		Clock:        clockFunc{integClock},
		UoW:          uow,
	})
	server := &inboundhttp.Server{Service: svc, Idempotency: postgres.NewIdempotencyStore(pool), UoW: uow, Now: integClock}
	env := &integEnv{router: inboundhttp.NewRouter(server), pool: pool}
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(postgres.NewDoorStore(pool).UpsertZone(ctx, "zone-dock", "WH1"))
	must(postgres.NewDoorStore(pool).UpsertDoor(ctx, "D-IN", "zone-dock", shared.FlowInbound, ""))
	for _, n := range []string{"ASN-1", "ASN-2"} {
		must(postgres.NewAsnStore(pool).Register(ctx, n, integAt8))
	}
	must(postgres.NewCutoffStore(pool).Replace(ctx, "WH1", "UTC", []repository.Cutoff{{ID: "WH1-1500"}}))
	return env
}

func (e *integEnv) do(method, key, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	return rec
}

func (e *integEnv) keyRows(key string) int {
	var n int
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM idempotency_keys WHERE key = $1`, key).Scan(&n); err != nil {
		panic(err)
	}
	return n
}

func bookBody(door string, start, end time.Time, asn string) string {
	return fmt.Sprintf(`{"direction":"INBOUND","doorCode":%q,"carrier":"Acme","windowStart":%q,"windowEnd":%q,"asnNumbers":[%q]}`,
		door, start.Format(time.RFC3339), end.Format(time.RFC3339), asn)
}

func integBookedID(t *testing.T, e *integEnv) string {
	t.Helper()
	rec := e.do(http.MethodGet, "", "/appointments", "")
	var listed struct {
		Items []struct {
			AppointmentID string `json:"appointmentId"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil || len(listed.Items) != 1 {
		t.Fatalf("list appointments: %v (%s)", err, rec.Body.String())
	}
	return listed.Items[0].AppointmentID
}

func wantIntegStatus(t *testing.T, rec *httptest.ResponseRecorder, code int) {
	t.Helper()
	if rec.Code != code {
		t.Fatalf("status = %d, want %d (body %s)", rec.Code, code, rec.Body.String())
	}
}

func wantIntegProblemSlug(t *testing.T, rec *httptest.ResponseRecorder, slug string) {
	t.Helper()
	var p struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("not a problem document: %v (%s)", err, rec.Body.String())
	}
	if !strings.HasSuffix(p.Type, slug) {
		t.Fatalf("problem type = %s, want .../%s", p.Type, slug)
	}
}

// The optimistic-lock conflict is RETRYABLE (ADR 0014): the version-guarded
// UPDATE is made to lose (a trigger drops it, standing in for a competing
// writer), the refusal is not stored — the loser's key row is absent — and
// the retry under the SAME key executes and is then stored and replayed.
func TestConcurrentUpdateRefusalIsNotStoredSoTheKeyRetriesTheWork(t *testing.T) {
	e := newIntegEnv(t)
	wantIntegStatus(t, e.do(http.MethodPost, "k-book", "/appointments", bookBody("D-IN", integAt8, integAt10, "ASN-1")), http.StatusCreated)
	id := integBookedID(t, e)

	ctx := context.Background()
	exec := func(sql string) {
		t.Helper()
		if _, err := e.pool.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	exec(`CREATE OR REPLACE FUNCTION drop_update_probe() RETURNS trigger AS $$ BEGIN RETURN NULL; END $$ LANGUAGE plpgsql`)
	exec(`CREATE TRIGGER drop_update_probe BEFORE UPDATE ON dock_appointments FOR EACH ROW EXECUTE FUNCTION drop_update_probe()`)

	refused := e.do(http.MethodPost, "k-cancel", "/appointments/"+id+"/cancellations", `{"reason":"SITE_REQUEST"}`)
	wantIntegStatus(t, refused, http.StatusConflict)
	wantIntegProblemSlug(t, refused, "concurrent-update")
	if rows := e.keyRows("k-cancel"); rows != 0 {
		t.Fatalf("key rows after the concurrent-update refusal = %d, want 0: a RETRYABLE refusal is not stored", rows)
	}

	exec(`DROP TRIGGER drop_update_probe ON dock_appointments`)

	retry := e.do(http.MethodPost, "k-cancel", "/appointments/"+id+"/cancellations", `{"reason":"SITE_REQUEST"}`)
	wantIntegStatus(t, retry, http.StatusOK)
	if retry.Header().Get("Idempotency-Replayed") != "" {
		t.Fatal("the retry must execute, not replay")
	}
	stored := e.do(http.MethodPost, "k-cancel", "/appointments/"+id+"/cancellations", `{"reason":"SITE_REQUEST"}`)
	wantIntegStatus(t, stored, http.StatusOK)
	if stored.Header().Get("Idempotency-Replayed") != "true" || stored.Body.String() != retry.Body.String() {
		t.Fatalf("the stored 2xx answer must replay byte-identically (%q vs %q)", stored.Body.String(), retry.Body.String())
	}
}

// door-double-booked is RETRYABLE (ADR 0014): the conflicting booking's
// refusal is not stored; once the conflicting appointment is cancelled, the
// SAME key and body execute and are stored.
func TestDoorDoubleBookedRefusalIsNotStoredAndTheKeyIsFreeAgain(t *testing.T) {
	e := newIntegEnv(t)
	wantIntegStatus(t, e.do(http.MethodPost, "k-a", "/appointments", bookBody("D-IN", integAt8, integAt10, "ASN-1")), http.StatusCreated)

	clash := bookBody("D-IN", time.Date(2030, 1, 7, 9, 0, 0, 0, time.UTC), integAt12, "ASN-2")
	refused := e.do(http.MethodPost, "k-b", "/appointments", clash)
	wantIntegStatus(t, refused, http.StatusConflict)
	wantIntegProblemSlug(t, refused, "door-double-booked")
	if rows := e.keyRows("k-b"); rows != 0 {
		t.Fatalf("key rows after the door-double-booked refusal = %d, want 0: a RETRYABLE refusal is not stored", rows)
	}

	// The conflict clears, and the released key makes the retry a new run.
	id := integBookedID(t, e)
	wantIntegStatus(t, e.do(http.MethodPost, "k-free", "/appointments/"+id+"/cancellations", `{"reason":"SITE_REQUEST"}`), http.StatusOK)
	retry := e.do(http.MethodPost, "k-b", "/appointments", clash)
	wantIntegStatus(t, retry, http.StatusCreated)
	if retry.Header().Get("Idempotency-Replayed") != "" {
		t.Fatal("the retry under the released key must execute, not replay")
	}
	if rows := e.keyRows("k-b"); rows != 1 {
		t.Fatalf("key rows after the success = %d, want 1: a 2xx is stored", rows)
	}
}
