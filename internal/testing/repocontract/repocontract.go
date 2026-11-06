// Package repocontract is the behavioural contract every implementation of the
// outbound ports must satisfy; the in-memory store and the Postgres adapter
// both run it, so they cannot drift apart.
package repocontract

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/claudioed/dock-scheduling/internal/application/ports"
	"github.com/claudioed/dock-scheduling/internal/application/repository"
	"github.com/claudioed/dock-scheduling/internal/domain/dockappointment"
	"github.com/claudioed/dock-scheduling/internal/domain/dockcalendar"
	"github.com/claudioed/dock-scheduling/internal/domain/shared"
)

// Env is one fresh, empty implementation under test.
type Env struct {
	Appointments ports.AppointmentRepo
	Calendars    ports.CalendarRepo
	Doors        ports.DoorView
	Asns         ports.AsnView
	Cutoffs      ports.CutoffView
	Processed    ports.ProcessedEvents
	Idempotency  ports.IdempotencyStore
	Events       ports.EventPublisher
	UoW          ports.UnitOfWork
}

// Factory builds a fresh Env per test.
type Factory func(t *testing.T) Env

// Run executes the whole contract.
func Run(t *testing.T, f Factory) {
	t.Helper()
	for name, fn := range map[string]func(*testing.T, Factory){
		"appointments": runAppointments, "paging": runPaging, "backstops": runBackstops, "active": runActiveQueries, "ended-and-lock": runEndedAndLock, "active-by-asn": runActiveByAsnAndDoor, "calendars": runCalendars,
		"calendar-updates": runCalendarUpdates, "doors": runDoors, "door-updates": runDoorUpdates, "asns": runAsns, "cutoffs": runCutoffs, "claims": runClaims, "idempotency": runIdempotency, "rollback": runRollback,
	} {
		t.Run(name, func(t *testing.T) { fn(t, f) })
	}
}

var base = time.Date(2030, 1, 7, 8, 0, 0, 0, time.UTC) // a Monday

func apptID(n int) string { return fmt.Sprintf("appt-%08x-e89b-12d3-a456-426614174000", n) }

// book builds a BOOKED INBOUND appointment on door starting hoursFromBase after base for one hour.
func book(t *testing.T, n int, door string, hoursFromBase int, asns ...string) *dockappointment.DockAppointment {
	t.Helper()
	if len(asns) == 0 {
		asns = []string{fmt.Sprintf("ASN-%d", n)}
	}
	start := base.Add(time.Duration(hoursFromBase) * time.Hour)
	a, _, err := dockappointment.Book(dockappointment.BookParams{
		ID: apptID(n), SiteID: "WH1", DoorCode: door, Direction: "INBOUND", Carrier: "ACME", CarrierSCAC: "ACMF",
		WindowStart: start, WindowEnd: start.Add(time.Hour), ASNNumbers: asns, Now: base.Add(-24 * time.Hour),
	})
	if err != nil {
		t.Fatalf("Book: %v", err)
	}
	return a
}

func mustSave(t *testing.T, e Env, a *dockappointment.DockAppointment, expected int64) {
	t.Helper()
	if err := e.Appointments.Save(context.Background(), a, expected); err != nil {
		t.Fatalf("Save(%s, %d): %v", a.ID(), expected, err)
	}
}

func runAppointments(t *testing.T, f Factory) {
	ctx := context.Background()
	e := f(t)
	a := book(t, 1, "D1", 0)
	mustSave(t, e, a, 0)
	got, err := e.Appointments.Get(ctx, apptID(1))
	if err != nil || got.Snapshot().ID != a.Snapshot().ID || got.State() != dockappointment.Booked || got.Version() != 1 || got.ASNNumbers()[0] != "ASN-1" {
		t.Fatalf("Get = %+v, %v", got, err)
	}
	if _, err := e.Appointments.GetForUpdate(ctx, apptID(1)); err != nil {
		t.Fatalf("GetForUpdate: %v", err)
	}
	if _, err := e.Appointments.Get(ctx, apptID(99)); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("Get missing: %v", err)
	}
	if err := e.Appointments.Save(ctx, book(t, 1, "D1", 0), 0); !errors.Is(err, dockappointment.ErrConcurrentUpdate) {
		t.Fatalf("a second insert of one id: %v", err)
	}
	// update guarded by the loaded version
	if _, err := got.Cancel(dockappointment.SiteRequest, base); err != nil {
		t.Fatal(err)
	}
	if err := e.Appointments.Save(ctx, got, 5); !errors.Is(err, dockappointment.ErrConcurrentUpdate) {
		t.Fatalf("stale expected version: %v", err)
	}
	mustSave(t, e, got, 1)
	again, _ := e.Appointments.Get(ctx, apptID(1))
	if again.State() != dockappointment.Cancelled || again.Version() != 2 || again.CancellationReason() != dockappointment.SiteRequest {
		t.Fatalf("after update: %+v", again.Snapshot())
	}
	if err := e.Appointments.Save(ctx, book(t, 2, "D1", 0), 3); !errors.Is(err, dockappointment.ErrConcurrentUpdate) {
		t.Fatalf("update of a missing row: %v", err)
	}
	roundTripFields(t, e)
}

