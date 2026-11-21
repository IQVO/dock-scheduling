package http

// The Idempotency-Key middleware per replay class (ADR 0014), over the
// in-memory store, in BOTH modes (JOINED, the keyed routes; DETACHED, the
// sweep), driven with a fake handler that makes one write through the
// request's unit of work and then answers a catalogue entry's refusal. The
// write is what tells the classes apart: FINAL and RETRYABLE roll it back,
// OUTCOME commits it. The synthetic OUTCOME entry proves the mechanism no
// real entry uses yet.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/claudioed/dock-scheduling/internal/adapters/outbound/memory"
	"github.com/claudioed/dock-scheduling/internal/domain/dockappointment"
	"github.com/claudioed/dock-scheduling/internal/domain/shared"
)

// probeDoor is the door fact the scripted handler writes through the
// request's context (joining the middleware's unit of work exactly as a use
// case's save does).
const probeDoor = "PROBE-DOOR"

// keyedScript is a handler that optionally writes the probe and then answers
// what answer says for this run.
type keyedScript struct {
	store   *memory.Store
	calls   atomic.Int64
	answer  func(w http.ResponseWriter, r *http.Request)
	writeOn bool
}

func (h *keyedScript) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.calls.Add(1)
	if h.writeOn {
		if err := h.store.Doors().UpsertDoor(r.Context(), probeDoor, "zone-dock", shared.FlowInbound, ""); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	h.answer(w, r)
}

func (h *keyedScript) wrote() bool {
	door, err := h.store.Doors().Door(context.Background(), probeDoor)
	return err == nil && door != nil
}

func refusingWith(err error) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) { writeError(w, r, err) }
}

// rig builds the production middleware over the in-memory store, in one mode.
type rig struct {
	store    *memory.Store
	detached bool
}

func newRig(t *testing.T, detached bool) *rig {
	t.Helper()
	g := &rig{store: memory.NewStore(), detached: detached}
	if err := g.store.Doors().UpsertZone(context.Background(), "zone-dock", "WH1"); err != nil {
		t.Fatal(err)
	}
	return g
}

func (g *rig) post(key string, h http.Handler) *httptest.ResponseRecorder {
	m := &idempotency{store: g.store.Idempotency(), uow: g.store, now: func() time.Time { return time.Unix(0, 0).UTC() }}
	req := httptest.NewRequest(http.MethodPost, "/appointments", strings.NewReader(`{"n":1}`))
	req.Header.Set(IdempotencyKeyHeader, key)
	rec := httptest.NewRecorder()
	m.wrap(h, g.detached).ServeHTTP(rec, req)
	return rec
}

func (g *rig) stored(key string) bool {
	_, ok, err := g.store.Idempotency().Lookup(context.Background(), key)
	return err == nil && ok
}

func replayedHeader(rec *httptest.ResponseRecorder) bool {
	return rec.Header().Get(IdempotencyReplayedHeader) == "true"
}

// Every catalogue entry, answered by the scripted handler under one key and
// asked again, in both modes: a FINAL refusal is stored and replayed with the
// handler's write rolled back; a RETRYABLE refusal is not stored, the key is
// free and the retry runs again (its write rolled back too in JOINED).
func TestEveryCatalogueEntryIsReplayedOrReleasedByItsClass(t *testing.T) {
	for _, e := range problemCatalogue {
		for _, mode := range []struct {
			name     string
			detached bool
		}{{"joined", false}, {"detached", true}} {
			t.Run(e.p.slug+"/"+e.replay.String()+"/"+mode.name, func(t *testing.T) {
				g := newRig(t, mode.detached)
				h := &keyedScript{store: g.store, answer: refusingWith(e.err), writeOn: true}
				key := "key-" + e.p.slug + "-" + mode.name
				stored := e.replay == replayFinal // no OUTCOME entry exists today (TestNoCatalogueEntryIsOutcomeYet)

				first, second := g.post(key, h), g.post(key, h)
				checkFirstAnswer(t, e, mode, first, h, g, key, stored)
				checkSecondAnswer(t, e, second, first, h, stored)
			})
		}
	}
}

// checkFirstAnswer: the refusal answers with its status, is stored only when
// its class says so, and (JOINED) its write was rolled back.
func checkFirstAnswer(t *testing.T, e catalogueEntry, mode struct {
	name     string
	detached bool
}, first *httptest.ResponseRecorder, h *keyedScript, g *rig, key string, stored bool) {
	t.Helper()
	if first.Code != e.p.status {
		t.Fatalf("first answer = %d, want %d (%s)", first.Code, e.p.status, first.Body.String())
	}
	if got := g.stored(key); got != stored {
		t.Fatalf("key stored after the first answer = %v, want %v", got, stored)
	}
	if !mode.detached && h.wrote() {
		t.Fatal("a FINAL or RETRYABLE refusal must roll the handler's write back (JOINED)")
	}
}

