package usecases_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/claudioed/dock-scheduling/internal/application/ports"
	"github.com/claudioed/dock-scheduling/internal/application/usecases"
	"github.com/claudioed/dock-scheduling/internal/domain/dockappointment"
	"github.com/claudioed/dock-scheduling/internal/domain/dockcalendar"
)

func TestBookAppointmentHappyPath(t *testing.T) {
	e := newEnv(t)
	v, err := e.svc.BookAppointment(context.Background(), inboundCmd("D-BOTH", at8, at10))
	if err != nil {
		t.Fatal(err)
	}
	if v.AppointmentID != apptN(1) || v.SiteID != "WH1" || v.State != "BOOKED" || v.Version != 1 || v.DoorCode != "D-BOTH" || v.Provenance != "native" {
		t.Fatalf("view = %+v", v)
	}
	if got := e.eventNames(); len(got) != 1 || got[0] != "DockAppointmentBooked" {
		t.Fatalf("events = %v", got)
	}
	b := e.store.Published()[0].(dockappointment.BookedEvent)
	if b.ToleranceMinutes != 15 || b.SiteID() != "WH1" {
		t.Fatalf("booked event = %+v", b)
	}
}

func TestBookAppointmentRejects(t *testing.T) {
	tests := []struct {
		name string
		cmd  usecases.BookCommand
		want error
	}{
		{"invalid door code", inboundCmd("a b", at8, at10), nil},
		{"unknown door", inboundCmd("D-NOPE", at8, at10), dockappointment.ErrUnknownDoor},
		{"inbound on an outbound door", inboundCmd("D-OUT", at8, at10), dockappointment.ErrDoorFlowMismatch},
		{"outbound on an inbound door", outboundCmd("D-IN", at8, at10), dockappointment.ErrDoorFlowMismatch},
		{"window in the past", inboundCmd("D-BOTH", t0.Add(-time.Hour), t0), dockappointment.ErrWindowInPast},
		{"window off the grid", inboundCmd("D-BOTH", at8.Add(time.Minute), at10), dockappointment.ErrInvalidWindow},
		{"unknown asn", inboundCmd("D-BOTH", at8, at10, "ASN-404"), dockappointment.ErrUnknownAsn},
		{"outbound with an unknown cpt and no shipments", func() usecases.BookCommand {
			c := outboundCmd("D-BOTH", at8, at10)
			c.CPTID = "WH1-0000"
			return c
		}(), dockappointment.ErrMissingOutboundRef},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			_, err := e.svc.BookAppointment(context.Background(), tc.cmd)
			if err == nil || (tc.want != nil && !errors.Is(err, tc.want)) {
				t.Fatalf("err = %v; want %v", err, tc.want)
			}
			if len(e.store.Published()) != 0 {
				t.Fatal("a refused booking must publish nothing")
			}
		})
	}
}

func TestBookCancelledAsnIsUnknown(t *testing.T) {
	e := newEnv(t)
	must(t, e.store.Asns().Cancel(context.Background(), "ASN-1"))
	if _, err := e.svc.BookAppointment(context.Background(), inboundCmd("D-BOTH", at8, at10)); !errors.Is(err, dockappointment.ErrUnknownAsn) {
		t.Fatalf("err = %v", err)
	}
}

func TestOutboundBookingWithKnownCptOrShipments(t *testing.T) {
	e := newEnv(t)
	e.book(outboundCmd("D-OUT", at8, at10))
	c := outboundCmd("D-OUT", at10, at12)
	c.CPTID, c.ShipmentRefs = "", []string{"SHP-1"}
	e.book(c)
}

func TestBookingsShareOneCalendarPerDoor(t *testing.T) {
	e := newEnv(t)
	e.book(inboundCmd("D-BOTH", at8, at10))
	_, err := e.svc.BookAppointment(context.Background(), outboundCmd("D-BOTH", at9, at11))
	if !errors.Is(err, dockappointment.ErrDoorDoubleBooked) {
		t.Fatalf("outbound over an inbound window on a Both door: %v", err)
	}
	e.book(outboundCmd("D-BOTH", at10, at12)) // half-open: adjacent is compatible
	e.book(outboundCmd("D-BOTH2", at9, at11)) // another door
}

func TestAnAsnSitsOnOneActiveAppointment(t *testing.T) {
	e := newEnv(t)
	id := e.book(inboundCmd("D-BOTH", at8, at10, "ASN-1", "ASN-2"))
	_, err := e.svc.BookAppointment(context.Background(), inboundCmd("D-BOTH2", at8, at10, "ASN-2", "ASN-3"))
	if !errors.Is(err, dockappointment.ErrAsnAlreadyScheduled) || !strings.Contains(err.Error(), id) {
		t.Fatalf("err = %v", err)
	}
	_, err = e.svc.CancelAppointment(context.Background(), usecases.CancelCommand{AppointmentID: id, Reason: "CARRIER_REQUEST"})
	must(t, err)
	e.book(inboundCmd("D-BOTH2", at8, at10, "ASN-2", "ASN-3"))
}

