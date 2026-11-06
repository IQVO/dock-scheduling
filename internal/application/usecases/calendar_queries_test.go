package usecases_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/claudioed/dock-scheduling/internal/application/repository"
	"github.com/claudioed/dock-scheduling/internal/application/usecases"
	"github.com/claudioed/dock-scheduling/internal/application/views"
	"github.com/claudioed/dock-scheduling/internal/domain/dockappointment"
	"github.com/claudioed/dock-scheduling/internal/domain/dockcalendar"
)

func settings(maxW, gran, tol, grace int) views.SettingsView {
	return views.SettingsView{MaxWindowMinutes: maxW, SlotGranularityMinutes: gran, ArrivalToleranceMinutes: tol, NoShowGraceMinutes: grace}
}

func TestGetDockCalendarDefaults(t *testing.T) {
	e := newEnv(t)
	v, err := e.svc.GetDockCalendar(context.Background(), "WH1")
	if err != nil || v.Version != 0 || v.Settings != settings(240, 15, 15, 60) || len(v.OperatingHours) != 0 || v.Closures == nil || v.SiteID != "WH1" {
		t.Fatalf("defaults: %+v, %v", v, err)
	}
	if _, err := e.svc.GetDockCalendar(context.Background(), "a b"); err == nil {
		t.Fatal("invalid site id must fail")
	}
}

