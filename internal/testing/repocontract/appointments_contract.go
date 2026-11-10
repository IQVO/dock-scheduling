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
)

// runAppointments: save/get round trip, optimistic version, not-found and the
// concurrent-update refusals of inserts and updates.
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

// runDoorOverride: an appointment whose door was set by a physical fact is exempt from the overlap backstop.
func runDoorOverride(t *testing.T, f Factory) {
	ctx := context.Background()
	e := f(t)
	mustSave(t, e, book(t, 1, "D1", 0, "ASN-A"), 0)
	moved := book(t, 2, "D2", 0, "ASN-B")
	mustSave(t, e, moved, 0)
	got, _ := e.Appointments.Get(ctx, apptID(2))
	if _, err := got.ReassignDoor("D1", base); err != nil {
		t.Fatal(err)
	}
	mustSave(t, e, got, 1)
	again, err := e.Appointments.Get(ctx, apptID(2))
	if err != nil || again.DoorCode() != "D1" || !again.DoorOverride() {
		t.Fatalf("reassigned = %+v, %v", again, err)
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
