package dockappointment_test

import (
	"errors"
	"testing"
	"time"

	da "github.com/claudioed/dock-scheduling/internal/domain/dockappointment"
)

func later(d time.Duration) time.Time { return now.Add(d) }

func TestReschedule(t *testing.T) {
	a := mustBook(t, inbound())
	at := later(time.Hour)
	evs, err := a.Reschedule("WH1-DOCK-IN-02", t900, t1200, rules, at)
	mustEvents(t, evs, err, "DockAppointmentRescheduled")
	e := evs[0].(da.RescheduledEvent)
	if e.DoorCode != "WH1-DOCK-IN-02" || e.PreviousDoorCode != "WH1-DOCK-IN-01" || !e.WindowStart.Equal(t900) || !e.WindowEnd.Equal(t1200) ||
		!e.PreviousWindowStart.Equal(t800) || !e.PreviousWindowEnd.Equal(t1000) || e.ToleranceMinutes != 15 || e.AppointmentVersion() != 2 || !e.OccurredAt().Equal(at) {
		t.Fatalf("rescheduled = %+v", e)
	}
	if a.Version() != 2 || a.DoorCode() != "WH1-DOCK-IN-02" || !a.Window().Start().Equal(t900) || !a.UpdatedAt().Equal(at) || a.State() != da.Booked {
		t.Fatalf("aggregate after reschedule: %+v", a.Snapshot())
	}
}

func TestRescheduleKeepsDoorWhenEmpty(t *testing.T) {
	a := mustBook(t, inbound())
	if evs, err := a.Reschedule("", t900, t1200, rules, now); err != nil || evs[0].(da.RescheduledEvent).PreviousDoorCode != "WH1-DOCK-IN-01" {
		t.Fatalf("keep door: %v %v", evs, err)
	}
	if a.DoorCode() != "WH1-DOCK-IN-01" {
		t.Fatalf("door = %s", a.DoorCode())
	}
}

func TestRescheduleClearsEtaAndRisk(t *testing.T) {
	a := mustBook(t, inbound())
	if _, err := a.ReportEta(t900, "carrier", rules, now); err != nil || a.AtRisk() != da.LateEta || a.Eta() == nil {
		t.Fatal("setup: late eta should flag risk")
	}
	if _, err := a.Reschedule("", t900, t1200, rules, now); err != nil {
		t.Fatal(err)
	}
	if a.AtRisk() != "" || a.Eta() != nil {
		t.Fatalf("risk %q eta %v after reschedule", a.AtRisk(), a.Eta())
	}
}

func TestRescheduleRejects(t *testing.T) {
	tests := []struct {
		name string
		door string
		s, e time.Time
		at   time.Time
		want error
	}{
		{"bad door", "a b", t900, t1200, now, nil},
		{"bad window", "", t1200, t900, now, da.ErrInvalidWindow},
		{"off grid", "", t900.Add(time.Minute), t1200, now, da.ErrInvalidWindow},
		{"too long", "", t800, t800.Add(5 * time.Hour), now, da.ErrInvalidWindow},
		{"in past", "", t900, t1200, t1000, da.ErrWindowInPast},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a := mustBook(t, inbound())
			evs, err := a.Reschedule(tc.door, tc.s, tc.e, rules, tc.at)
			if err == nil || evs != nil {
				t.Fatalf("Reschedule = %v, %v; want error", evs, err)
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("error = %v; want %v", err, tc.want)
			}
			if a.Version() != 1 || !a.Window().Start().Equal(t800) {
				t.Fatal("a refused reschedule must not change the aggregate")
			}
		})
	}
}

func TestRescheduleWindowStartingNowIsAllowed(t *testing.T) {
	a := mustBook(t, inbound())
	if _, err := a.Reschedule("", t900, t1200, rules, t900); err != nil {
		t.Fatalf("window starting at now: %v", err)
	}
}

// states drives an appointment into each state through legal commands only.
func inState(t *testing.T, s da.State) *da.DockAppointment {
	t.Helper()
	a := mustBook(t, inbound())
	var err error
	switch s {
	case da.Booked:
	case da.CheckedIn:
		_, err = a.RecordArrival(t800, "visit-1", rules, now)
	case da.Completed:
		_, err = a.RecordDoorRelease(t1000, now)
	case da.Cancelled:
		_, err = a.Cancel(da.CarrierRequest, now)
	case da.NoShow:
		_, err = a.MarkNoShow(rules, t1000.Add(time.Hour))
	}
	if err != nil {
		t.Fatalf("drive to %s: %v", s, err)
	}
	if a.State() != s {
		t.Fatalf("state = %s; want %s", a.State(), s)
	}
	return a
}

