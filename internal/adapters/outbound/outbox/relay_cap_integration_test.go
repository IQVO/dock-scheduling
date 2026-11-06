//go:build integration

package outbox_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	relay "github.com/claudioed/dock-scheduling/internal/adapters/outbound/outbox"
	"github.com/claudioed/dock-scheduling/internal/adapters/outbound/postgres"
	appoutbox "github.com/claudioed/dock-scheduling/internal/application/outbox"
	"github.com/claudioed/dock-scheduling/internal/testing/pgtest"
)

// The attempt cap over real Postgres (ADR 0005; the fitness sensor
// TestOutboxRelaysPassAttemptCap's documented convention): a row that keeps
// failing is excluded from claims once it has failed maxAttempts times —
// the failure that reaches the cap is logged once at ERROR with the row's
// id, event and key — and the good row behind it, same key, then publishes.

// flakyHandler fails rows whose event is in fail (under the mutex) and
// accepts everything else, recording what it sent.
type flakyHandler struct {
	mu   sync.Mutex
	fail map[string]bool
	sent []appoutbox.Row
}

func (h *flakyHandler) Handle(_ context.Context, row appoutbox.Row) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.fail[row.Fact.Event] {
		return errors.New("kafka: message too large")
	}
	h.sent = append(h.sent, row)
	return nil
}

func (h *flakyHandler) heal() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.fail = nil
}

func (h *flakyHandler) sentEvents() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]string, len(h.sent))
	for i, r := range h.sent {
		out[i] = r.Fact.Event
	}
	return out
}

// insertApptRow inserts one due outbox row with a minimal dockappointment
// envelope and returns its id.
func insertApptRow(t *testing.T, pool *pgxpool.Pool, event, caseID string) int64 {
	t.Helper()
	var id int64
	err := pool.QueryRow(context.Background(), `
		INSERT INTO outbox (event_id, aggreg_type, aggreg_id, payload, occurred_at)
		VALUES ($1, 'dockappointment', $2, $3::jsonb, now())
		RETURNING id
	`, "evt-"+event, caseID,
		`{"event":"`+event+`","aggregate_id":"`+caseID+`","occurred_at":"2026-10-21T09:30:00Z","data":{"appointment_id":"`+caseID+`"}}`,
	).Scan(&id)
	if err != nil {
		t.Fatalf("insert outbox row: %v", err)
	}
	return id
}

func makeDue(t *testing.T, pool *pgxpool.Pool, id int64) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`UPDATE outbox SET next_attempt_at = now() WHERE id = $1`, id); err != nil {
		t.Fatalf("make row %d due: %v", id, err)
	}
}

func rowAttempts(t *testing.T, pool *pgxpool.Pool, id int64) (attempts int, present bool) {
	t.Helper()
	err := pool.QueryRow(context.Background(),
		`SELECT attempts FROM outbox WHERE id = $1`, id).Scan(&attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false
	}
	if err != nil {
		t.Fatalf("read row %d: %v", id, err)
	}
	return attempts, true
}

// failingPass runs one relay pass and wants it to fail publishing nothing.
func failingPass(t *testing.T, r *relay.Relay) {
	t.Helper()
	published, err := r.RelayOnce(context.Background())
	if err == nil || published != 0 {
		t.Fatalf("pass: published=%d err=%v, want the failure with nothing published", published, err)
	}
}

// errorRecords returns the ERROR records of a JSON slog buffer.
func errorRecords(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	dec := json.NewDecoder(buf)
	for dec.More() {
		var rec map[string]any
		if err := dec.Decode(&rec); err != nil {
			t.Fatalf("decode log record: %v", err)
		}
		if rec["level"] == "ERROR" {
			out = append(out, rec)
		}
	}
	return out
}

// TestOutboxRelay_MaxAttempts_PoisonRowExcludedThenGoodRowPublishes is the
// cap's reference test: the poison row fails exactly maxAttempts times (its
// backoff is elided between passes), the pass that records the last failure
// logs ONE attempt-cap ERROR naming the row, and from then on the row is
// excluded from claims — the good row behind it, same key, publishes.
func TestOutboxRelay_MaxAttempts_PoisonRowExcludedThenGoodRowPublishes(t *testing.T) {
	pool := pgtest.NewPool(t)
	poisonID := insertApptRow(t, pool, "Poison", "appt-cap-a")
	goodID := insertApptRow(t, pool, "Good", "appt-cap-a")

	var logs bytes.Buffer
	handler := &flakyHandler{fail: map[string]bool{"Poison": true}}
	r := relay.NewOutboxRelay(postgres.NewOutboxStore(pool), handler, slog.New(slog.NewJSONHandler(&logs, nil)),
		relay.WithMaxAttempts(3))

	failingPass(t, r)
	makeDue(t, pool, poisonID)
	failingPass(t, r)
	if recs := errorRecords(t, &logs); len(recs) != 0 {
		t.Fatalf("no attempt-cap ERROR below the cap, got %v", recs)
	}

	makeDue(t, pool, poisonID)
	failingPass(t, r) // third failure: reaches the cap
	assertCapRecord(t, errorRecords(t, &logs), poisonID)

	// The capped row is excluded from claims even when due; the good row
	// behind it publishes.
	makeDue(t, pool, poisonID)
	assertOnlyTheGoodRowPublishes(t, pool, r, handler, poisonID, goodID)
	assertRearmPublishes(t, pool, r, handler, poisonID)
}

