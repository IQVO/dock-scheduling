//go:build integration

package analyticsstore_test

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/claudioed/dock-scheduling/internal/adapters/outbound/analyticsstore"
	"github.com/claudioed/dock-scheduling/internal/analytics/adherence"
	"github.com/claudioed/dock-scheduling/internal/application/repository"
	"github.com/claudioed/dock-scheduling/internal/testing/pgtest"
)

var (
	day   = time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	ctx   = context.Background()
	evSeq atomic.Int64
)

// world is a fresh analytical database (only the analytics migrations, never the
// OLTP ones) with its writer and reader.
type world struct {
	t    *testing.T
	url  string
	proj *analyticsstore.Projection
	rd   *analyticsstore.Reader
}

func newWorld(t *testing.T) *world {
	t.Helper()
	url := pgtest.EmptyURL(t)
	if err := analyticsstore.RunMigrations(url); err != nil {
		t.Fatalf("migrations: %v", err)
	}
	wp, err := analyticsstore.NewPool(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(wp.Close)
	rp, err := analyticsstore.NewReadOnlyPool(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(rp.Close)
	return &world{t: t, url: url, proj: analyticsstore.NewProjection(wp), rd: analyticsstore.NewReader(rp)}
}

// apply folds an event with a fresh id and requires it to be applied.
func (w *world) apply(e adherence.Event) {
	w.t.Helper()
	e.EventID = fmt.Sprintf("ev-%d", evSeq.Add(1))
	if e.At.IsZero() {
		e.At = day
	}
	if applied, err := w.proj.Apply(ctx, e); err != nil || !applied {
		w.t.Fatalf("Apply(%s %s) = %v, %v; want applied", e.Kind, e.AppointmentID, applied, err)
	}
}

func booked(id, site, scac string, start time.Time) adherence.Event {
	return adherence.Event{Kind: adherence.Booked, AppointmentID: id, SiteID: site, CarrierSCAC: scac,
		WindowStart: start, WindowEnd: start.Add(time.Hour), Version: 1, At: start.Add(-48 * time.Hour)}
}

func checkedIn(id, site string, status adherence.ArrivalStatus, at time.Time) adherence.Event {
	return adherence.Event{Kind: adherence.CheckedIn, AppointmentID: id, SiteID: site, ArrivalStatus: status, CheckedInAt: at, At: at}
}

func completed(id, site string, at time.Time) adherence.Event {
	return adherence.Event{Kind: adherence.Completed, AppointmentID: id, SiteID: site, CompletedAt: at, At: at}
}

func cancelled(id, site string, at time.Time) adherence.Event {
	return adherence.Event{Kind: adherence.Cancelled, AppointmentID: id, SiteID: site, Reason: "CARRIER_REQUEST", At: at}
}

func noShow(id, site, scac string, windowEnd time.Time) adherence.Event {
	return adherence.Event{Kind: adherence.NoShow, AppointmentID: id, SiteID: site, CarrierSCAC: scac, WindowEnd: windowEnd, At: windowEnd.Add(time.Hour)}
}

func (w *world) report(site, scac string, from, to time.Time) []repository.AdherenceRow {
	w.t.Helper()
	rows, err := w.rd.ScheduleAdherence(ctx, repository.AdherenceQuery{SiteID: site, CarrierSCAC: scac, From: from, To: to})
	if err != nil {
		w.t.Fatalf("ScheduleAdherence: %v", err)
	}
	return rows
}

func wantRows(t *testing.T, got []repository.AdherenceRow, want ...repository.AdherenceRow) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("rows = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestMigrationsUpDownUp(t *testing.T) {
	url := pgtest.EmptyURL(t)
	for range 2 {
		if err := analyticsstore.RunMigrations(url); err != nil {
			t.Fatalf("up (twice is a no-op): %v", err)
		}
	}
	m, err := analyticsstore.Migrator(url)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Down(); err != nil {
		t.Fatalf("down: %v", err)
	}
	_, _ = m.Close()
	if err := analyticsstore.RunMigrations(url); err != nil {
		t.Fatalf("up again after down: %v", err)
	}
}

// Every outcome of the adherence rule (ADR 0006 section 4), per site and carrier.
func TestScheduleAdherenceCountsEveryOutcome(t *testing.T) {
	w := newWorld(t)
	start := day.Add(8 * time.Hour)
	// WH1 / ACMF: one of each outcome, plus three that do not count.
	w.apply(booked("a-ontime", "WH1", "ACMF", start))
	w.apply(checkedIn("a-ontime", "WH1", adherence.OnTime, start.Add(5*time.Minute)))
	w.apply(booked("a-early", "WH1", "ACMF", start))
	w.apply(checkedIn("a-early", "WH1", adherence.Early, start.Add(-time.Hour)))
	w.apply(booked("a-late", "WH1", "ACMF", start))
	w.apply(checkedIn("a-late", "WH1", adherence.Late, start.Add(40*time.Minute)))
	w.apply(booked("a-noshow", "WH1", "ACMF", start))
	w.apply(noShow("a-noshow", "WH1", "ACMF", start.Add(time.Hour)))
	w.apply(booked("a-implicit", "WH1", "ACMF", start)) // X6: completed with no check-in = ON_TIME
	w.apply(completed("a-implicit", "WH1", start.Add(30*time.Minute)))
	w.apply(booked("a-open", "WH1", "ACMF", start)) // still BOOKED: no outcome yet
	w.apply(booked("a-cancelled", "WH1", "ACMF", start))
	w.apply(cancelled("a-cancelled", "WH1", start.Add(-time.Hour))) // cancelled before anyone arrived
	// Cancelled AFTER the check-in: the arrival happened, it counts by its status.
	w.apply(booked("a-cancel-after", "WH1", "ACMF", start))
	w.apply(checkedIn("a-cancel-after", "WH1", adherence.Late, start.Add(time.Hour)))
	w.apply(cancelled("a-cancel-after", "WH1", start.Add(2*time.Hour)))
	// Another carrier, one with no SCAC (a legacy import), another site.
	w.apply(booked("b-ontime", "WH1", "ZZZZ", start))
	w.apply(checkedIn("b-ontime", "WH1", adherence.OnTime, start))
	w.apply(booked("c-legacy", "WH1", "", start))
	w.apply(checkedIn("c-legacy", "WH1", adherence.Late, start.Add(time.Hour)))
	w.apply(booked("d-wh2", "WH2", "ACMF", start))
	w.apply(checkedIn("d-wh2", "WH2", adherence.Early, start.Add(-time.Hour)))

	from, to := day, day.Add(24*time.Hour)
	wantRows(t, w.report("", "", from, to),
		repository.AdherenceRow{SiteID: "WH1", CarrierSCAC: "-", Late: 1},
		repository.AdherenceRow{SiteID: "WH1", CarrierSCAC: "ACMF", OnTime: 2, Early: 1, Late: 2, NoShow: 1},
		repository.AdherenceRow{SiteID: "WH1", CarrierSCAC: "ZZZZ", OnTime: 1},
		repository.AdherenceRow{SiteID: "WH2", CarrierSCAC: "ACMF", Early: 1},
	)
	t.Run("narrowed to a site", func(t *testing.T) {
		if rows := w.report("WH2", "", from, to); len(rows) != 1 || rows[0].SiteID != "WH2" {
			t.Fatalf("rows = %+v, want only WH2", rows)
		}
	})
	t.Run("narrowed to a carrier", func(t *testing.T) {
		rows := w.report("", "ACMF", from, to)
		wantRows(t, rows,
			repository.AdherenceRow{SiteID: "WH1", CarrierSCAC: "ACMF", OnTime: 2, Early: 1, Late: 2, NoShow: 1},
			repository.AdherenceRow{SiteID: "WH2", CarrierSCAC: "ACMF", Early: 1})
	})
	t.Run("narrowed to the carrier-less rows", func(t *testing.T) {
		wantRows(t, w.report("", "-", from, to), repository.AdherenceRow{SiteID: "WH1", CarrierSCAC: "-", Late: 1})
	})
	t.Run("an unknown site or carrier has no rows, never a nil slice", func(t *testing.T) {
		if rows := w.report("NOPE", "", from, to); rows == nil || len(rows) != 0 {
			t.Fatalf("rows = %#v, want an empty non-nil slice", rows)
		}
	})
}

// The range is half-open on the window start; a reschedule moves the appointment
// to the day of its CURRENT window.
func TestScheduleAdherenceRangeFollowsTheCurrentWindow(t *testing.T) {
	w := newWorld(t)
	start := day.Add(8 * time.Hour)
	w.apply(booked("r-1", "WH1", "ACMF", start))
	w.apply(checkedIn("r-1", "WH1", adherence.OnTime, start))
	w.apply(booked("r-2", "WH1", "ACMF", day.Add(24*time.Hour))) // exactly at the end: excluded
	w.apply(checkedIn("r-2", "WH1", adherence.OnTime, day.Add(24*time.Hour)))

	wantRows(t, w.report("", "", day, day.Add(24*time.Hour)), repository.AdherenceRow{SiteID: "WH1", CarrierSCAC: "ACMF", OnTime: 1})
	wantRows(t, w.report("", "", day.Add(24*time.Hour), day.Add(48*time.Hour)), repository.AdherenceRow{SiteID: "WH1", CarrierSCAC: "ACMF", OnTime: 1})

	w.apply(adherence.Event{Kind: adherence.Rescheduled, AppointmentID: "r-1", SiteID: "WH1",
		WindowStart: start.Add(48 * time.Hour), WindowEnd: start.Add(49 * time.Hour), Version: 2, At: day})
	wantRows(t, w.report("", "", day, day.Add(24*time.Hour)))
	wantRows(t, w.report("", "", day.Add(48*time.Hour), day.Add(72*time.Hour)), repository.AdherenceRow{SiteID: "WH1", CarrierSCAC: "ACMF", OnTime: 1})
}

// Events keyed by the same appointment are ordered on a partition, but the
// projection does not rely on it: replays and reordering change nothing.
func TestProjectionToleratesReorderingAndReplays(t *testing.T) {
	w := newWorld(t)
	start := day.Add(8 * time.Hour)

	// A reschedule (version 2) overtakes the booking (version 1): the newer window stays.
	w.apply(adherence.Event{Kind: adherence.Rescheduled, AppointmentID: "o-1", SiteID: "WH1",
		WindowStart: start.Add(24 * time.Hour), WindowEnd: start.Add(25 * time.Hour), Version: 2})
	w.apply(booked("o-1", "WH1", "ACMF", start))
	// The check-in lands after the completion that made it implicit: the explicit status wins.
	w.apply(completed("o-2", "WH1", start.Add(30*time.Minute)))
	w.apply(booked("o-2", "WH1", "ACMF", start))
	w.apply(checkedIn("o-2", "WH1", adherence.Late, start.Add(20*time.Minute)))
	// The no-show lands before the booking: the carrier and window come from whichever carries them.
	w.apply(noShow("o-3", "WH1", "", start.Add(time.Hour)))
	w.apply(booked("o-3", "WH1", "ACMF", start))

	wantRows(t, w.report("", "", day, day.Add(24*time.Hour)),
		repository.AdherenceRow{SiteID: "WH1", CarrierSCAC: "ACMF", Late: 1, NoShow: 1})
	wantRows(t, w.report("", "", day.Add(24*time.Hour), day.Add(48*time.Hour))) // o-1 has no outcome yet
	w.apply(checkedIn("o-1", "WH1", adherence.Early, start.Add(23*time.Hour)))
	wantRows(t, w.report("", "", day.Add(24*time.Hour), day.Add(48*time.Hour)),
		repository.AdherenceRow{SiteID: "WH1", CarrierSCAC: "ACMF", Early: 1})
}

func TestApplyIsIdempotentPerEventID(t *testing.T) {
	w := newWorld(t)
	start := day.Add(8 * time.Hour)
	b := booked("i-1", "WH1", "ACMF", start)
	b.EventID = "same-id"
	for i, want := range []bool{true, false, false} {
		applied, err := w.proj.Apply(ctx, b)
		if err != nil || applied != want {
			t.Fatalf("Apply #%d = %v, %v; want applied=%v", i+1, applied, err, want)
		}
	}
	c := checkedIn("i-1", "WH1", adherence.OnTime, start)
	c.EventID = "same-checkin"
	if applied, err := w.proj.Apply(ctx, c); err != nil || !applied {
		t.Fatalf("first check-in = %v, %v", applied, err)
	}
	// The same id again, even carrying a different status, changes nothing.
	c.ArrivalStatus = adherence.Late
	if applied, err := w.proj.Apply(ctx, c); err != nil || applied {
		t.Fatalf("replayed check-in = %v, %v; want not applied", applied, err)
	}
	wantRows(t, w.report("", "", day, day.Add(24*time.Hour)), repository.AdherenceRow{SiteID: "WH1", CarrierSCAC: "ACMF", OnTime: 1})
}

func TestApplyRefusesAnInvalidEventBeforeTouchingTheDatabase(t *testing.T) {
	w := newWorld(t)
	_, err := w.proj.Apply(ctx, adherence.Event{Kind: adherence.CheckedIn, EventID: "bad", At: day, AppointmentID: "x", SiteID: "WH1", ArrivalStatus: "SOONISH", CheckedInAt: day})
	if !errors.Is(err, adherence.ErrInvalidEvent) {
		t.Fatalf("Apply = %v, want ErrInvalidEvent", err)
	}
}

// A value Postgres refuses deterministically (a NUL byte is SQLSTATE 22021) is
// ErrRejected, so the consumer dead-letters it; the claim rolled back with it.
func TestApplyClassifiesADeterministicRefusalAsRejected(t *testing.T) {
	w := newWorld(t)
	e := booked("bad\x00id", "WH1", "ACMF", day.Add(8*time.Hour))
	e.EventID = "nul-id"
	if _, err := w.proj.Apply(ctx, e); !errors.Is(err, adherence.ErrRejected) {
		t.Fatalf("Apply = %v, want ErrRejected", err)
	}
	e.AppointmentID = "good"
	if applied, err := w.proj.Apply(ctx, e); err != nil || !applied {
		t.Fatalf("the rolled-back claim must not block the id: %v, %v", applied, err)
	}
}

func TestApplyIsTransientWhenTheDatabaseIsGone(t *testing.T) {
	url := pgtest.EmptyURL(t)
	if err := analyticsstore.RunMigrations(url); err != nil {
		t.Fatal(err)
	}
	pool, err := analyticsstore.NewPool(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	pool.Close() // every later call fails with a pool-closed error, not a Postgres refusal
	e := booked("t-1", "WH1", "ACMF", day.Add(8*time.Hour))
	e.EventID = "t-1"
	_, err = analyticsstore.NewProjection(pool).Apply(ctx, e)
	if err == nil || errors.Is(err, adherence.ErrRejected) || errors.Is(err, adherence.ErrInvalidEvent) {
		t.Fatalf("Apply on a closed pool = %v, want a plain transient error", err)
	}
}

func TestFreshnessIsTheNewestAppliedEventTime(t *testing.T) {
	w := newWorld(t)
	if asOf, err := w.rd.Freshness(ctx); err != nil || asOf != nil {
		t.Fatalf("Freshness on an empty projection = %v, %v; want nil", asOf, err)
	}
	newest := day.Add(10 * time.Hour)
	w.apply(adherence.Event{Kind: adherence.Cancelled, AppointmentID: "f-1", SiteID: "WH1", At: day.Add(2 * time.Hour)})
	w.apply(adherence.Event{Kind: adherence.Cancelled, AppointmentID: "f-2", SiteID: "WH1", At: newest})
	w.apply(adherence.Event{Kind: adherence.Cancelled, AppointmentID: "f-3", SiteID: "WH1", At: day.Add(5 * time.Hour)})
	asOf, err := w.rd.Freshness(ctx)
	if err != nil || asOf == nil || !asOf.Equal(newest) || asOf.Location() != time.UTC {
		t.Fatalf("Freshness = %v, %v; want %s in UTC", asOf, err, newest)
	}
}

// The reader pool refuses writes by itself, and a role with SELECT only does too.
func TestTheReaderCannotWrite(t *testing.T) {
	w := newWorld(t)
	w.apply(booked("w-1", "WH1", "ACMF", day.Add(8*time.Hour)))

	owner, err := analyticsstore.NewReadOnlyPool(ctx, w.url) // the OWNER's DSN, read-only by default
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	if _, err := owner.Exec(ctx, `DELETE FROM appointment_facts`); err == nil {
		t.Fatal("the read-only pool must refuse a write even for a role that could")
	}

	roleURL := pgtest.AsRole(t, w.url, fmt.Sprintf("ds_reports_%d", evSeq.Add(1)),
		"SELECT ON ALL TABLES IN SCHEMA public")
	rolePool, err := analyticsstore.NewPool(ctx, roleURL) // a plain (writable) pool: only the grants stand in the way
	if err != nil {
		t.Fatal(err)
	}
	defer rolePool.Close()
	if _, err := rolePool.Exec(ctx, `DELETE FROM appointment_facts`); err == nil {
		t.Fatal("a SELECT-only role must not be able to delete")
	}
	rows, err := analyticsstore.NewReader(rolePool).ScheduleAdherence(ctx, repository.AdherenceQuery{From: day, To: day.Add(24 * time.Hour)})
	if err != nil || len(rows) != 0 {
		t.Fatalf("the SELECT-only role reads the report: %v, %v", rows, err)
	}
}