var allStates = []da.State{da.Booked, da.CheckedIn, da.Completed, da.Cancelled, da.NoShow}

func TestTransitionMatrix(t *testing.T) {
	type cmd struct {
		name  string
		legal map[da.State]bool
		run   func(*da.DockAppointment) ([]da.Event, error)
	}
	cmds := []cmd{
		{"Reschedule", map[da.State]bool{da.Booked: true}, func(a *da.DockAppointment) ([]da.Event, error) {
			return a.Reschedule("", t900, t1200, rules, now)
		}},
		{"Cancel", map[da.State]bool{da.Booked: true, da.CheckedIn: true}, func(a *da.DockAppointment) ([]da.Event, error) {
			return a.Cancel(da.SiteRequest, now)
		}},
		{"ReportEta", map[da.State]bool{da.Booked: true}, func(a *da.DockAppointment) ([]da.Event, error) {
			return a.ReportEta(t900, "api", rules, now)
		}},
		{"MarkAtRisk", map[da.State]bool{da.Booked: true}, func(a *da.DockAppointment) ([]da.Event, error) {
			return a.MarkAtRisk(da.DoorReassignedOverlap, now)
		}},
		{"RecordArrival", map[da.State]bool{da.Booked: true}, func(a *da.DockAppointment) ([]da.Event, error) {
			return a.RecordArrival(t800, "", rules, now)
		}},
		{"ReassignDoor", map[da.State]bool{da.Booked: true, da.CheckedIn: true}, func(a *da.DockAppointment) ([]da.Event, error) {
			return a.ReassignDoor("WH1-DOCK-IN-02", now)
		}},
		{"RecordDoorRelease", map[da.State]bool{da.Booked: true, da.CheckedIn: true}, func(a *da.DockAppointment) ([]da.Event, error) {
			return a.RecordDoorRelease(t1000, now)
		}},
		{"MarkNoShow", map[da.State]bool{da.Booked: true}, func(a *da.DockAppointment) ([]da.Event, error) {
			return a.MarkNoShow(rules, t1200.Add(24*time.Hour))
		}},
	}
	for _, c := range cmds {
		for _, s := range allStates {
			t.Run(c.name+"/"+string(s), func(t *testing.T) {
				checkTransition(t, s, c.legal[s], c.run)
			})
		}
	}
}

func checkTransition(t *testing.T, s da.State, legal bool, run func(*da.DockAppointment) ([]da.Event, error)) {
	t.Helper()
	a := inState(t, s)
	before := a.Version()
	evs, err := run(a)
	if legal {
		if err != nil {
			t.Fatalf("legal in %s but: %v", s, err)
		}
		if a.Version() != before+1 {
			t.Fatalf("version %d -> %d; want +1", before, a.Version())
		}
		return
	}
	if !errors.Is(err, da.ErrIllegalTransition) || evs != nil {
		t.Fatalf("illegal in %s: events %v err %v", s, evs, err)
	}
	if a.Version() != before || a.State() != s {
		t.Fatal("an illegal command must not change the aggregate")
	}
}

func TestCancel(t *testing.T) {
	a := mustBook(t, inbound())
	evs, err := a.Cancel(da.TurnedAway, later(time.Minute))
	mustEvents(t, evs, err, "DockAppointmentCancelled")
	e := evs[0].(da.CancelledEvent)
	if e.Reason != da.TurnedAway || e.DoorCode != "WH1-DOCK-IN-01" || e.AppointmentVersion() != 2 {
		t.Fatalf("cancelled = %+v", e)
	}
	if a.State() != da.Cancelled || a.CancellationReason() != da.TurnedAway {
		t.Fatalf("state %s reason %s", a.State(), a.CancellationReason())
	}
	if _, err := mustBook(t, inbound()).Cancel("BORED", now); !errors.Is(err, da.ErrInvalidReason) {
		t.Fatalf("invalid reason: %v", err)
	}
}

func TestCancelFromCheckedInClearsRisk(t *testing.T) {
	a := inState(t, da.CheckedIn)
	if _, err := a.Cancel(da.SiteRequest, now); err != nil || a.State() != da.Cancelled {
		t.Fatalf("cancel checked-in: %v", err)
	}
}

