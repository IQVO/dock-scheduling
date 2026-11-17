package outbox_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/claudioed/dock-scheduling/internal/application/outbox"
	"github.com/claudioed/dock-scheduling/internal/domain/dockappointment"
	"github.com/claudioed/dock-scheduling/internal/domain/dockcalendar"
	"github.com/claudioed/dock-scheduling/internal/domain/shared"
)

// The standard fields of ADR 0013 on the wire: x12_status, cbv_biz_step and
// read_point_gln are OPTIONAL and omitted (never null, never "") when there is
// nothing honest to say, so a fact with none serialises exactly as before.

const (
	doorA = "WH1-DOCK-IN-01"
	doorB = "WH1-DOCK-IN-02"
	glnA  = "4006381333931"
	glnB  = "4012345000009"
)

func at(h int) time.Time { return time.Date(2026, 11, 6, h, 0, 0, 0, time.UTC) }

func header(flow shared.Direction) dockappointment.Header {
	return dockappointment.Header{Appointment: "appt-1", Site: "WH1", Version: 2, At: at(7), Flow: flow}
}

// gln knows doorA and doorB; it records every door it is asked about.
type gln struct{ asked []string }

func (g *gln) lookup(door string) (string, error) {
	g.asked = append(g.asked, door)
	switch door {
	case doorA:
		return glnA, nil
	case doorB:
		return glnB, nil
	}
	return "", nil
}

func render(t *testing.T, v any, err error) map[string]any {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	raw, merr := json.Marshal(v)
	if merr != nil {
		t.Fatal(merr)
	}
	var out map[string]any
	if uerr := json.Unmarshal(raw, &out); uerr != nil {
		t.Fatal(uerr)
	}
	return out
}

func appointment(t *testing.T, e dockappointment.Event, l outbox.GLNLookup) map[string]any {
	t.Helper()
	v, err := outbox.AppointmentFactData(e, l)
	return render(t, v, err)
}

func wantStd(t *testing.T, got map[string]any, x12, cbv, readPoint string) {
	t.Helper()
	for key, want := range map[string]string{"x12_status": x12, "cbv_biz_step": cbv, "read_point_gln": readPoint} {
		v, present := got[key]
		if want == "" {
			if present {
				t.Errorf("%s must be omitted when absent, got %v in %v", key, v, got)
			}
			continue
		}
		if v != want {
			t.Errorf("%s = %v, want %q (in %v)", key, v, want, got)
		}
	}
}

func TestBookedCarriesTheAppointmentStatusOfItsDirection(t *testing.T) {
	for _, c := range []struct {
		flow shared.Direction
		x12  string
	}{{shared.Inbound, "AB"}, {shared.Outbound, "AA"}} {
		t.Run(string(c.flow), func(t *testing.T) {
			g := &gln{}
			got := appointment(t, dockappointment.BookedEvent{Header: header(c.flow), Direction: c.flow, DoorCode: doorA, WindowStart: at(8), WindowEnd: at(9)}, g.lookup)
			wantStd(t, got, c.x12, "", glnA)
			if len(g.asked) != 1 || g.asked[0] != doorA {
				t.Errorf("the GLN of the booked door is looked up once, asked %v", g.asked)
			}
		})
	}
}

func TestACheckInIsArrivingAtTheLocationOfItsDirection(t *testing.T) {
	for _, c := range []struct {
		flow shared.Direction
		x12  string
	}{{shared.Inbound, "X1"}, {shared.Outbound, "X3"}} {
		got := appointment(t, dockappointment.CheckedInEvent{Header: header(c.flow), DoorCode: doorA, CheckedInAt: at(7)}, new(gln).lookup)
		wantStd(t, got, c.x12, "urn:epcglobal:cbv:bizstep:arriving", glnA)
	}
}

func TestARescheduleRestatesTheAppointmentAtTheNewDoor(t *testing.T) {
	e := dockappointment.RescheduledEvent{Header: header(shared.Outbound), DoorCode: doorB, PreviousDoorCode: doorA, WindowStart: at(9), WindowEnd: at(10)}
	g := &gln{}
	wantStd(t, appointment(t, e, g.lookup), "AA", "", glnB)
	if len(g.asked) != 1 || g.asked[0] != doorB {
		t.Errorf("the read point of a reschedule is the NEW door, asked %v", g.asked)
	}
}

func TestADoorReassignmentHasTheNewDoorAsItsReadPointAndNoStatus(t *testing.T) {
	g := &gln{}
	got := appointment(t, dockappointment.DoorReassigned{Header: header(shared.Inbound), FromDoorCode: doorA, ToDoorCode: doorB}, g.lookup)
	wantStd(t, got, "", "", glnB)
	if len(g.asked) != 1 || g.asked[0] != doorB {
		t.Errorf("asked %v, want only the door the appointment moved to", g.asked)
	}
}

