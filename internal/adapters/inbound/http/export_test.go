package http

// Test hooks for the replay-class tests (idempotency_replay_classes_*_test.go,
// package http_test): the production catalogue with each entry's declared
// replay class, the production error writer behind a keyed route, and the
// production Idempotency-Key middleware over a caller-chosen store.

import (
	"net/http"
	"time"

	"github.com/claudioed/dock-scheduling/internal/application/ports"
)

// ReplayProblemForTest is one catalogue entry as the external tests see it.
type ReplayProblemForTest struct {
	Slug   string
	Status int
	Class  string // FINAL, OUTCOME or RETRYABLE as the entry declares it
}

// ReplayCatalogueForTest returns every problem type the catalogue maps, with
// the replay class its entry declares. RED STAGE: the catalogue has no class
// field yet, so every entry reports none — exactly what the first template
// test must fail on.
func ReplayCatalogueForTest() []ReplayProblemForTest {
	out := make([]ReplayProblemForTest, 0, len(problemCatalogue))
	for _, e := range problemCatalogue {
		out = append(out, ReplayProblemForTest{Slug: e.p.slug, Status: e.p.status, Class: ""})
	}
	return out
}

// ReplayEntryErrorForTest returns the typed error of the first catalogue entry
// whose slug is slug, so a test can refuse exactly as the handler does.
func ReplayEntryErrorForTest(slug string) (error, bool) {
	for _, e := range problemCatalogue {
		if e.p.slug == slug {
			return e.err, true
		}
	}
	return nil, false
}

// RefuseForTest answers err through the production error writer, so the
// production class marking runs exactly as it does behind a keyed route.
func RefuseForTest(w http.ResponseWriter, r *http.Request, err error) {
	writeError(w, r, err)
}

// KeyedForTest is the production Idempotency-Key middleware (Server.keyed)
// over a caller-chosen store and unit of work: the same wrap every keyed
// route is registered with, detached for the sweep's mode.
func KeyedForTest(store ports.IdempotencyStore, uow ports.UnitOfWork, now func() time.Time, detached bool) func(http.Handler) http.Handler {
	s := &Server{Idempotency: store, UoW: uow, Now: now}
	return s.keyed(detached)
}