func TestReportEta(t *testing.T) {
	tol := 15 * time.Minute
	tests := []struct {
		name string
		eta  time.Time
		risk bool
	}{
		{"early", t800.Add(-time.Hour), false},
		{"at start", t800, false},
		{"exactly at tolerance", t800.Add(tol), false},
		{"one second late", t800.Add(tol + time.Second), true},
		{"hours late", t1200, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a := mustBook(t, inbound())
			at := later(time.Minute)
			evs, err := a.ReportEta(tc.eta, "carrier-portal", rules, at)
			if err != nil {
				t.Fatal(err)
			}
			assertEtaStored(t, a, tc.eta, at)
			if tc.risk {
				assertLateEtaRisk(t, a, evs, tc.eta)
			} else if evs != nil || a.AtRisk() != "" {
				t.Fatalf("no risk expected: %v %q", evs, a.AtRisk())
			}
		})
	}
}

func assertEtaStored(t *testing.T, a *da.DockAppointment, eta, reportedAt time.Time) {
	t.Helper()
	if a.Version() != 2 || a.Eta() == nil || !a.Eta().At.Equal(eta) || a.Eta().Source != "carrier-portal" || !a.Eta().ReportedAt.Equal(reportedAt) {
		t.Fatalf("eta not stored: %+v v%d", a.Eta(), a.Version())
	}
}

func assertLateEtaRisk(t *testing.T, a *da.DockAppointment, evs []da.Event, eta time.Time) {
	t.Helper()
	mustEvents(t, evs, nil, "DockAppointmentAtRisk")
	r := evs[0].(da.AtRisk)
	if r.Cause != da.LateEta || !r.EtaAt.Equal(eta) || !r.WindowStart.Equal(t800) || !r.WindowEnd.Equal(t1000) || r.DoorCode != "WH1-DOCK-IN-01" || a.AtRisk() != da.LateEta {
		t.Fatalf("at risk = %+v flag %q", r, a.AtRisk())
	}
}

func TestReportEtaClearsOnlyLateEtaRisk(t *testing.T) {
	a := mustBook(t, inbound())
	_, _ = a.ReportEta(t1200, "api", rules, now)
	if _, err := a.ReportEta(t800, "api", rules, now); err != nil || a.AtRisk() != "" {
		t.Fatalf("on-time eta should clear LATE_ETA: %v %q", err, a.AtRisk())
	}
	b := mustBook(t, inbound())
	_, _ = b.MarkAtRisk(da.DoorReassignedOverlap, now)
	if _, err := b.ReportEta(t800, "api", rules, now); err != nil || b.AtRisk() != da.DoorReassignedOverlap {
		t.Fatalf("on-time eta must keep an overlap risk: %v %q", err, b.AtRisk())
	}
}

func TestReportEtaRejects(t *testing.T) {
	long := string(make([]byte, 65))
	for name, c := range map[string]struct {
		eta    time.Time
		source string
	}{"zero eta": {time.Time{}, "api"}, "blank source": {t900, "  "}, "source too long": {t900, long}} {
		t.Run(name, func(t *testing.T) {
			a := mustBook(t, inbound())
			if evs, err := a.ReportEta(c.eta, c.source, rules, now); !errors.Is(err, da.ErrInvalidEta) || evs != nil {
				t.Fatalf("ReportEta = %v, %v", evs, err)
			}
			if a.Version() != 1 {
				t.Fatal("refused report changed the aggregate")
			}
		})
	}
	a := mustBook(t, inbound())
	if _, err := a.ReportEta(t900, "x", rules, now); err != nil {
		t.Fatalf("one-character source: %v", err)
	}
	maxSrc := ""
	for i := 0; i < 64; i++ {
		maxSrc += "s"
	}
	if _, err := a.ReportEta(t900, maxSrc, rules, now); err != nil {
		t.Fatalf("64-character source: %v", err)
	}
}

func TestMarkAtRisk(t *testing.T) {
	a := mustBook(t, inbound())
	evs, err := a.MarkAtRisk(da.DoorReassignedOverlap, later(time.Minute))
	mustEvents(t, evs, err, "DockAppointmentAtRisk")
	r := evs[0].(da.AtRisk)
	if r.Cause != da.DoorReassignedOverlap || !r.EtaAt.IsZero() {
		t.Fatalf("at risk = %+v", r)
	}
	if a.AtRisk() != da.DoorReassignedOverlap || a.Version() != 2 {
		t.Fatalf("flag %q version %d", a.AtRisk(), a.Version())
	}
}

