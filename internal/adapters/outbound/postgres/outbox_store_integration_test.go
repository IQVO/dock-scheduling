//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/claudioed/dock-scheduling/internal/adapters/outbound/postgres"
	"github.com/claudioed/dock-scheduling/internal/application/outbox"
	"github.com/claudioed/dock-scheduling/internal/domain/dockcalendar"
	"github.com/claudioed/dock-scheduling/internal/testing/pgtest"
)

// This file proves the outbox store: the publisher writes self-contained
// wire-shaped rows, and Drain claims, removes, retries, caps and skips locked
// rows exactly as the relay needs.

func publishBooked(t *testing.T, pool *pgxpool.Pool, n int) {
	t.Helper()
	_, events := booked(t, n, "D1", n)
	if err := postgres.NewOutboxPublisher(pool).PublishAppointmentEvents(context.Background(), events...); err != nil {
		t.Fatal(err)
	}
}

func ok(context.Context, outbox.Row) error { return nil }

func TestPublisherWritesWireShapedRowsWithAStableEventID(t *testing.T) {
	pool := pgtest.NewPool(t)
	pub := postgres.NewOutboxPublisher(pool)
	ids := []string{"id-1", "id-2"}
	pub.NewID = func() string { id := ids[0]; ids = ids[1:]; return id }
	a, events := booked(t, 1, "D1", 0)
	_ = a
	cal, err := dockcalendar.New("WH1")
	if err != nil {
		t.Fatal(err)
	}
	_, calEvents, err := cal.ScheduleClosure("clo-123e4567-e89b-12d3-a456-426614174001", "D1", base.Add(48*time.Hour), base.Add(72*time.Hour), "maintenance", nil, base)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := pub.PublishAppointmentEvents(ctx, events...); err != nil {
		t.Fatal(err)
	}
	if err := pub.PublishCalendarEvents(ctx, calEvents...); err != nil {
		t.Fatal(err)
	}
	rows, err := pool.Query(ctx, `SELECT event_id, aggreg_type, aggreg_id, payload FROM outbox ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	want := []struct{ id, typ, agg, event string }{
		{"id-1", "dockappointment", apptID(1), "DockAppointmentBooked"},
		{"id-2", "dockcalendar", "WH1", "DoorClosureScheduled"},
	}
	for i := 0; rows.Next(); i++ {
		var id, typ, agg string
		var payload []byte
		if err := rows.Scan(&id, &typ, &agg, &payload); err != nil {
			t.Fatal(err)
		}
		var env struct {
			Event string          `json:"event"`
			Data  json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(payload, &env); err != nil {
			t.Fatal(err)
		}
		if id != want[i].id || typ != want[i].typ || agg != want[i].agg || env.Event != want[i].event || len(env.Data) < 10 {
			t.Fatalf("row %d = %s %s %s %s %s", i, id, typ, agg, env.Event, env.Data)
		}
	}
}

func TestDrainRemovesPublishedRowsInOrder(t *testing.T) {
	pool := pgtest.NewPool(t)
	for n := 1; n <= 3; n++ {
		publishBooked(t, pool, n)
	}
	var seen []string
	n, err := postgres.NewOutboxStore(pool).Drain(context.Background(), 10, 0, func(_ context.Context, r outbox.Row) error {
		seen = append(seen, r.Fact.AggregateID)
		if r.Fact.Event != "DockAppointmentBooked" || r.Fact.Entity != "dockappointment" || r.Fact.EventID == "" || len(r.Fact.Data) == 0 || len(r.RawPayload) == 0 {
			t.Errorf("row = %+v", r)
		}
		return nil
	})
	if err != nil || n != 3 {
		t.Fatalf("Drain = %d, %v", n, err)
	}
	if len(seen) != 3 || seen[0] != apptID(1) || seen[2] != apptID(3) {
		t.Fatalf("order = %v", seen)
	}
	if c := pgtest.Count(t, pool, "outbox"); c != 0 {
		t.Fatalf("outbox = %d after a clean drain", c)
	}
}

func TestDrainStopsAtTheFirstFailureAndBacksOff(t *testing.T) {
	pool := pgtest.NewPool(t)
	for n := 1; n <= 3; n++ {
		publishBooked(t, pool, n)
	}
	store, boom := postgres.NewOutboxStore(pool), errors.New("broker down")
	calls := 0
	n, err := store.Drain(context.Background(), 10, 0, func(_ context.Context, r outbox.Row) error {
		calls++
		if calls == 2 {
			return boom
		}
		return nil
	})
	var sf *outbox.SendFailureError
	if !errors.As(err, &sf) || !errors.Is(err, boom) || sf.Attempts != 1 || sf.Row.Fact.AggregateID != apptID(2) {
		t.Fatalf("Drain = %d, %v", n, err)
	}
	if n != 1 || calls != 2 {
		t.Fatalf("published=%d calls=%d: the row before the failure goes, the rows after stay untouched", n, calls)
	}
	if c := pgtest.Count(t, pool, "outbox"); c != 2 {
		t.Fatalf("outbox = %d, want 2 (the failed row and the one after it)", c)
	}
	var attempts int
	var inFuture bool
	if err := pool.QueryRow(context.Background(), `SELECT attempts, next_attempt_at > now() FROM outbox ORDER BY id LIMIT 1`).Scan(&attempts, &inFuture); err != nil || attempts != 1 || !inFuture {
		t.Fatalf("failed row attempts=%d backoff=%v %v", attempts, inFuture, err)
	}
	// The failed row is backing off, so the next pass does not claim it: it
	// claims only the row that was left untouched (the fleet's deliberate
	// ordering trade-off, bounded by the attempt cap).
	var seen []string
	n, err = store.Drain(context.Background(), 10, 0, func(_ context.Context, r outbox.Row) error { seen = append(seen, r.Fact.AggregateID); return nil })
	if err != nil || n != 1 || len(seen) != 1 || seen[0] != apptID(3) {
		t.Fatalf("a row in backoff must not be claimed: n=%d seen=%v err=%v", n, seen, err)
	}
	if c := pgtest.Count(t, pool, "outbox"); c != 1 {
		t.Fatalf("outbox = %d, want only the backing-off row", c)
	}
}

func TestDrainExcludesRowsAtTheAttemptCap(t *testing.T) {
	pool := pgtest.NewPool(t)
	publishBooked(t, pool, 1)
	store, ctx := postgres.NewOutboxStore(pool), context.Background()
	if _, err := store.Drain(ctx, 10, 2, func(context.Context, outbox.Row) error { return errors.New("poison") }); err == nil {
		t.Fatal("expected the send failure")
	}
	if _, err := pool.Exec(ctx, `UPDATE outbox SET next_attempt_at = now() - interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	var sf *outbox.SendFailureError
	_, err := store.Drain(ctx, 10, 2, func(context.Context, outbox.Row) error { return errors.New("poison") })
	if !errors.As(err, &sf) || sf.Attempts != 2 {
		t.Fatalf("second failure = %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE outbox SET next_attempt_at = now() - interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	called := false
	n, err := store.Drain(ctx, 10, 2, func(context.Context, outbox.Row) error { called = true; return nil })
	if err != nil || n != 0 || called {
		t.Fatalf("a row at the cap is excluded from claims: %d %v called=%v", n, err, called)
	}
	if c := pgtest.Count(t, pool, "outbox"); c != 1 {
		t.Fatalf("the capped row must stay in the outbox for inspection, outbox = %d", c)
	}
	// an operator re-arms it
	if _, err := pool.Exec(ctx, `UPDATE outbox SET attempts = 0, next_attempt_at = now()`); err != nil {
		t.Fatal(err)
	}
	if n, err = store.Drain(ctx, 10, 2, ok); err != nil || n != 1 {
		t.Fatalf("re-armed row = %d, %v", n, err)
	}
}

func TestDrainHandsAPoisonPayloadOverWithAnEmptyEvent(t *testing.T) {
	pool := pgtest.NewPool(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `INSERT INTO outbox (event_id, aggreg_type, aggreg_id, payload, occurred_at) VALUES ('poison-1', 'dockappointment', 'appt-x', '"not an envelope"', now())`); err != nil {
		t.Fatal(err)
	}
	var got outbox.Row
	n, err := postgres.NewOutboxStore(pool).Drain(ctx, 10, 0, func(_ context.Context, r outbox.Row) error { got = r; return nil })
	if err != nil || n != 1 {
		t.Fatalf("Drain = %d, %v", n, err)
	}
	if got.Fact.Event != "" || string(got.RawPayload) != `"not an envelope"` || got.Fact.AggregateID != "appt-x" || got.Fact.EventID != "poison-1" {
		t.Fatalf("poison row = %+v", got)
	}
}

func TestOverlappingDrainsClaimDisjointRows(t *testing.T) {
	pool := pgtest.NewPool(t)
	for n := 1; n <= 4; n++ {
		publishBooked(t, pool, n)
	}
	store, ctx := postgres.NewOutboxStore(pool), context.Background()
	claimed, release, done := make(chan struct{}, 1), make(chan struct{}), make(chan error, 1)
	go func() {
		_, err := store.Drain(ctx, 2, 0, func(context.Context, outbox.Row) error {
			select {
			case claimed <- struct{}{}:
			default:
			}
			<-release
			return nil
		})
		done <- err
	}()
	<-claimed // the first drain holds rows 1 and 2
	var seen []string
	n, err := store.Drain(ctx, 10, 0, func(_ context.Context, r outbox.Row) error { seen = append(seen, r.Fact.AggregateID); return nil })
	if err != nil || n != 2 || seen[0] != apptID(3) || seen[1] != apptID(4) {
		t.Fatalf("the second drain must skip the locked rows: n=%d seen=%v err=%v", n, seen, err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if c := pgtest.Count(t, pool, "outbox"); c != 0 {
		t.Fatalf("outbox = %d", c)
	}
}