func roundTripFields(t *testing.T, e Env) {
	t.Helper()
	ctx := context.Background()
	a := book(t, 3, "D2", 4)
	if _, err := a.ReportEta(base.Add(10*time.Hour), "carrier", dockappointment.Rules{}, base); err != nil {
		t.Fatal(err)
	}
	mustSave(t, e, a, 0)
	got, err := e.Appointments.Get(ctx, apptID(3))
	if err != nil {
		t.Fatal(err)
	}
	s, w := got.Snapshot(), a.Snapshot()
	if s.AtRisk != w.AtRisk || s.Eta == nil || s.Eta.Source != "carrier" || !s.Eta.At.Equal(w.Eta.At) || !s.WindowStart.Equal(w.WindowStart) ||
		s.Carrier != w.Carrier || s.CarrierSCAC != w.CarrierSCAC || s.Provenance != w.Provenance || s.Direction != w.Direction || s.Version != w.Version {
		t.Fatalf("round trip lost fields:\n got %+v\nwant %+v", s, w)
	}
}

func runBackstops(t *testing.T, f Factory) {
	ctx := context.Background()
	e := f(t)
	mustSave(t, e, book(t, 1, "D1", 0, "ASN-A"), 0)
	if err := e.Appointments.Save(ctx, book(t, 2, "D1", 0, "ASN-B"), 0); !errors.Is(err, dockappointment.ErrDoorDoubleBooked) {
		t.Fatalf("same door, same window: %v", err)
	}
	mustSave(t, e, book(t, 3, "D1", 1, "ASN-C"), 0) // adjacent: [9,10) after [8,9)
	mustSave(t, e, book(t, 4, "D2", 0, "ASN-D"), 0) // other door
	if err := e.Appointments.Save(ctx, book(t, 5, "D5", 9, "ASN-A"), 0); !errors.Is(err, dockappointment.ErrAsnAlreadyScheduled) {
		t.Fatalf("an ASN on two active appointments: %v", err)
	}
	// a terminal appointment frees both the door window and its ASN
	a, _ := e.Appointments.Get(ctx, apptID(1))
	if _, err := a.Cancel(dockappointment.SiteRequest, base); err != nil {
		t.Fatal(err)
	}
	mustSave(t, e, a, 1)
	mustSave(t, e, book(t, 6, "D1", 0, "ASN-A"), 0)
}

// seedActive stores a cancelled appointment on D1/ASN-A and three active ones.
func seedActive(t *testing.T, e Env) {
	t.Helper()
	cancelled := book(t, 1, "D1", 0, "ASN-A")
	if _, err := cancelled.Cancel(dockappointment.SiteRequest, base); err != nil {
		t.Fatal(err)
	}
	mustSave(t, e, cancelled, 0)
	mustSave(t, e, book(t, 6, "D1", 0, "ASN-A"), 0)
	mustSave(t, e, book(t, 3, "D1", 1, "ASN-C"), 0)
	mustSave(t, e, book(t, 4, "D2", 0, "ASN-D"), 0)
}

func runActiveQueries(t *testing.T, f Factory) {
	ctx := context.Background()
	e := f(t)
	seedActive(t, e)
	ids, err := e.Appointments.ActiveIDsByASN(ctx, []string{"ASN-A", "ASN-D"}, apptID(4))
	if err != nil || len(ids) != 1 || ids[0] != apptID(6) {
		t.Fatalf("ActiveIDsByASN = %v, %v", ids, err)
	}
	bk, err := e.Appointments.ActiveBookings(ctx, "WH1", "D1")
	if err != nil || len(bk) != 2 || bk[0].AppointmentID != apptID(6) || bk[1].AppointmentID != apptID(3) {
		t.Fatalf("ActiveBookings(D1) = %+v, %v", bk, err)
	}
	if bk, _ = e.Appointments.ActiveBookings(ctx, "WH1", ""); len(bk) != 3 {
		t.Fatalf("ActiveBookings(site) = %d rows", len(bk))
	}
	if bk, _ = e.Appointments.ActiveBookings(ctx, "WH2", ""); len(bk) != 0 {
		t.Fatalf("ActiveBookings(other site) = %d rows", len(bk))
	}
}