func TestRecordArrival(t *testing.T) {
	tests := []struct {
		name string
		at   time.Time
		want da.ArrivalStatus
	}{
		{"early", t800.Add(-time.Hour), da.Early},
		{"on time", t800.Add(5 * time.Minute), da.OnTime},
		{"late", t800.Add(time.Hour), da.Late},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a := mustBook(t, inbound())
			_, _ = a.ReportEta(t1200, "api", rules, now)
			at := later(2 * time.Hour)
			evs, err := a.RecordArrival(tc.at, "visit-1", rules, at)
			mustEvents(t, evs, err, "DockAppointmentCheckedIn")
			e := evs[0].(da.CheckedInEvent)
			if e.ArrivalStatus != tc.want || !e.CheckedInAt.Equal(tc.at) || e.VisitID != "visit-1" || e.DoorCode != "WH1-DOCK-IN-01" {
				t.Fatalf("checked in = %+v", e)
			}
			arr := a.Arrival()
			if a.State() != da.CheckedIn || arr == nil || arr.Status != tc.want || arr.Implicit || arr.VisitID != "visit-1" || !arr.At.Equal(tc.at) || a.AtRisk() != "" {
				t.Fatalf("aggregate: state %s arrival %+v risk %q", a.State(), arr, a.AtRisk())
			}
		})
	}
}

func TestReassignDoor(t *testing.T) {
	a := mustBook(t, inbound())
	evs, err := a.ReassignDoor("WH1-DOCK-IN-02", later(time.Minute))
	mustEvents(t, evs, err, "DockAppointmentDoorReassigned")
	e := evs[0].(da.DoorReassigned)
	if e.FromDoorCode != "WH1-DOCK-IN-01" || e.ToDoorCode != "WH1-DOCK-IN-02" || a.DoorCode() != "WH1-DOCK-IN-02" || a.Version() != 2 {
		t.Fatalf("reassigned = %+v door %s", e, a.DoorCode())
	}
	if evs, err := a.ReassignDoor("WH1-DOCK-IN-02", now); err != nil || evs != nil || a.Version() != 2 {
		t.Fatalf("same door is a no-op: %v %v v%d", evs, err, a.Version())
	}
	if _, err := a.ReassignDoor("bad door", now); err == nil {
		t.Fatal("invalid door code must be refused")
	}
}

func TestRecordDoorRelease(t *testing.T) {
	released := t1000.Add(-20 * time.Minute)
	a := inState(t, da.CheckedIn)
	evs, err := a.RecordDoorRelease(released, later(time.Hour))
	mustEvents(t, evs, err, "DockAppointmentCompleted")
	e := evs[0].(da.CompletedEvent)
	if !e.CompletedAt.Equal(released) || e.DoorCode != "WH1-DOCK-IN-01" || a.State() != da.Completed || !a.CompletedAt().Equal(released) {
		t.Fatalf("completed = %+v", e)
	}
	if arr := a.Arrival(); arr == nil || arr.Implicit || arr.Status != da.OnTime {
		t.Fatalf("a checked-in release keeps the recorded arrival: %+v", arr)
	}

	b := mustBook(t, inbound())
	if evs, err := b.RecordDoorRelease(released, now); err != nil || len(evs) != 1 || b.State() != da.Completed {
		t.Fatalf("BOOKED release: %v %v", evs, err)
	}
	if arr := b.Arrival(); arr == nil || !arr.Implicit || arr.Status != da.OnTime || !arr.At.Equal(released) {
		t.Fatalf("implicit arrival = %+v", arr)
	}
}

func TestMarkNoShow(t *testing.T) {
	a := mustBook(t, inbound())
	due := t1000.Add(60 * time.Minute)
	if !a.NoShowDueAt(rules).Equal(due) {
		t.Fatalf("due at %v", a.NoShowDueAt(rules))
	}
	if evs, err := a.MarkNoShow(rules, due.Add(-time.Nanosecond)); !errors.Is(err, da.ErrNoShowTooEarly) || evs != nil {
		t.Fatalf("one tick early: %v %v", evs, err)
	}
	if a.State() != da.Booked || a.Version() != 1 {
		t.Fatal("a too-early sweep must not change the aggregate")
	}
	_, _ = a.ReportEta(t1200, "api", rules, now)
	evs, err := a.MarkNoShow(rules, due)
	mustEvents(t, evs, err, "DockAppointmentNoShow")
	e := evs[0].(da.NoShowEvent)
	if e.Direction != "INBOUND" || e.CarrierSCAC != "ACMF" || !e.WindowEnd.Equal(t1000) || e.DoorCode != "WH1-DOCK-IN-01" || !e.OccurredAt().Equal(due) {
		t.Fatalf("no show = %+v", e)
	}
	if a.State() != da.NoShow || a.AtRisk() != "" {
		t.Fatalf("state %s risk %q", a.State(), a.AtRisk())
	}
}