func TestBookingHonoursClosuresAndHours(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	_, err := e.svc.ScheduleDoorClosure(ctx, usecases.ClosureCommand{SiteID: "WH1", DoorCode: "D-BOTH", From: at9, To: at10, Reason: "maintenance"})
	must(t, err)
	if _, err := e.svc.BookAppointment(ctx, inboundCmd("D-BOTH", at8, at10)); !errors.Is(err, dockcalendar.ErrDoorUnavailable) {
		t.Fatalf("booking over a closure: %v", err)
	}
	e.book(inboundCmd("D-BOTH", at10, at12))
	_, err = e.svc.DeclareOperatingHours(ctx, "WH1", []usecases.HoursInput{{Weekday: "MON", OpensAt: "09:00", ClosesAt: "10:00", Timezone: "UTC"}})
	if !errors.Is(err, dockcalendar.ErrHoursConflictWithAppointments) {
		t.Fatalf("hours that strand the 10:00-12:00 booking: %v", err)
	}
	if _, err := e.svc.BookAppointment(ctx, inboundCmd("D-BOTH2", at11, at12, "ASN-2")); err != nil {
		t.Fatal(err)
	}
}

func TestRescheduleAppointment(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	id := e.book(inboundCmd("D-BOTH", at8, at10))
	// moving inside its own old window is not an overlap with itself
	v, err := e.svc.RescheduleAppointment(ctx, usecases.RescheduleCommand{AppointmentID: id, WindowStart: at9, WindowEnd: at11})
	if err != nil || v.Version != 2 || v.DoorCode != "D-BOTH" || !v.WindowStart.Equal(at9) {
		t.Fatalf("reschedule: %+v, %v", v, err)
	}
	// another door, optimistic version honoured
	v, err = e.svc.RescheduleAppointment(ctx, usecases.RescheduleCommand{AppointmentID: id, DoorCode: "D-BOTH2", WindowStart: at9, WindowEnd: at11, ExpectedVersion: 2})
	if err != nil || v.DoorCode != "D-BOTH2" || v.Version != 3 {
		t.Fatalf("door change: %+v, %v", v, err)
	}
	re := e.store.Published()[len(e.store.Published())-1].(dockappointment.RescheduledEvent)
	if re.PreviousDoorCode != "D-BOTH" || re.DoorCode != "D-BOTH2" {
		t.Fatalf("event = %+v", re)
	}
	_, err = e.svc.RescheduleAppointment(ctx, usecases.RescheduleCommand{AppointmentID: id, WindowStart: at10, WindowEnd: at12, ExpectedVersion: 1})
	if !errors.Is(err, usecases.ErrPreconditionFailed) {
		t.Fatalf("stale If-Match: %v", err)
	}
}

func TestRescheduleRefusals(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a := e.book(inboundCmd("D-BOTH", at8, at10))
	e.book(inboundCmd("D-BOTH2", at10, at12, "ASN-2"))
	tests := []struct {
		name string
		cmd  usecases.RescheduleCommand
		want error
	}{
		{"malformed id", usecases.RescheduleCommand{AppointmentID: "x", WindowStart: at9, WindowEnd: at11}, dockappointment.ErrInvalidAppointmentID},
		{"unknown id", usecases.RescheduleCommand{AppointmentID: apptN(99), WindowStart: at9, WindowEnd: at11}, ports.ErrNotFound},
		{"invalid door code", usecases.RescheduleCommand{AppointmentID: a, DoorCode: "a b", WindowStart: at9, WindowEnd: at11}, nil},
		{"unknown door", usecases.RescheduleCommand{AppointmentID: a, DoorCode: "D-NOPE", WindowStart: at9, WindowEnd: at11}, dockappointment.ErrUnknownDoor},
		{"door of another site", usecases.RescheduleCommand{AppointmentID: a, DoorCode: "D-OTHER", WindowStart: at9, WindowEnd: at11}, dockappointment.ErrUnknownDoor},
		{"door that does not take the direction", usecases.RescheduleCommand{AppointmentID: a, DoorCode: "D-OUT", WindowStart: at9, WindowEnd: at11}, dockappointment.ErrDoorFlowMismatch},
		{"into the past", usecases.RescheduleCommand{AppointmentID: a, WindowStart: t0.Add(-2 * time.Hour), WindowEnd: t0.Add(-time.Hour)}, dockappointment.ErrWindowInPast},
		{"onto another booking", usecases.RescheduleCommand{AppointmentID: a, DoorCode: "D-BOTH2", WindowStart: at11, WindowEnd: at12.Add(time.Hour)}, dockappointment.ErrDoorDoubleBooked},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := e.svc.RescheduleAppointment(ctx, tc.cmd)
			if err == nil || (tc.want != nil && !errors.Is(err, tc.want)) {
				t.Fatalf("err = %v; want %v", err, tc.want)
			}
		})
	}
	if v, _ := e.svc.GetAppointment(ctx, a); v.Version != 1 {
		t.Fatalf("refused reschedules must not change the aggregate: v%d", v.Version)
	}
}

