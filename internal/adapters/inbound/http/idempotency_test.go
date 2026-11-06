package http_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	inboundhttp "github.com/claudioed/dock-scheduling/internal/adapters/inbound/http"
	"github.com/claudioed/dock-scheduling/internal/application/ports"
	"github.com/claudioed/dock-scheduling/internal/application/repository"
	"github.com/claudioed/dock-scheduling/internal/domain/dockcalendar"
	"github.com/claudioed/dock-scheduling/internal/domain/shared"
)

// post sends a POST with an explicit Idempotency-Key.
func (e *env) postKeyed(key, path, body string) *httptest.ResponseRecorder {
	return e.do(req{method: http.MethodPost, path: path, body: body, headers: map[string]string{"Idempotency-Key": key}})
}

func countAppointments(t *testing.T, e *env) int {
	t.Helper()
	return len(decode(t, e.get("/appointments"))["items"].([]any))
}

func TestReplayReturnsTheStoredAnswerByteForByte(t *testing.T) {
	e := newEnv(t)
	body := inboundBody("D-IN", at8, at10, "ASN-1")
	first := e.postKeyed("k-1", "/appointments", body)
	wantStatus(t, first, http.StatusCreated)
	if first.Header().Get("Idempotency-Replayed") != "" {
		t.Fatal("the first execution carries no Idempotency-Replayed header")
	}
	second := e.postKeyed("k-1", "/appointments", body)
	wantStatus(t, second, http.StatusCreated)
	if second.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatal("a replay carries Idempotency-Replayed: true")
	}
	if second.Body.String() != first.Body.String() {
		t.Fatalf("the replay must be byte-identical:\n%s\n%s", first.Body.String(), second.Body.String())
	}
	for _, h := range []string{"ETag", "Location", "Content-Type"} {
		if first.Header().Get(h) != second.Header().Get(h) || first.Header().Get(h) == "" {
			t.Fatalf("replay must keep %s: %q vs %q", h, first.Header().Get(h), second.Header().Get(h))
		}
	}
	if n := countAppointments(t, e); n != 1 {
		t.Fatalf("the handler ran once, found %d appointments", n)
	}
	if n := len(e.store.Published()); n != 1 {
		t.Fatalf("one DockAppointmentBooked event, got %d", n)
	}
}

func TestSameKeyWithAnotherRequestIsConflict(t *testing.T) {
	e := newEnv(t)
	wantStatus(t, e.postKeyed("k-2", "/appointments", inboundBody("D-IN", at8, at10, "ASN-1")), http.StatusCreated)
	wantProblem(t, e.postKeyed("k-2", "/appointments", inboundBody("D-IN", at10, at12, "ASN-2")), 409, "idempotency-key-reused")
	id := decode(t, e.get("/appointments"))["items"].([]any)[0].(map[string]any)["appointmentId"].(string)
	wantProblem(t, e.postKeyed("k-2", "/appointments/"+id+"/cancellations", `{"reason":"SITE_REQUEST"}`), 409, "idempotency-key-reused")
}

func TestIdempotencyKeyIsRequiredAndValidated(t *testing.T) {
	e := newEnv(t)
	body := inboundBody("D-IN", at8, at10, "ASN-1")
	wantProblem(t, e.postKeyed("", "/appointments", body), 400, "idempotency-key-required")
	wantProblem(t, e.postKeyed(strings.Repeat("k", 129), "/appointments", body), 400, "idempotency-key-invalid")
	wantProblem(t, e.postKeyed("clé", "/appointments", body), 400, "idempotency-key-invalid")
	wantStatus(t, e.postKeyed(strings.Repeat("k", 128), "/appointments", body), http.StatusCreated)
}

func TestAClientErrorIsStoredAndReplayedWithoutSideEffects(t *testing.T) {
	e := newEnv(t)
	e.book("D-IN", at8, at10, "ASN-1")
	published := len(e.store.Published())
	conflict := inboundBody("D-IN", at8, at10, "ASN-2")
	first := e.postKeyed("k-4", "/appointments", conflict)
	wantProblem(t, first, 409, "door-double-booked")
	replay := e.postKeyed("k-4", "/appointments", conflict)
	wantProblem(t, replay, 409, "door-double-booked")
	if replay.Header().Get("Idempotency-Replayed") != "true" || replay.Body.String() != first.Body.String() {
		t.Fatalf("a 4xx answer replays too: %q", replay.Header().Get("Idempotency-Replayed"))
	}
	if countAppointments(t, e) != 1 || len(e.store.Published()) != published {
		t.Fatal("a refused booking leaves no appointment and no event")
	}
}

// brokenCalendars makes every calendar read fail until fixed.
type brokenCalendars struct {
	ports.CalendarRepo
	broken bool
}