// Facts the standards have no honest word for still carry the read point when
// their door has a GLN, and nothing else.
func TestFactsWithNoStandardStatusCarryOnlyTheReadPoint(t *testing.T) {
	hdr := header(shared.Inbound)
	for name, e := range map[string]dockappointment.Event{
		"cancelled": dockappointment.CancelledEvent{Header: hdr, DoorCode: doorA, Reason: dockappointment.CarrierRequest},
		"no show":   dockappointment.NoShowEvent{Header: hdr, DoorCode: doorA, Direction: shared.Inbound, WindowEnd: at(10)},
		"completed": dockappointment.CompletedEvent{Header: hdr, DoorCode: doorA, CompletedAt: at(9)},
		"at risk":   dockappointment.AtRisk{Header: hdr, DoorCode: doorA, EtaAt: at(9), WindowStart: at(8), WindowEnd: at(10), Cause: dockappointment.LateEta},
	} {
		t.Run(name, func(t *testing.T) { wantStd(t, appointment(t, e, new(gln).lookup), "", "", glnA) })
	}
}

func TestADoorWithoutAGLNOrAnUnknownDoorOmitsTheReadPoint(t *testing.T) {
	got := appointment(t, dockappointment.BookedEvent{Header: header(shared.Inbound), Direction: shared.Inbound, DoorCode: "WH1-DOCK-NO-GLN"}, new(gln).lookup)
	wantStd(t, got, "AB", "", "")
	// No lookup at all (nil) is the same: nothing is invented.
	wantStd(t, appointment(t, dockappointment.BookedEvent{Header: header(shared.Outbound), Direction: shared.Outbound, DoorCode: doorA}, nil), "AA", "", "")
}

// The byte-identity promise: a fact with no standard meaning and no GLN is the
// very JSON it was before the fields existed (no key, not even null).
func TestAFactWithNothingToAddIsByteIdenticalToTheOldPayload(t *testing.T) {
	v, err := outbox.AppointmentFactData(dockappointment.CancelledEvent{Header: header(shared.Inbound), DoorCode: doorA, Reason: dockappointment.CarrierRequest}, noGLN)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(v)
	const want = `{"appointment_id":"appt-1","site_id":"WH1","door_code":"WH1-DOCK-IN-01","reason":"CARRIER_REQUEST","version":2}`
	if string(raw) != want {
		t.Errorf("got  %s\nwant %s", raw, want)
	}
	for _, key := range []string{"x12_status", "cbv_biz_step", "read_point_gln"} {
		if strings.Contains(string(raw), key) {
			t.Errorf("%s leaked into %s", key, raw)
		}
	}
}

func noGLN(string) (string, error) { return "", nil }

func TestALookupFailureFailsTheFactNotTheField(t *testing.T) {
	boom := errors.New("db down")
	_, err := outbox.AppointmentFactData(dockappointment.CancelledEvent{Header: header(shared.Inbound), DoorCode: doorA},
		func(string) (string, error) { return "", boom })
	if !errors.Is(err, boom) || !strings.Contains(err.Error(), doorA) {
		t.Fatalf("err = %v, want the lookup error naming the door", err)
	}
	// An empty door code is never looked up.
	called := false
	_, err = outbox.AppointmentFactData(dockappointment.CancelledEvent{Header: header(shared.Inbound)}, func(string) (string, error) { called = true; return "", nil })
	if err != nil || called {
		t.Fatalf("a fact with no door must not query a GLN (err %v, called %v)", err, called)
	}
}

func TestCalendarFactsCarryOnlyTheReadPointOfTheirDoor(t *testing.T) {
	site := dockcalendar.Header{Site: "WH1", Version: 3, At: at(5)}
	closure := dockcalendar.Closure{ID: "clo-1", Door: doorA, From: at(10), To: at(12), Reason: "maintenance"}
	for name, e := range map[string]dockcalendar.Event{
		"scheduled": dockcalendar.ClosureScheduled{Header: site, Closure: closure},
		"lifted":    dockcalendar.ClosureLifted{Header: site, Closure: closure},
	} {
		t.Run(name, func(t *testing.T) {
			v, err := outbox.CalendarFactData(e, new(gln).lookup)
			wantStd(t, render(t, v, err), "", "", glnA)
		})
	}
	monday, err := dockcalendar.NewOperatingHours(doorA, dockcalendar.Mon, "06:00", "22:00", "UTC")
	if err != nil {
		t.Fatal(err)
	}
	g := &gln{}
	v, err := outbox.CalendarFactData(dockcalendar.OperatingHoursDeclared{Header: site, Rows: []dockcalendar.OperatingHours{monday}}, g.lookup)
	wantStd(t, render(t, v, err), "", "", "")
	if len(g.asked) != 0 {
		t.Errorf("the operating-hours declaration has no single door and must not look one up, asked %v", g.asked)
	}
}

// Nullable and NullableTime are the helpers of the present-and-null fields.
func TestNullableHelpers(t *testing.T) {
	if outbox.Nullable("") != nil || *outbox.Nullable("x") != "x" {
		t.Error("Nullable: empty is nil, anything else is itself")
	}
	if outbox.NullableTime(time.Time{}) != nil {
		t.Error("NullableTime: the zero time is nil")
	}
	local := time.Date(2026, 11, 6, 9, 0, 0, 0, time.FixedZone("x", 3600))
	if got := outbox.NullableTime(local); got == nil || got.Location() != time.UTC || !got.Equal(local) {
		t.Errorf("NullableTime(%v) = %v, want the same instant in UTC", local, got)
	}
}