// checkSecondAnswer: a stored refusal replays byte-identically with the
// handler run once; a released one runs again without the replay header.
func checkSecondAnswer(t *testing.T, e catalogueEntry, second, first *httptest.ResponseRecorder, h *keyedScript, stored bool) {
	t.Helper()
	if second.Code != e.p.status {
		t.Fatalf("second answer = %d, want %d", second.Code, e.p.status)
	}
	switch {
	case !stored:
		if h.calls.Load() != 2 || replayedHeader(second) {
			t.Fatalf("calls=%d replayed=%v: a RETRYABLE answer is released and runs again", h.calls.Load(), replayedHeader(second))
		}
	case h.calls.Load() != 1 || !replayedHeader(second):
		t.Fatalf("calls=%d replayed=%v: a FINAL answer is stored and replayed", h.calls.Load(), replayedHeader(second))
	case second.Body.String() != first.Body.String():
		t.Fatalf("replay body %q differs from the first answer %q", second.Body.String(), first.Body.String())
	}
}

// errSyntheticOutcome stands for a use case that commits state and then
// refuses: no catalogue entry does that today, and this proof pins the
// mechanism (the class exists, the middleware commits the writes with the
// stored answer) so the first real OUTCOME is a catalogue change only.
var errSyntheticOutcome = errors.New("synthetic outcome refusal")

// The OUTCOME mechanism: the write commits WITH the stored answer, a replay
// records nothing new, in both modes.
func TestAnOutcomeRefusalCommitsItsWritesAndIsReplayed(t *testing.T) {
	orig := problemCatalogue
	defer func() { problemCatalogue = orig }()
	problemCatalogue = append(problemCatalogue, catalogueEntry{
		errSyntheticOutcome, conflict("synthetic-outcome-refusal", "A committed refusal"), replayOutcome,
	})
	for _, mode := range []struct {
		name     string
		detached bool
	}{{"joined", false}, {"detached", true}} {
		t.Run(mode.name, func(t *testing.T) {
			checkOutcomeUnderMode(t, mode)
		})
	}
}

func checkOutcomeUnderMode(t *testing.T, mode struct {
	name     string
	detached bool
}) {
	t.Helper()
	g := newRig(t, mode.detached)
	h := &keyedScript{store: g.store, answer: refusingWith(errSyntheticOutcome), writeOn: true}
	key := "k-outcome-" + mode.name

	first := g.post(key, h)
	if first.Code != http.StatusConflict {
		t.Fatalf("first answer = %d (%s)", first.Code, first.Body.String())
	}
	if !h.wrote() {
		t.Fatal("an OUTCOME refusal commits what the command wrote")
	}
	if !g.stored(key) {
		t.Fatal("the OUTCOME answer is stored with the key")
	}
	for i := 0; i < 3; i++ {
		rep := g.post(key, h)
		if rep.Code != http.StatusConflict || !replayedHeader(rep) || rep.Body.String() != first.Body.String() {
			t.Fatalf("replay %d = %d replayed=%v", i, rep.Code, replayedHeader(rep))
		}
	}
	if h.calls.Load() != 1 {
		t.Fatalf("handler ran %d times: a replay records nothing new", h.calls.Load())
	}
}

// When the first request ends RETRYABLE nothing is stored: a concurrent
// duplicate finds no answer and runs the handler itself.
func TestAConcurrentDuplicateExecutesWhenTheFirstEndsRetryable(t *testing.T) {
	g := newRig(t, false)
	h := &keyedScript{store: g.store, answer: refusingWith(dockappointment.ErrDoorDoubleBooked), writeOn: true}
	const n = 4
	var wg sync.WaitGroup
	recs := make([]*httptest.ResponseRecorder, n)
	for i := range recs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			recs[i] = g.post("k-race", h)
		}()
	}
	wg.Wait()
	for _, rec := range recs {
		if rec.Code != http.StatusConflict || replayedHeader(rec) {
			t.Fatalf("a concurrent duplicate = %d replayed=%v, want a fresh refusal", rec.Code, replayedHeader(rec))
		}
	}
	if h.calls.Load() != n {
		t.Fatalf("handler ran %d times, want %d: a released key lets every duplicate execute", h.calls.Load(), n)
	}
}