func runActiveByAsnAndDoor(t *testing.T, f Factory) {
	ctx := context.Background()
	e := f(t)
	seedActive(t, e)
	byAsn, err := e.Appointments.ActiveByASN(ctx, "ASN-A")
	if err != nil || len(byAsn) != 1 || byAsn[0].ID().String() != apptID(6) {
		t.Fatalf("ActiveByASN = %v, %v", byAsn, err)
	}
	onDoor, err := e.Appointments.ListActiveOnDoor(ctx, "D1")
	if err != nil || len(onDoor) != 2 || onDoor[0].ID().String() != apptID(6) {
		t.Fatalf("ListActiveOnDoor = %v, %v", onDoor, err)
	}
}

func runEndedAndLock(t *testing.T, f Factory) {
	ctx := context.Background()
	e := f(t)
	seedActive(t, e)
	ended, err := e.Appointments.ListBookedEndedBy(ctx, base.Add(2*time.Hour), 10)
	if err != nil || len(ended) != 3 { // appt 6 [8,9), appt 3 [9,10), appt 4 [8,9); the one ending at 10 is included
		t.Fatalf("ListBookedEndedBy = %v, %v", ended, err)
	}
	if ended, _ = e.Appointments.ListBookedEndedBy(ctx, base.Add(time.Hour), 10); len(ended) != 2 {
		t.Fatalf("ListBookedEndedBy(boundary) = %v", ended)
	}
	if ended, _ = e.Appointments.ListBookedEndedBy(ctx, base.Add(2*time.Hour), 1); len(ended) != 1 {
		t.Fatalf("ListBookedEndedBy(limit) = %v", ended)
	}
	if err := e.Appointments.LockDoor(ctx, "D1"); err != nil {
		t.Fatalf("LockDoor: %v", err)
	}
}

func runPaging(t *testing.T, f Factory) {
	ctx := context.Background()
	e := f(t)
	for i := 1; i <= 5; i++ {
		d := "D1"
		if i%2 == 0 {
			d = "D2"
		}
		mustSave(t, e, book(t, i, d, i), 0)
	}
	risky := book(t, 6, "D3", 0)
	_, _ = risky.ReportEta(base.Add(48*time.Hour), "api", dockappointment.Rules{}, base)
	mustSave(t, e, risky, 0)
	var seen []string
	var after *repository.AppointmentAfter
	for {
		rows, err := e.Appointments.ListPage(ctx, repository.AppointmentFilter{}, after, 2)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range rows {
			seen = append(seen, r.ID().String())
		}
		if len(rows) < 2 {
			break
		}
		last := rows[len(rows)-1]
		after = &repository.AppointmentAfter{WindowStart: last.Window().Start(), ID: last.ID().String()}
	}
	want := []string{apptID(6), apptID(1), apptID(2), apptID(3), apptID(4), apptID(5)}
	if fmt.Sprint(seen) != fmt.Sprint(want) {
		t.Fatalf("paged order = %v; want %v", seen, want)
	}
	check := func(name string, flt repository.AppointmentFilter, wantIDs ...string) {
		rows, err := e.Appointments.ListPage(ctx, flt, nil, 50)
		var got []string
		for _, r := range rows {
			got = append(got, r.ID().String())
		}
		if err != nil || fmt.Sprint(got) != fmt.Sprint(wantIDs) {
			t.Errorf("%s: %v, %v; want %v", name, got, err, wantIDs)
		}
	}
	check("door", repository.AppointmentFilter{DoorCode: "D2"}, apptID(2), apptID(4))
	check("site", repository.AppointmentFilter{SiteID: "WH9"})
	check("state", repository.AppointmentFilter{State: "CANCELLED"})
	check("direction", repository.AppointmentFilter{Direction: "OUTBOUND"})
	check("scac", repository.AppointmentFilter{CarrierSCAC: "NOPE"})
	check("at risk", repository.AppointmentFilter{AtRiskOnly: true}, apptID(6))
	from, to := base.Add(2*time.Hour), base.Add(4*time.Hour) // overlaps [2,3), [3,4); [1,2) ends at from, [4,5) starts at to
	check("overlap range", repository.AppointmentFilter{From: from, To: to}, apptID(2), apptID(3))
}

