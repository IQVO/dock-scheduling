package dockappointment_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	da "github.com/claudioed/dock-scheduling/internal/domain/dockappointment"
)

const (
	apptID  = "appt-123e4567-e89b-12d3-a456-426614174000"
	apptID2 = "appt-223e4567-e89b-12d3-a456-426614174000"
)

var (
	now   = time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	t800  = time.Date(2026, 11, 6, 8, 0, 0, 0, time.UTC)
	t900  = time.Date(2026, 11, 6, 9, 0, 0, 0, time.UTC)
	t1000 = time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	t1200 = time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)

	rules = da.Rules{MaxWindow: da.HardMaxWindow, Granularity: 15 * time.Minute, ArrivalTolerance: 15 * time.Minute, NoShowGrace: 60 * time.Minute}
)

func inbound() da.BookParams {
	return da.BookParams{
		ID: apptID, SiteID: "WH1", DoorCode: "WH1-DOCK-IN-01", Direction: "INBOUND", Carrier: "ACME Freight", CarrierSCAC: "ACMF",
		WindowStart: t800, WindowEnd: t1000, ASNNumbers: []string{"ASN-1", "ASN-2"}, ExpectedSeal: "SEAL-1", Rules: rules, Now: now,
	}
}

func outbound() da.BookParams {
	p := inbound()
	p.Direction, p.ASNNumbers, p.CPTID, p.ShipmentRefs = "OUTBOUND", nil, "WH1-1500", []string{"SHP-1"}
	return p
}

func mustBook(t *testing.T, p da.BookParams) *da.DockAppointment {
	t.Helper()
	a, _, err := da.Book(p)
	if err != nil {
		t.Fatalf("Book: %v", err)
	}
	return a
}

func eventNames(events []da.Event) []string {
	names := make([]string, len(events))
	for i, e := range events {
		names[i] = e.EventName()
	}
	return names
}

func mustEvents(t *testing.T, events []da.Event, err error, want ...string) []da.Event {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := eventNames(events)
	if len(got) != len(want) {
		t.Fatalf("events = %v; want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("events = %v; want %v", got, want)
		}
	}
	return events
}

func TestBookInbound(t *testing.T) {
	a, events, err := da.Book(inbound())
	evs := mustEvents(t, events, err, "DockAppointmentBooked")
	assertBookedAggregate(t, a)
	assertBookedEvent(t, evs[0].(da.BookedEvent))
}

func eq[T comparable](t *testing.T, what string, got, want T) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %v; want %v", what, got, want)
	}
}

func assertBookedAggregate(t *testing.T, a *da.DockAppointment) {
	t.Helper()
	eq(t, "state", a.State(), da.Booked)
	eq(t, "version", a.Version(), int64(1))
	eq(t, "provenance", a.Provenance(), da.Native)
	eq(t, "id", a.ID().String(), apptID)
	eq(t, "door", a.DoorCode().String(), "WH1-DOCK-IN-01")
	eq(t, "site", a.SiteID().String(), "WH1")
	eq(t, "direction", string(a.Direction()), "INBOUND")
	eq(t, "carrier", a.Carrier().Name(), "ACME Freight")
	eq(t, "scac", a.Carrier().SCAC(), "ACMF")
	eq(t, "seal", a.ExpectedSeal(), "SEAL-1")
	eq(t, "window start", a.Window().Start(), t800)
	eq(t, "window end", a.Window().End(), t1000)
	eq(t, "asns", strings.Join(a.ASNNumbers(), ","), "ASN-1,ASN-2")
	eq(t, "created", a.CreatedAt(), now)
	eq(t, "updated", a.UpdatedAt(), now)
	eq(t, "cpt", a.CPTID(), "")
	eq(t, "shipments", len(a.ShipmentRefs()), 0)
}