// assertCapRecord wants exactly one attempt-cap ERROR naming the poison row.
func assertCapRecord(t *testing.T, recs []map[string]any, poisonID int64) {
	t.Helper()
	if len(recs) != 1 {
		t.Fatalf("want exactly 1 attempt-cap ERROR record, got %d", len(recs))
	}
	for field, want := range map[string]any{
		"outbox_id":    float64(poisonID),
		"event_type":   "dockappointment.Poison",
		"key":          "appt-cap-a",
		"attempts":     float64(3),
		"max_attempts": float64(3),
	} {
		if recs[0][field] != want {
			t.Errorf("cap record %s = %v, want %v", field, recs[0][field], want)
		}
	}
}

func assertOnlyTheGoodRowPublishes(t *testing.T, pool *pgxpool.Pool, r *relay.Relay, handler *flakyHandler, poisonID, goodID int64) {
	t.Helper()
	if published, err := r.RelayOnce(context.Background()); err != nil || published != 1 {
		t.Fatalf("pass after the cap = %d, %v; want 1 (the good row), nil", published, err)
	}
	if got := handler.sentEvents(); len(got) != 1 || got[0] != "Good" {
		t.Fatalf("sink got %v, want only the good row", got)
	}
	if _, ok := rowAttempts(t, pool, goodID); ok {
		t.Fatal("the good row was not removed")
	}
	if published, err := r.RelayOnce(context.Background()); err != nil || published != 0 {
		t.Fatalf("drained pass = %d, %v; want a no-op", published, err)
	}
	if attempts, ok := rowAttempts(t, pool, poisonID); !ok || attempts != 3 {
		t.Fatalf("poison row: attempts=%d present=%v, want 3, still in the outbox", attempts, ok)
	}
}

// assertRearmPublishes is the runbook's re-arm: attempts = 0 (and a healed
// handler) claims the row again and it publishes.
func assertRearmPublishes(t *testing.T, pool *pgxpool.Pool, r *relay.Relay, handler *flakyHandler, poisonID int64) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`UPDATE outbox SET attempts = 0, next_attempt_at = now() WHERE id = $1`, poisonID); err != nil {
		t.Fatal(err)
	}
	handler.heal()
	if published, err := r.RelayOnce(context.Background()); err != nil || published != 1 {
		t.Fatalf("re-armed pass = %d, %v; want 1, nil", published, err)
	}
	if _, ok := rowAttempts(t, pool, poisonID); ok {
		t.Fatal("the re-armed row was not removed after publishing")
	}
}

// TestOutboxRelay_NoCap_PoisonRowBlocksLaterRows pins WithMaxAttempts(0)
// (OUTBOX_MAX_ATTEMPTS=0, unlimited): the poison row is retried forever and
// the row behind it never publishes. This is why every cmd/* relay must pass
// a cap (architecture fitness test TestOutboxRelaysPassAttemptCap).
func TestOutboxRelay_NoCap_PoisonRowBlocksLaterRows(t *testing.T) {
	pool := pgtest.NewPool(t)
	poisonID := insertApptRow(t, pool, "Poison", "appt-nocap-b")
	goodID := insertApptRow(t, pool, "Good", "appt-nocap-b")

	handler := &flakyHandler{fail: map[string]bool{"Poison": true}}
	r := relay.NewOutboxRelay(postgres.NewOutboxStore(pool), handler, slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil)))

	for i := 1; i <= 5; i++ {
		failingPass(t, r)
		makeDue(t, pool, poisonID) // the scheduler's tick; backoff elided
	}
	if _, ok := rowAttempts(t, pool, goodID); !ok {
		t.Fatal("the good row vanished under an unlimited relay")
	}
	if attempts, _ := rowAttempts(t, pool, poisonID); attempts != 5 {
		t.Fatalf("poison attempts = %d, want 5", attempts)
	}
	if got := handler.sentEvents(); len(got) != 0 {
		t.Fatalf("unlimited relay sent %v, want nothing (blocked on the poison row)", got)
	}
}