func runCalendars(t *testing.T, f Factory) {
	ctx := context.Background()
	e := f(t)
	def, err := e.Calendars.Get(ctx, "WH1")
	if err != nil || def.Version() != 0 || def.Settings() != dockcalendar.DefaultSettings() {
		t.Fatalf("default calendar = %+v, %v", def, err)
	}
	if _, err := e.Calendars.GetForUpdate(ctx, "WH1"); err != nil {
		t.Fatal(err)
	}
	row, _ := dockcalendar.NewOperatingHours("D1", dockcalendar.Mon, "06:00", "22:00", "America/Sao_Paulo")
	if _, err := def.DeclareOperatingHours([]dockcalendar.OperatingHours{row}, nil, base); err != nil {
		t.Fatal(err)
	}
	if err := e.Calendars.Save(ctx, def, 5); !errors.Is(err, dockappointment.ErrConcurrentUpdate) {
		t.Fatalf("insert with a non-zero expected version: %v", err)
	}
	if err := e.Calendars.Save(ctx, def, 0); err != nil {
		t.Fatal(err)
	}
	if err := e.Calendars.Save(ctx, def, 0); !errors.Is(err, dockappointment.ErrConcurrentUpdate) {
		t.Fatalf("second insert of one site: %v", err)
	}
	got, _ := e.Calendars.Get(ctx, "WH1")
	if got.Version() != 1 || len(got.OperatingHours()) != 1 || got.OperatingHours()[0].Timezone() != "America/Sao_Paulo" {
		t.Fatalf("saved calendar = %+v", got.Snapshot())
	}
	if err := e.Calendars.Save(ctx, got, 7); !errors.Is(err, dockappointment.ErrConcurrentUpdate) {
		t.Fatalf("stale update: %v", err)
	}
}

func runCalendarUpdates(t *testing.T, f Factory) {
	ctx := context.Background()
	e := f(t)
	cal, _ := e.Calendars.Get(ctx, "WH1")
	if err := cal.DeclareSettings(dockcalendar.Settings{MaxWindowMinutes: 120, SlotGranularityMinutes: 30, ArrivalToleranceMinutes: 5, NoShowGraceMinutes: 90}); err != nil {
		t.Fatal(err)
	}
	if err := e.Calendars.Save(ctx, cal, 0); err != nil {
		t.Fatal(err)
	}
	got, _ := e.Calendars.Get(ctx, "WH1")
	if _, _, err := got.ScheduleClosure("clo-123e4567-e89b-12d3-a456-426614174001", "D1", base, base.Add(time.Hour), "x", nil, base); err != nil {
		t.Fatal(err)
	}
	if err := e.Calendars.Save(ctx, got, 0); !errors.Is(err, dockappointment.ErrConcurrentUpdate) {
		t.Fatalf("stale update: %v", err)
	}
	if err := e.Calendars.Save(ctx, got, 1); err != nil {
		t.Fatal(err)
	}
	final, _ := e.Calendars.Get(ctx, "WH1")
	if final.Version() != 2 || len(final.Closures()) != 1 || final.Closures()[0].Door != "D1" || final.Settings().NoShowGraceMinutes != 90 {
		t.Fatalf("final calendar = %+v", final.Snapshot())
	}
}

func runDoors(t *testing.T, f Factory) {
	ctx := context.Background()
	e := f(t)
	if d, err := e.Doors.Door(ctx, "D1"); d != nil || err != nil {
		t.Fatalf("unknown door = %v, %v", d, err)
	}
	if err := e.Doors.UpsertDoor(ctx, "D1", "zone-1", shared.FlowBoth); err != nil {
		t.Fatal(err)
	}
	if d, _ := e.Doors.Door(ctx, "D1"); d != nil {
		t.Fatalf("a door whose zone has not arrived is held pending: %+v", d)
	}
	if err := e.Doors.UpsertZone(ctx, "zone-1", "WH1"); err != nil {
		t.Fatal(err)
	}
	d, err := e.Doors.Door(ctx, "D1")
	if err != nil || d == nil || d.Site != "WH1" || d.Flow != shared.FlowBoth || d.Code != "D1" {
		t.Fatalf("door = %+v, %v", d, err)
	}
}