func TestDeclareOperatingHours(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	rows := []usecases.HoursInput{{Weekday: "MON", OpensAt: "06:00", ClosesAt: "22:00", Timezone: "UTC"}, {DoorCode: "D-IN", Weekday: "MON", OpensAt: "07:00", ClosesAt: "12:00", Timezone: "UTC"}}
	v, err := e.svc.DeclareOperatingHours(ctx, "WH1", rows)
	if err != nil || v.Version != 1 || len(v.OperatingHours) != 2 || v.OperatingHours[1].DoorCode != "D-IN" {
		t.Fatalf("declare: %+v, %v", v, err)
	}
	if got := e.eventNames(); len(got) != 1 || got[0] != "DoorOperatingHoursDeclared" {
		t.Fatalf("events = %v", got)
	}
	if _, err := e.svc.BookAppointment(ctx, inboundCmd("D-IN", at12, at12.Add(time.Hour))); !errors.Is(err, dockcalendar.ErrDoorUnavailable) {
		t.Fatalf("D-IN closes at 12:00: %v", err)
	}
	e.book(inboundCmd("D-BOTH", at12, at12.Add(time.Hour)))
	for name, bad := range map[string][]usecases.HoursInput{
		"bad weekday": {{Weekday: "X", OpensAt: "06:00", ClosesAt: "22:00", Timezone: "UTC"}},
		"bad time":    {{Weekday: "MON", OpensAt: "22:00", ClosesAt: "06:00", Timezone: "UTC"}},
		"duplicate":   {rows[0], rows[0]},
	} {
		if _, err := e.svc.DeclareOperatingHours(ctx, "WH1", bad); !errors.Is(err, dockcalendar.ErrInvalidOperatingHours) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := e.svc.DeclareOperatingHours(ctx, "a b", rows); err == nil {
		t.Fatal("invalid site id must fail")
	}
	cal, _ := e.svc.GetDockCalendar(ctx, "WH1")
	if cal.Version != 1 {
		t.Fatalf("refused declarations must not bump the version: %d", cal.Version)
	}
}

func TestDeclareCalendarSettingsDrivesTheWindowRules(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	v, err := e.svc.DeclareCalendarSettings(ctx, "WH1", settings(120, 30, 5, 10))
	if err != nil || v.Settings != settings(120, 30, 5, 10) || v.Version != 1 {
		t.Fatalf("settings: %+v, %v", v, err)
	}
	if got := e.eventNames(); len(got) != 0 {
		t.Fatalf("settings raise no event: %v", got)
	}
	if _, err := e.svc.BookAppointment(ctx, inboundCmd("D-BOTH", at8, at11)); !errors.Is(err, dockappointment.ErrInvalidWindow) {
		t.Fatalf("3h window over the 2h site maximum: %v", err)
	}
	if _, err := e.svc.BookAppointment(ctx, inboundCmd("D-BOTH", at8.Add(15*time.Minute), at10)); !errors.Is(err, dockappointment.ErrInvalidWindow) {
		t.Fatalf("off the 30 minute grid: %v", err)
	}
	e.book(inboundCmd("D-BOTH", at8, at10))
	if b := e.store.Published()[0].(dockappointment.BookedEvent); b.ToleranceMinutes != 5 {
		t.Fatalf("tolerance in the event = %d", b.ToleranceMinutes)
	}
	if _, err := e.svc.DeclareCalendarSettings(ctx, "WH1", settings(0, 30, 5, 10)); !errors.Is(err, dockcalendar.ErrInvalidSettings) {
		t.Fatalf("invalid settings: %v", err)
	}
	if _, err := e.svc.DeclareCalendarSettings(ctx, "a b", settings(120, 30, 5, 10)); err == nil {
		t.Fatal("invalid site must fail")
	}
}

func TestClosures(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	k, err := e.svc.ScheduleDoorClosure(ctx, usecases.ClosureCommand{SiteID: "WH1", DoorCode: "D-BOTH", From: at9, To: at10, Reason: "leveller"})
	if err != nil || k.ClosureID != cloN(1) || k.SiteID != "WH1" || k.Lifted || k.DoorCode != "D-BOTH" {
		t.Fatalf("schedule: %+v, %v", k, err)
	}
	cal, _ := e.svc.GetDockCalendar(ctx, "WH1")
	if len(cal.Closures) != 1 || cal.Version != 1 {
		t.Fatalf("calendar = %+v", cal)
	}
	_, err = e.svc.ScheduleDoorClosure(ctx, usecases.ClosureCommand{SiteID: "WH1", DoorCode: "D-BOTH", From: at9.Add(30 * time.Minute), To: at11, Reason: "x"})
	if !errors.Is(err, dockcalendar.ErrOverlappingClosure) {
		t.Fatalf("overlap: %v", err)
	}
	lifted, err := e.svc.LiftDoorClosure(ctx, "WH1", k.ClosureID)
	if err != nil || !lifted.Lifted {
		t.Fatalf("lift: %+v, %v", lifted, err)
	}
	if cal, _ = e.svc.GetDockCalendar(ctx, "WH1"); len(cal.Closures) != 0 {
		t.Fatalf("a lifted closure is not listed: %+v", cal.Closures)
	}
	names := e.eventNames()
	if len(names) != 2 || names[0] != "DoorClosureScheduled" || names[1] != "DoorClosureLifted" {
		t.Fatalf("events = %v", names)
	}
}

func TestClosureRefusals(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	k, err := e.svc.ScheduleDoorClosure(ctx, usecases.ClosureCommand{SiteID: "WH1", DoorCode: "D-BOTH", From: at9, To: at10, Reason: "leveller"})
	must(t, err)
	for name, cmd := range map[string]usecases.ClosureCommand{
		"unknown door":         {SiteID: "WH1", DoorCode: "D-NOPE", From: at9, To: at10, Reason: "x"},
		"door of another site": {SiteID: "WH1", DoorCode: "D-OTHER", From: at9, To: at10, Reason: "x"},
	} {
		if _, err := e.svc.ScheduleDoorClosure(ctx, cmd); !errors.Is(err, dockappointment.ErrUnknownDoor) {
			t.Errorf("%s: %v", name, err)
		}
	}
	for name, cmd := range map[string]usecases.ClosureCommand{
		"bad site": {SiteID: "a b", DoorCode: "D-BOTH", From: at9, To: at10, Reason: "x"},
		"bad door": {SiteID: "WH1", DoorCode: "a b", From: at9, To: at10, Reason: "x"},
		"bad span": {SiteID: "WH1", DoorCode: "D-BOTH2", From: at10, To: at9, Reason: "x"},
	} {
		if _, err := e.svc.ScheduleDoorClosure(ctx, cmd); err == nil {
			t.Errorf("%s must fail", name)
		}
	}
	_, err = e.svc.LiftDoorClosure(ctx, "WH1", k.ClosureID)
	must(t, err)
	if _, err := e.svc.LiftDoorClosure(ctx, "WH1", k.ClosureID); !errors.Is(err, dockcalendar.ErrClosureAlreadyLifted) {
		t.Fatalf("second lift: %v", err)
	}
	if _, err := e.svc.LiftDoorClosure(ctx, "WH1", cloN(77)); !errors.Is(err, dockcalendar.ErrUnknownClosure) {
		t.Fatalf("unknown closure: %v", err)
	}
	if _, err := e.svc.LiftDoorClosure(ctx, "a b", cloN(1)); err == nil {
		t.Fatal("invalid site must fail")
	}
}

func TestAClosureCannotCoverAnActiveAppointment(t *testing.T) {
	e := newEnv(t)
	id := e.book(inboundCmd("D-BOTH", at9, at10))
	_, err := e.svc.ScheduleDoorClosure(context.Background(), usecases.ClosureCommand{SiteID: "WH1", DoorCode: "D-BOTH", From: at8, To: at11, Reason: "x"})
	var ce *dockcalendar.ConflictError
	if !errors.Is(err, dockcalendar.ErrClosureConflictsWithAppointments) || !errors.As(err, &ce) || len(ce.AppointmentIDs) != 1 || ce.AppointmentIDs[0] != id {
		t.Fatalf("err = %v", err)
	}
}

func TestListAppointmentsPaging(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	ids := []string{
		e.book(inboundCmd("D-BOTH", at8, at9)), e.book(inboundCmd("D-BOTH", at9, at10, "ASN-2")),
		e.book(inboundCmd("D-BOTH", at10, at11, "ASN-3")), e.book(outboundCmd("D-OUT", at8, at9)),
	}
	p1, err := e.svc.ListAppointments(ctx, repository.AppointmentFilter{}, 3, "")
	if err != nil || len(p1.Items) != 3 || p1.NextCursor == "" {
		t.Fatalf("page 1: %+v, %v", p1, err)
	}
	p2, err := e.svc.ListAppointments(ctx, repository.AppointmentFilter{}, 3, p1.NextCursor)
	if err != nil || len(p2.Items) != 1 || p2.NextCursor != "" {
		t.Fatalf("page 2: %+v, %v", p2, err)
	}
	seen := map[string]bool{}
	for _, v := range append(p1.Items, p2.Items...) {
		seen[v.AppointmentID] = true
	}
	for _, id := range ids {
		if !seen[id] {
			t.Errorf("%s missing from the pages", id)
		}
	}
	if def, err := e.svc.ListAppointments(ctx, repository.AppointmentFilter{DoorCode: "D-OUT"}, 0, ""); err != nil || len(def.Items) != 1 {
		t.Fatalf("default limit + filter: %+v, %v", def, err)
	}
	if empty, err := e.svc.ListAppointments(ctx, repository.AppointmentFilter{SiteID: "NOPE"}, 0, ""); err != nil || empty.Items == nil || len(empty.Items) != 0 {
		t.Fatalf("empty page: %+v, %v", empty, err)
	}
}

func TestListAppointmentsLimitBounds(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	for _, ok := range []int{1, 500} {
		if _, err := e.svc.ListAppointments(ctx, repository.AppointmentFilter{}, ok, ""); err != nil {
			t.Errorf("limit %d is allowed: %v", ok, err)
		}
	}
	for _, bad := range []struct {
		limit  int
		cursor string
	}{{-1, ""}, {501, ""}, {10, "!!!"}, {10, "e30"}, {10, "eyJzIjoibm9wZSIsImkiOiJ4In0"}} {
		if _, err := e.svc.ListAppointments(ctx, repository.AppointmentFilter{}, bad.limit, bad.cursor); !errors.Is(err, usecases.ErrInvalidListQuery) {
			t.Errorf("limit %d cursor %q: %v", bad.limit, bad.cursor, err)
		}
	}
}

func TestListAtRiskAndGetAppointment(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	late := e.book(inboundCmd("D-BOTH", at8, at9))
	e.book(inboundCmd("D-BOTH", at9, at10, "ASN-2"))
	_, err := e.svc.ReportEta(ctx, usecases.EtaCommand{AppointmentID: late, EtaAt: at10})
	must(t, err)
	page, err := e.svc.ListAtRisk(ctx, "WH1", 10, "")
	if err != nil || len(page.Items) != 1 || page.Items[0].AppointmentID != late || !page.Items[0].AtRisk {
		t.Fatalf("at risk: %+v, %v", page, err)
	}
	if _, err := e.svc.GetAppointment(ctx, "x"); !errors.Is(err, dockappointment.ErrInvalidAppointmentID) {
		t.Fatalf("malformed id: %v", err)
	}
	if _, err := e.svc.GetAppointment(ctx, apptN(50)); err == nil {
		t.Fatal("unknown id must fail")
	}
}

func TestGetDoorAvailability(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.book(inboundCmd("D-BOTH", at9, at10))
	_, err := e.svc.ScheduleDoorClosure(ctx, usecases.ClosureCommand{SiteID: "WH1", DoorCode: "D-BOTH", From: at11, To: at12, Reason: "x"})
	must(t, err)
	v, err := e.svc.GetDoorAvailability(ctx, "D-BOTH", at8, at12, 0)
	if err != nil || v.SiteID != "WH1" || v.GranularityMinutes != 15 || len(v.FreeWindows) != 2 || len(v.Busy) != 2 {
		t.Fatalf("availability: %+v, %v", v, err)
	}
	if !v.FreeWindows[0].End.Equal(at9) || !v.FreeWindows[1].Start.Equal(at10) || !v.FreeWindows[1].End.Equal(at11) {
		t.Fatalf("free windows = %+v", v.FreeWindows)
	}
	if v.Busy[0].Kind != "APPOINTMENT" || v.Busy[1].Kind != "CLOSURE" {
		t.Fatalf("busy = %+v", v.Busy)
	}
	if v, _ = e.svc.GetDoorAvailability(ctx, "D-BOTH", at8, at12, 90); len(v.FreeWindows) != 0 {
		t.Fatalf("minimum 90 minutes: %+v", v.FreeWindows)
	}
}

func TestGetDoorAvailabilityRejects(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	for name, c := range map[string]struct {
		door     string
		from, to time.Time
		min      int
		want     error
	}{
		"unknown door": {"D-NOPE", at8, at12, 0, usecases.ErrDoorNotFound},
		"reversed":     {"D-BOTH", at12, at8, 0, usecases.ErrInvalidAvailabilityQuery},
		"empty":        {"D-BOTH", at8, at8, 0, usecases.ErrInvalidAvailabilityQuery},
		"too long":     {"D-BOTH", at8, at8.Add(14*24*time.Hour + time.Second), 0, usecases.ErrInvalidAvailabilityQuery},
		"negative min": {"D-BOTH", at8, at12, -1, usecases.ErrInvalidAvailabilityQuery},
		"huge min":     {"D-BOTH", at8, at12, 241, usecases.ErrInvalidAvailabilityQuery},
	} {
		if _, err := e.svc.GetDoorAvailability(ctx, c.door, c.from, c.to, c.min); !errors.Is(err, c.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := e.svc.GetDoorAvailability(ctx, "a b", at8, at12, 0); err == nil {
		t.Fatal("invalid door must fail")
	}
	if _, err := e.svc.GetDoorAvailability(ctx, "D-BOTH", at8, at8.Add(14*24*time.Hour), 240); err != nil {
		t.Fatalf("exactly 14 days and 240 minutes are allowed: %v", err)
	}
}
