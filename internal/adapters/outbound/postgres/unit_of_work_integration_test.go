//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/claudioed/dock-scheduling/internal/adapters/outbound/postgres"
	"github.com/claudioed/dock-scheduling/internal/application/ports"
	"github.com/claudioed/dock-scheduling/internal/domain/dockappointment"
	"github.com/claudioed/dock-scheduling/internal/testing/pgtest"
)

// This file proves the UnitOfWork really is a unit: an appointment save and
// its outbox rows commit together, roll back together, a nested Do joins the
// outer transaction, a refused Save does not poison the transaction, and the
// per-door advisory lock plus the exclusion constraint keep one door
// single-booked under concurrency.

func TestUnitOfWorkCommitsAppointmentAndOutboxTogether(t *testing.T) {
	pool := pgtest.NewPool(t)
	repo, pub, uow := postgres.NewAppointmentRepo(pool), postgres.NewOutboxPublisher(pool), postgres.NewUnitOfWork(pool)
	a, events := booked(t, 1, "D1", 0)
	err := uow.Do(context.Background(), func(ctx context.Context) error {
		if err := repo.Save(ctx, a, 0); err != nil {
			return err
		}
		return pub.PublishAppointmentEvents(ctx, events...)
	})
	if err != nil {
		t.Fatal(err)
	}
	if n := pgtest.Count(t, pool, "dock_appointments"); n != 1 {
		t.Fatalf("dock_appointments = %d, want 1", n)
	}
	if n := pgtest.Count(t, pool, "outbox"); n != 1 {
		t.Fatalf("outbox = %d, want 1 (DockAppointmentBooked)", n)
	}
	if n := pgtest.Count(t, pool, "asn_claims"); n != 1 {
		t.Fatalf("asn_claims = %d, want 1", n)
	}
}

func TestUnitOfWorkRollbackUnWritesEverything(t *testing.T) {
	pool := pgtest.NewPool(t)
	repo, pub, uow := postgres.NewAppointmentRepo(pool), postgres.NewOutboxPublisher(pool), postgres.NewUnitOfWork(pool)
	burst := errors.New("consumer burst error")
	a, events := booked(t, 1, "D1", 0)
	err := uow.Do(context.Background(), func(ctx context.Context) error {
		if err := repo.Save(ctx, a, 0); err != nil {
			return err
		}
		if err := pub.PublishAppointmentEvents(ctx, events...); err != nil {
			return err
		}
		return burst
	})
	if !errors.Is(err, burst) {
		t.Fatalf("Do returned %v, want the fn error unchanged", err)
	}
	for _, table := range []string{"dock_appointments", "outbox", "asn_claims"} {
		if n := pgtest.Count(t, pool, table); n != 0 {
			t.Fatalf("%s = %d, want 0 (rolled back together)", table, n)
		}
	}
}

func TestNestedDoJoinsTheOuterTransaction(t *testing.T) {
	pool := pgtest.NewPool(t)
	repo, uow := postgres.NewAppointmentRepo(pool), postgres.NewUnitOfWork(pool)
	ctx := context.Background()
	a, _ := booked(t, 1, "D1", 0)
	err := uow.Do(ctx, func(outer context.Context) error {
		if err := uow.Do(outer, func(inner context.Context) error { return repo.Save(inner, a, 0) }); err != nil {
			return err
		}
		other, err := pool.Acquire(ctx)
		if err != nil {
			return err
		}
		defer other.Release()
		var visible int
		if err := other.QueryRow(ctx, `SELECT count(*) FROM dock_appointments`).Scan(&visible); err != nil {
			return err
		}
		if visible != 0 {
			return errors.New("the nested Do committed early: the row is visible before the outer commit")
		}
		return errors.New("roll the outer unit back on purpose")
	})
	if err == nil || errors.Is(err, context.Canceled) {
		t.Fatalf("Do = %v, want the deliberate rollback error", err)
	}
	if n := pgtest.Count(t, pool, "dock_appointments"); n != 0 {
		t.Fatalf("dock_appointments = %d, want 0: a joined nested unit must not survive the outer rollback", n)
	}
}

func TestARefusedSaveDoesNotPoisonTheUnitOfWork(t *testing.T) {
	pool := pgtest.NewPool(t)
	repo, uow := postgres.NewAppointmentRepo(pool), postgres.NewUnitOfWork(pool)
	first, _ := booked(t, 1, "D1", 0)
	clash, _ := booked(t, 2, "D1", 0)
	other, _ := booked(t, 3, "D2", 0)
	err := uow.Do(context.Background(), func(ctx context.Context) error {
		if err := repo.Save(ctx, first, 0); err != nil {
			return err
		}
		if err := repo.Save(ctx, clash, 0); !errors.Is(err, dockappointment.ErrDoorDoubleBooked) {
			return errors.New("the clashing save must be refused with ErrDoorDoubleBooked")
		}
		// the transaction is still usable after the refusal
		return repo.Save(ctx, other, 0)
	})
	if err != nil {
		t.Fatal(err)
	}
	if n := pgtest.Count(t, pool, "dock_appointments"); n != 2 {
		t.Fatalf("dock_appointments = %d, want 2 (first and other; the clash was refused)", n)
	}
}

// Without the advisory lock, the exclusion constraint alone keeps one winner.
func TestExclusionConstraintBackstopsConcurrentBookings(t *testing.T) {
	pool := pgtest.NewPool(t)
	repo, uow := postgres.NewAppointmentRepo(pool), postgres.NewUnitOfWork(pool)
	const workers = 8
	results := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 1; i <= workers; i++ {
		a, _ := booked(t, i, "D1", 0)
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- uow.Do(context.Background(), func(ctx context.Context) error { return repo.Save(ctx, a, 0) })
		}()
	}
	wg.Wait()
	close(results)
	wins, doubleBooked := 0, 0
	for err := range results {
		switch {
		case err == nil:
			wins++
		case errors.Is(err, dockappointment.ErrDoorDoubleBooked):
			doubleBooked++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if wins != 1 || doubleBooked != workers-1 {
		t.Fatalf("wins=%d doubleBooked=%d, want 1 and %d", wins, doubleBooked, workers-1)
	}
}

// With the per-door advisory lock, check-then-insert is serialised: the
// losers see the winner and book nothing, and nobody hits the constraint.
func TestPerDoorAdvisoryLockSerialisesCheckThenInsert(t *testing.T) {
	pool := pgtest.NewPool(t)
	repo, uow := postgres.NewAppointmentRepo(pool), postgres.NewUnitOfWork(pool)
	const workers = 8
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for i := 1; i <= workers; i++ {
		a, _ := booked(t, i, "D1", 0)
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- uow.Do(context.Background(), func(ctx context.Context) error {
				if err := repo.LockDoor(ctx, "D1"); err != nil {
					return err
				}
				active, err := repo.ListActiveOnDoor(ctx, "D1")
				if err != nil {
					return err
				}
				if len(active) > 0 {
					return ports.ErrNotFound // stands for "the check refused the booking"
				}
				return repo.Save(ctx, a, 0)
			})
		}()
	}
	wg.Wait()
	close(errs)
	wins, refused := 0, 0
	for err := range errs {
		switch {
		case err == nil:
			wins++
		case errors.Is(err, ports.ErrNotFound):
			refused++
		default:
			t.Fatalf("the lock must keep everyone off the constraint, got: %v", err)
		}
	}
	if wins != 1 || refused != workers-1 {
		t.Fatalf("wins=%d refused=%d, want 1 and %d", wins, refused, workers-1)
	}
}