func assertBookedEvent(t *testing.T, b da.BookedEvent) {
	t.Helper()
	eq(t, "appointment id", b.AppointmentID(), apptID)
	eq(t, "version", b.AppointmentVersion(), int64(1))
	eq(t, "occurred at", b.OccurredAt(), now)
	eq(t, "site", b.SiteID(), "WH1")
	eq(t, "direction", string(b.Direction), "INBOUND")
	eq(t, "door", b.DoorCode, "WH1-DOCK-IN-01")
	eq(t, "carrier", b.Carrier, "ACME Freight")
	eq(t, "scac", b.CarrierSCAC, "ACMF")
	eq(t, "window start", b.WindowStart, t800)
	eq(t, "window end", b.WindowEnd, t1000)
	eq(t, "asns", strings.Join(b.ASNNumbers, ","), "ASN-1,ASN-2")
	eq(t, "seal", b.ExpectedSeal, "SEAL-1")
	eq(t, "tolerance", b.ToleranceMinutes, 15)
	eq(t, "provenance", b.Provenance, da.Native)
	eq(t, "cpt", b.CPTID, "")
}

func TestBookOutbound(t *testing.T) {
	a, events, err := da.Book(outbound())
	mustEvents(t, events, err, "DockAppointmentBooked")
	if a.Direction() != "OUTBOUND" || a.CPTID() != "WH1-1500" || len(a.ShipmentRefs()) != 1 || len(a.ASNNumbers()) != 0 {
		t.Fatalf("outbound refs wrong: %+v", a.Snapshot())
	}
	p := outbound()
	p.CPTID = ""
	if _, _, err := da.Book(p); err != nil {
		t.Fatalf("shipment refs only: %v", err)
	}
	p = outbound()
	p.ShipmentRefs = nil
	if _, _, err := da.Book(p); err != nil {
		t.Fatalf("cpt only: %v", err)
	}
}

func TestBookWindowMayStartAtNowButNotBefore(t *testing.T) {
	p := inbound()
	p.WindowStart, p.WindowEnd, p.Now = t800, t1000, t800
	if _, _, err := da.Book(p); err != nil {
		t.Fatalf("window starting at now: %v", err)
	}
	p.Now = t800.Add(time.Second)
	if _, _, err := da.Book(p); !errors.Is(err, da.ErrWindowInPast) {
		t.Fatalf("window one second in the past: %v", err)
	}
}