func TestCancelAppointment(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	id := e.book(inboundCmd("D-BOTH", at8, at10))
	if _, err := e.svc.CancelAppointment(ctx, usecases.CancelCommand{AppointmentID: id, Reason: "BORED"}); !errors.Is(err, dockappointment.ErrInvalidReason) {
		t.Fatalf("invalid reason: %v", err)
	}
	if _, err := e.svc.CancelAppointment(ctx, usecases.CancelCommand{AppointmentID: id, Reason: "SITE_REQUEST", ExpectedVersion: 7}); !errors.Is(err, usecases.ErrPreconditionFailed) {
		t.Fatalf("If-Match: %v", err)
	}
	v, err := e.svc.CancelAppointment(ctx, usecases.CancelCommand{AppointmentID: id, Reason: "SITE_REQUEST", ExpectedVersion: 1})
	if err != nil || v.State != "CANCELLED" || v.CancellationReason != "SITE_REQUEST" || v.Version != 2 {
		t.Fatalf("cancel: %+v, %v", v, err)
	}
	if _, err := e.svc.CancelAppointment(ctx, usecases.CancelCommand{AppointmentID: id, Reason: "SITE_REQUEST"}); !errors.Is(err, dockappointment.ErrIllegalTransition) {
		t.Fatalf("second cancel: %v", err)
	}
	// the door window is free again
	e.book(inboundCmd("D-BOTH", at8, at10))
}

func TestReportEta(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	id := e.book(inboundCmd("D-BOTH", at8, at10))
	r, err := e.svc.ReportEta(ctx, usecases.EtaCommand{AppointmentID: id, EtaAt: at8.Add(10 * time.Minute)})
	if err != nil || r.AtRisk || r.Appointment.Eta == nil || r.Appointment.Eta.Source != "api" || r.Appointment.Version != 2 {
		t.Fatalf("on-time eta: %+v, %v", r, err)
	}
	r, err = e.svc.ReportEta(ctx, usecases.EtaCommand{AppointmentID: id, EtaAt: at9, Source: "carrier-portal"})
	if err != nil || !r.AtRisk || !r.Appointment.AtRisk || r.Appointment.AtRiskCause != "LATE_ETA" {
		t.Fatalf("late eta: %+v, %v", r, err)
	}
	if got := e.eventNames(); got[len(got)-1] != "DockAppointmentAtRisk" {
		t.Fatalf("events = %v", got)
	}
	if _, err := e.svc.ReportEta(ctx, usecases.EtaCommand{AppointmentID: "x"}); err == nil {
		t.Fatal("malformed id must fail")
	}
	if _, err := e.svc.ReportEta(ctx, usecases.EtaCommand{AppointmentID: apptN(9), EtaAt: at9}); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("unknown id: %v", err)
	}
	if _, err := e.svc.ReportEta(ctx, usecases.EtaCommand{AppointmentID: id}); !errors.Is(err, dockappointment.ErrInvalidEta) {
		t.Fatalf("zero eta: %v", err)
	}
}

func TestNoShowSweep(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a := e.book(inboundCmd("D-BOTH", at8, at10))
	b := e.book(inboundCmd("D-BOTH2", at8, at11, "ASN-2"))
	e.clock.now = at10.Add(30 * time.Minute) // a ended 30 min ago: inside the 60 min grace
	r, err := e.svc.RunNoShowSweep(ctx)
	if err != nil || r.Count != 0 || r.AppointmentIDs == nil || len(r.AppointmentIDs) != 0 || !r.RanAt.Equal(e.clock.now) {
		t.Fatalf("early sweep: %+v, %v", r, err)
	}
	e.clock.now = at10.Add(time.Hour) // grace over for a only
	r, err = e.svc.RunNoShowSweep(ctx)
	if err != nil || r.Count != 1 || r.AppointmentIDs[0] != a || e.state(a) != dockappointment.NoShow || e.state(b) != dockappointment.Booked {
		t.Fatalf("sweep: %+v, %v", r, err)
	}
	if r, _ = e.svc.RunNoShowSweep(ctx); r.Count != 0 {
		t.Fatalf("the sweep is idempotent: %+v", r)
	}
	names := e.eventNames()
	if names[len(names)-1] != "DockAppointmentNoShow" {
		t.Fatalf("events = %v", names)
	}
}

func TestNoShowSweepUsesTheGraceOfTheAppointmentsSite(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	wh1 := e.book(inboundCmd("D-BOTH", at8, at10))
	wh2 := e.book(inboundCmd("D-OTHER", at8, at10, "ASN-2"))
	_, err := e.svc.DeclareCalendarSettings(ctx, "WH1", settings(240, 15, 15, 0))
	must(t, err)
	e.clock.now = at10 // WH1 grace 0: due now; WH2 default grace 60: not yet
	r, err := e.svc.RunNoShowSweep(ctx)
	if err != nil || r.Count != 1 || r.AppointmentIDs[0] != wh1 || e.state(wh2) != dockappointment.Booked {
		t.Fatalf("zero grace on WH1 only: %+v, %v", r, err)
	}
	e.clock.now = at12
	if r, _ = e.svc.RunNoShowSweep(ctx); r.Count != 1 || r.AppointmentIDs[0] != wh2 {
		t.Fatalf("WH2 after its grace: %+v", r)
	}
}