func runDoorUpdates(t *testing.T, f Factory) {
	ctx := context.Background()
	e := f(t)
	_ = e.Doors.UpsertZone(ctx, "zone-1", "WH1")
	_ = e.Doors.UpsertDoor(ctx, "D1", "zone-1", shared.FlowBoth)
	_ = e.Doors.UpsertDoor(ctx, "D1", "zone-1", shared.FlowInbound)
	d, _ := e.Doors.Door(ctx, "D1")
	if d.Flow != shared.FlowInbound {
		t.Fatalf("upsert must replace the flow: %+v", d)
	}
	_ = e.Doors.UpsertZone(ctx, "zone-1", "WH2")
	if d, _ = e.Doors.Door(ctx, "D1"); d.Site != "WH2" {
		t.Fatalf("upsert must replace the site: %+v", d)
	}
	if err := e.Doors.RemoveDoor(ctx, "D1"); err != nil {
		t.Fatal(err)
	}
	if d, _ = e.Doors.Door(ctx, "D1"); d != nil {
		t.Fatalf("removed door still known: %+v", d)
	}
	if err := e.Doors.RemoveDoor(ctx, "D1"); err != nil {
		t.Fatalf("removing an unknown door is a no-op: %v", err)
	}
}

func runAsns(t *testing.T, f Factory) {
	ctx := context.Background()
	e := f(t)
	_ = e.Asns.Register(ctx, "ASN-1", base)
	_ = e.Asns.Register(ctx, "ASN-2", base)
	_ = e.Asns.Cancel(ctx, "ASN-2")
	_ = e.Asns.Cancel(ctx, "ASN-3") // cancel before register inserts CANCELLED
	_ = e.Asns.Register(ctx, "ASN-3", base)
	_ = e.Asns.Register(ctx, "ASN-1", base) // idempotent
	got, err := e.Asns.Statuses(ctx, []string{"ASN-1", "ASN-2", "ASN-3", "ASN-4"})
	if err != nil || got["ASN-1"] != dockappointment.AsnRegistered || got["ASN-2"] != dockappointment.AsnCancelledStatus ||
		got["ASN-3"] != dockappointment.AsnCancelledStatus || got["ASN-4"] != dockappointment.AsnUnknown {
		t.Fatalf("statuses = %v, %v", got, err)
	}
	if got, err = e.Asns.Statuses(ctx, nil); err != nil || len(got) != 0 {
		t.Fatalf("statuses of nothing = %v, %v", got, err)
	}
}

func runCutoffs(t *testing.T, f Factory) {
	ctx := context.Background()
	e := f(t)
	one := []repository.Cutoff{{ID: "WH1-1500", LocalTime: "15:00", DaysOfWeek: []string{"Mon", "Tue"}, ShipMethod: "ground"}, {ID: "WH1-1800", LocalTime: "18:00", ShipMethod: "same-day"}}
	if err := e.Cutoffs.Replace(ctx, "WH1", "America/Sao_Paulo", one); err != nil {
		t.Fatal(err)
	}
	if err := e.Cutoffs.Replace(ctx, "WH2", "UTC", []repository.Cutoff{{ID: "WH2-1"}}); err != nil {
		t.Fatal(err)
	}
	got, err := e.Cutoffs.Known(ctx, "WH1", []string{"WH1-1500", "WH1-1800", "WH2-1", "nope"})
	if err != nil || len(got) != 2 || !got["WH1-1500"] || !got["WH1-1800"] {
		t.Fatalf("known = %v, %v", got, err)
	}
	if err := e.Cutoffs.Replace(ctx, "WH1", "UTC", one[:1]); err != nil {
		t.Fatal(err)
	}
	if got, _ = e.Cutoffs.Known(ctx, "WH1", []string{"WH1-1500", "WH1-1800"}); len(got) != 1 || !got["WH1-1500"] {
		t.Fatalf("replace must drop the cutoffs of the old schedule: %v", got)
	}
	if err := e.Cutoffs.Replace(ctx, "WH1", "UTC", nil); err != nil {
		t.Fatal(err)
	}
	if got, _ = e.Cutoffs.Known(ctx, "WH1", []string{"WH1-1500"}); len(got) != 0 {
		t.Fatalf("an empty schedule empties the site: %v", got)
	}
	if got, err = e.Cutoffs.Known(ctx, "WH1", nil); err != nil || len(got) != 0 {
		t.Fatalf("known of nothing: %v %v", got, err)
	}
}

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