func TestBookRejects(t *testing.T) {
	tests := []struct {
		name string
		mut  func(*da.BookParams)
		want error
	}{
		{"bad id", func(p *da.BookParams) { p.ID = "appt-1" }, da.ErrInvalidAppointmentID},
		{"upper-case id", func(p *da.BookParams) { p.ID = "appt-123E4567-e89b-12d3-a456-426614174000" }, da.ErrInvalidAppointmentID},
		{"bad site", func(p *da.BookParams) { p.SiteID = "" }, nil},
		{"bad door", func(p *da.BookParams) { p.DoorCode = "a b" }, nil},
		{"bad direction", func(p *da.BookParams) { p.Direction = "SIDEWAYS" }, nil},
		{"bad carrier", func(p *da.BookParams) { p.Carrier = " " }, nil},
		{"bad scac", func(p *da.BookParams) { p.CarrierSCAC = "x" }, nil},
		{"window reversed", func(p *da.BookParams) { p.WindowStart, p.WindowEnd = t1000, t800 }, da.ErrInvalidWindow},
		{"window empty", func(p *da.BookParams) { p.WindowEnd = p.WindowStart }, da.ErrInvalidWindow},
		{"window too long", func(p *da.BookParams) { p.WindowEnd = t800.Add(4*time.Hour + 15*time.Minute) }, da.ErrInvalidWindow},
		{"off grid start", func(p *da.BookParams) { p.WindowStart = t800.Add(time.Minute) }, da.ErrInvalidWindow},
		{"off grid end", func(p *da.BookParams) { p.WindowEnd = t1000.Add(time.Minute) }, da.ErrInvalidWindow},
		{"fractional start", func(p *da.BookParams) { p.WindowStart = t800.Add(time.Nanosecond) }, da.ErrInvalidWindow},
		{"fractional end", func(p *da.BookParams) { p.WindowEnd = t1000.Add(time.Nanosecond) }, da.ErrInvalidWindow},
		{"in past", func(p *da.BookParams) { p.Now = t900 }, da.ErrWindowInPast},
		{"inbound no asn", func(p *da.BookParams) { p.ASNNumbers = nil }, da.ErrUnknownAsn},
		{"inbound blank asn", func(p *da.BookParams) { p.ASNNumbers = []string{" "} }, da.ErrUnknownAsn},
		{"inbound duplicate asn", func(p *da.BookParams) { p.ASNNumbers = []string{"A", "A"} }, da.ErrDuplicateAsn},
		{"inbound with cpt", func(p *da.BookParams) { p.CPTID = "x" }, da.ErrInvalidRefs},
		{"inbound with shipments", func(p *da.BookParams) { p.ShipmentRefs = []string{"x"} }, da.ErrInvalidRefs},
		{"outbound with asn", func(p *da.BookParams) { *p = outbound(); p.ASNNumbers = []string{"A"} }, da.ErrInvalidRefs},
		{"outbound no refs", func(p *da.BookParams) { *p = outbound(); p.CPTID, p.ShipmentRefs = "", nil }, da.ErrMissingOutboundRef},
		{"outbound blank shipment", func(p *da.BookParams) { *p = outbound(); p.ShipmentRefs = []string{""} }, da.ErrInvalidRefs},
		{"outbound control cpt", func(p *da.BookParams) { *p = outbound(); p.CPTID = "a\x00" }, da.ErrInvalidRefs},
		{"seal control char", func(p *da.BookParams) { p.ExpectedSeal = "S\x01" }, da.ErrInvalidRefs},
		{"seal too long", func(p *da.BookParams) { p.ExpectedSeal = strings.Repeat("s", 65) }, da.ErrInvalidRefs},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := inbound()
			tc.mut(&p)
			a, events, err := da.Book(p)
			if err == nil || a != nil || events != nil {
				t.Fatalf("Book = %v, %v, %v; want error", a, events, err)
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("error = %v; want %v", err, tc.want)
			}
		})
	}
}

func TestBookWindowLimits(t *testing.T) {
	p := inbound()
	p.WindowEnd = t800.Add(da.HardMaxWindow)
	if _, _, err := da.Book(p); err != nil {
		t.Fatalf("exactly four hours: %v", err)
	}
	p.Rules.MaxWindow = 2 * time.Hour
	if _, _, err := da.Book(p); !errors.Is(err, da.ErrInvalidWindow) {
		t.Fatalf("site max 2h, window 4h: %v", err)
	}
	p.WindowEnd = t800.Add(2 * time.Hour)
	if _, _, err := da.Book(p); err != nil {
		t.Fatalf("site max 2h, window 2h: %v", err)
	}
	p.Rules.MaxWindow = 10 * time.Hour // a site may lower the cap, never raise it
	p.WindowEnd = t800.Add(5 * time.Hour)
	if _, _, err := da.Book(p); !errors.Is(err, da.ErrInvalidWindow) {
		t.Fatalf("site max above hard cap: %v", err)
	}
	p.Rules = da.Rules{} // zero rules: hard cap, no grid
	p.WindowStart, p.WindowEnd = t800.Add(time.Minute), t800.Add(time.Minute+time.Hour)
	if _, _, err := da.Book(p); err != nil {
		t.Fatalf("zero rules: %v", err)
	}
	if da.HardMaxWindow != 4*time.Hour || da.DefaultMaxWindow != da.HardMaxWindow {
		t.Fatal("hard window is 4 hours")
	}
}

