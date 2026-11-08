package repocontract

import (
	"context"
	"errors"
	"testing"

	"github.com/claudioed/dock-scheduling/internal/application/ports"
	"github.com/claudioed/dock-scheduling/internal/application/repository"
)

// runClaims: the inbox idempotency — one claim per processed-event id.
func runClaims(t *testing.T, f Factory) {
	ctx := context.Background()
	e := f(t)
	first, err := e.Processed.Claim(ctx, "evt-1")
	again, err2 := e.Processed.Claim(ctx, "evt-1")
	other, err3 := e.Processed.Claim(ctx, "evt-2")
	if err != nil || err2 != nil || err3 != nil || !first || again || !other {
		t.Fatalf("claims: %v %v %v / %v %v %v", first, again, other, err, err2, err3)
	}
}

// runIdempotency: the stored replay of an Idempotency-Key answer; the first
// writer wins.
func runIdempotency(t *testing.T, f Factory) {
	ctx := context.Background()
	e := f(t)
	if _, found, err := e.Idempotency.Lookup(ctx, "k"); found || err != nil {
		t.Fatalf("empty lookup: %v %v", found, err)
	}
	rec := repository.IdempotencyRecord{Key: "k", Fingerprint: "fp", Status: 201, ContentType: "application/json", Body: []byte(`{"a":1}`), CreatedAt: base}
	if err := e.Idempotency.Save(ctx, rec); err != nil {
		t.Fatal(err)
	}
	loser := rec
	loser.Fingerprint, loser.Status, loser.Body = "other", 200, []byte("x")
	if err := e.Idempotency.Save(ctx, loser); err != nil {
		t.Fatal(err)
	}
	got, found, err := e.Idempotency.Lookup(ctx, "k")
	if err != nil || !found || got.Fingerprint != "fp" || got.Status != 201 || string(got.Body) != `{"a":1}` || got.ContentType != "application/json" || !got.CreatedAt.Equal(base) {
		t.Fatalf("first writer must win: %+v %v %v", got, found, err)
	}
}

// runRollback: a unit of work that fails rolls back everything, and nested Do
// calls join the outer transaction.
func runRollback(t *testing.T, f Factory) {
	ctx := context.Background()
	e := f(t)
	boom := errors.New("boom")
	err := e.UoW.Do(ctx, func(ctx context.Context) error {
		if _, err := e.Processed.Claim(ctx, "evt-rb"); err != nil {
			return err
		}
		if err := e.Appointments.Save(ctx, book(t, 1, "D1", 0), 0); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("Do must return fn's error unchanged: %v", err)
	}
	if _, err := e.Appointments.Get(ctx, apptID(1)); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("a rolled back save must not persist: %v", err)
	}
	if first, _ := e.Processed.Claim(ctx, "evt-rb"); !first {
		t.Fatal("a rolled back claim must be un-claimed")
	}
	err = e.UoW.Do(ctx, func(ctx context.Context) error {
		return e.UoW.Do(ctx, func(ctx context.Context) error { return e.Appointments.Save(ctx, book(t, 2, "D1", 0), 0) })
	})
	if err != nil {
		t.Fatalf("nested Do joins: %v", err)
	}
	if _, err := e.Appointments.Get(ctx, apptID(2)); err != nil {
		t.Fatalf("committed save missing: %v", err)
	}
	if err := e.Events.PublishAppointmentEvents(ctx); err != nil {
		t.Fatal(err)
	}
}