func (b *brokenCalendars) GetForUpdate(ctx context.Context, site shared.SiteID) (*dockcalendar.DockCalendar, error) {
	if b.broken {
		return nil, errors.New("boom")
	}
	return b.CalendarRepo.GetForUpdate(ctx, site)
}

func TestAServerErrorIsNeverStored(t *testing.T) {
	e := newEnv(t)
	broken := &brokenCalendars{CalendarRepo: e.store.Calendars(), broken: true}
	e.svc = newService(e, func(d *depsOverride) { d.calendars = broken })
	e.server.Service = e.svc
	body := fmt.Sprintf(`{"doorCode":"D-IN","from":%q,"to":%q,"reason":"maintenance"}`, at8.Format(time.RFC3339), at10.Format(time.RFC3339))

	rec := e.postKeyed("k-5", "/sites/WH1/dock-calendar/closures", body)
	p := wantProblem(t, rec, 500, "internal-error")
	if strings.Contains(p["detail"].(string), "boom") {
		t.Fatalf("a 500 must not leak the internal message: %v", p["detail"])
	}
	broken.broken = false
	rec = e.postKeyed("k-5", "/sites/WH1/dock-calendar/closures", body)
	wantStatus(t, rec, http.StatusCreated)
	if rec.Header().Get("Idempotency-Replayed") != "" {
		t.Fatal("the retry after a 5xx runs the real work")
	}
}

func TestTheSweepIsKeyedToo(t *testing.T) {
	e := newEnv(t)
	wantProblem(t, e.postKeyed("", "/no-show-sweeps", ""), 400, "idempotency-key-required")
	first := e.postKeyed("k-6", "/no-show-sweeps", "")
	wantStatus(t, first, http.StatusOK)
	second := e.postKeyed("k-6", "/no-show-sweeps", "")
	if second.Header().Get("Idempotency-Replayed") != "true" || second.Body.String() != first.Body.String() {
		t.Fatalf("a sweep replay answers the stored result")
	}
}

func TestConcurrentRequestsWithOneKeyExecuteOnce(t *testing.T) {
	e := newEnv(t)
	body := inboundBody("D-IN", at8, at10, "ASN-1")
	const n = 12
	var wg sync.WaitGroup
	recs := make([]*httptest.ResponseRecorder, n)
	for i := range recs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			recs[i] = e.postKeyed("k-7", "/appointments", body)
		}()
	}
	wg.Wait()
	fresh := 0
	for _, rec := range recs {
		wantStatus(t, rec, http.StatusCreated)
		if rec.Header().Get("Idempotency-Replayed") == "" {
			fresh++
		}
		if rec.Body.String() != recs[0].Body.String() {
			t.Fatal("every caller sees the same answer")
		}
	}
	if fresh != 1 || countAppointments(t, e) != 1 {
		t.Fatalf("executed %d times, %d appointments; want exactly one", fresh, countAppointments(t, e))
	}
}

// rivalStore reports a competing answer on the post-save re-read, as a
// concurrent request that won the primary key would.
type rivalStore struct {
	ports.IdempotencyStore
	rival   repository.IdempotencyRecord
	lookups int
}

func (r *rivalStore) Lookup(ctx context.Context, key string) (repository.IdempotencyRecord, bool, error) {
	r.lookups++
	if r.lookups == 1 {
		return repository.IdempotencyRecord{}, false, nil
	}
	return r.rival, true, nil
}

func TestTheLoserOfAKeyRaceDiscardsItsWorkAndReplaysTheWinner(t *testing.T) {
	winner := newEnv(t)
	body := inboundBody("D-IN", at8, at10, "ASN-1")
	wantStatus(t, winner.postKeyed("k-8", "/appointments", body), http.StatusCreated)
	rec, found, err := winner.store.Idempotency().Lookup(context.Background(), "k-8")
	if err != nil || !found {
		t.Fatalf("winner record: %v %v", found, err)
	}
	rec.CreatedAt = rec.CreatedAt.Add(time.Millisecond) // another request's save

	loser := newEnv(t)
	loser.server.Idempotency = &rivalStore{IdempotencyStore: loser.store.Idempotency(), rival: rec}
	loser.router = inboundhttp.NewRouter(loser.server)
	got := loser.postKeyed("k-8", "/appointments", body)
	wantStatus(t, got, http.StatusCreated)
	if got.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatal("the loser replays the winner's answer")
	}
	if countAppointments(t, loser) != 0 || len(loser.store.Published()) != 0 {
		t.Fatal("the loser's own booking and event are rolled back")
	}
}

func TestRejectedRequestsNeverConsumeTheKey(t *testing.T) {
	e := newEnv(t)
	path := "/appointments"
	wantProblem(t, e.postKeyed("k-9", path, "{\"direction\":\"\\u0000\"}"), 400, "malformed-request-body")
	wantStatus(t, e.postKeyed("k-9", path, inboundBody("D-IN", at8, at10, "ASN-1")), http.StatusCreated)
}