func TestWindowOverlapsIsHalfOpen(t *testing.T) {
	w := func(a, b time.Time) da.Window {
		x, err := da.NewWindow(a, b, rules)
		if err != nil {
			t.Fatal(err)
		}
		return x
	}
	a := w(t800, t1000)
	tests := []struct {
		name string
		b    da.Window
		want bool
	}{
		{"identical", w(t800, t1000), true},
		{"adjacent after", w(t1000, t1200), false},
		{"adjacent before", w(t800.Add(-2*time.Hour), t800), false},
		{"overlap tail", w(t900, t1200), true},
		{"overlap head", w(t800.Add(-time.Hour), t900), true},
		{"contained", w(t800.Add(15*time.Minute), t900), true},
		{"containing", w(t800.Add(-time.Hour), t1000.Add(time.Hour)), true},
		{"disjoint", w(t1200, t1200.Add(time.Hour)), false},
	}
	for _, tc := range tests {
		if got := a.Overlaps(tc.b); got != tc.want {
			t.Errorf("%s: a.Overlaps(b) = %v; want %v", tc.name, got, tc.want)
		}
		if got := tc.b.Overlaps(a); got != tc.want {
			t.Errorf("%s: b.Overlaps(a) = %v; want %v", tc.name, got, tc.want)
		}
	}
}

func TestClassifyArrival(t *testing.T) {
	w, _ := da.NewWindow(t800, t1000, rules)
	tol := 15 * time.Minute
	tests := []struct {
		at   time.Time
		want da.ArrivalStatus
	}{
		{t800.Add(-tol - time.Second), da.Early},
		{t800.Add(-tol), da.OnTime},
		{t800, da.OnTime},
		{t800.Add(tol), da.OnTime},
		{t800.Add(tol + time.Second), da.Late},
		{t1000.Add(time.Hour), da.Late},
	}
	for _, tc := range tests {
		if got := da.Classify(w, tol, tc.at); got != tc.want {
			t.Errorf("Classify(%v) = %s; want %s", tc.at, got, tc.want)
		}
	}
	if got := da.Classify(w, 0, t800); got != da.OnTime {
		t.Errorf("zero tolerance at start = %s", got)
	}
}

func TestStatePredicates(t *testing.T) {
	for _, s := range []da.State{da.Booked, da.CheckedIn} {
		if !s.Active() || s.Terminal() {
			t.Errorf("%s should be active and not terminal", s)
		}
	}
	for _, s := range []da.State{da.Completed, da.Cancelled, da.NoShow} {
		if s.Active() || !s.Terminal() {
			t.Errorf("%s should be terminal and not active", s)
		}
	}
	if da.State("BOGUS").Terminal() || da.State("BOGUS").Active() {
		t.Error("an unknown state is neither active nor terminal")
	}
}

func TestParseReason(t *testing.T) {
	for _, r := range []da.Reason{da.CarrierRequest, da.SiteRequest, da.AsnCancelled, da.TurnedAway} {
		got, err := da.ParseReason(string(r))
		if err != nil || got != r {
			t.Fatalf("ParseReason(%s) = %s, %v", r, got, err)
		}
	}
	if _, err := da.ParseReason("BORED"); !errors.Is(err, da.ErrInvalidReason) {
		t.Fatalf("unknown reason: %v", err)
	}
}

func TestNewID(t *testing.T) {
	if id, err := da.NewID(apptID); err != nil || id.String() != apptID {
		t.Fatalf("NewID = %v, %v", id, err)
	}
	for _, bad := range []string{"", "appt-", "x" + apptID, apptID + "x", "appt-123e4567-e89b-12d3-a456-42661417400"} {
		if _, err := da.NewID(bad); !errors.Is(err, da.ErrInvalidAppointmentID) {
			t.Errorf("NewID(%q) = %v", bad, err)
		}
	}
}

func TestToleranceMinutes(t *testing.T) {
	if got := (da.Rules{ArrivalTolerance: 90 * time.Minute}).ToleranceMinutes(); got != 90 {
		t.Fatalf("ToleranceMinutes = %d", got)
	}
}
