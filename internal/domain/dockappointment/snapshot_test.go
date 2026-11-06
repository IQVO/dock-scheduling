package dockappointment_test

import (
	"errors"
	"reflect"
	"testing"
	"time"

	da "github.com/claudioed/dock-scheduling/internal/domain/dockappointment"
)

func TestSnapshotRestoreRoundTrip(t *testing.T) {
	for _, st := range allStates {
		t.Run(string(st), func(t *testing.T) {
			a := inState(t, st)
			s := a.Snapshot()
			b, err := da.Restore(s)
			if err != nil {
				t.Fatalf("Restore: %v", err)
			}
			if !reflect.DeepEqual(s, b.Snapshot()) {
				t.Fatalf("round trip differs:\n%+v\n%+v", s, b.Snapshot())
			}
			if b.State() != st || b.Version() != a.Version() || b.Provenance() != a.Provenance() {
				t.Fatal("accessors differ after Restore")
			}
		})
	}
}

func TestSnapshotRestoreWithEtaRiskAndOutbound(t *testing.T) {
	a := mustBook(t, outbound())
	_, _ = a.ReportEta(t1200, "carrier", rules, now)
	b, err := da.Restore(a.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	if b.AtRisk() != da.LateEta || b.Eta() == nil || b.Eta().Source != "carrier" || b.CPTID() != "WH1-1500" || len(b.ShipmentRefs()) != 1 {
		t.Fatalf("restored = %+v", b.Snapshot())
	}
}

func TestRestoreAllowsWindowInThePast(t *testing.T) {
	s := inState(t, da.Booked).Snapshot()
	s.WindowStart, s.WindowEnd = now.AddDate(-1, 0, 0), now.AddDate(-1, 0, 0).Add(time.Hour)
	if _, err := da.Restore(s); err != nil {
		t.Fatalf("a past window is a valid stored row: %v", err)
	}
}

func TestRestoreBoundaries(t *testing.T) {
	s := inState(t, da.Booked).Snapshot()
	s.Version = 1
	if _, err := da.Restore(s); err != nil {
		t.Fatalf("version 1 is valid: %v", err)
	}
	s.Version = 0
	if _, err := da.Restore(s); !errors.Is(err, da.ErrInvalidVersion) {
		t.Fatalf("version 0: %v", err)
	}
	s.Version = 1
	s.WindowEnd = s.WindowStart
	if _, err := da.Restore(s); !errors.Is(err, da.ErrInvalidWindow) {
		t.Fatalf("empty window: %v", err)
	}
	s.WindowEnd = s.WindowStart.Add(time.Nanosecond)
	if _, err := da.Restore(s); err != nil {
		t.Fatalf("one-nanosecond window is structurally valid: %v", err)
	}
}

func TestRestoreRejectsCorruptRows(t *testing.T) {
	tests := []struct {
		name string
		st   da.State
		mut  func(*da.Snapshot)
		want error
	}{
		{"unknown state", da.Booked, func(s *da.Snapshot) { s.State = "BOGUS" }, da.ErrInvalidState},
		{"unknown provenance", da.Booked, func(s *da.Snapshot) { s.Provenance = "x" }, da.ErrInvalidSnapshot},
		{"bad id", da.Booked, func(s *da.Snapshot) { s.ID = "x" }, da.ErrInvalidAppointmentID},
		{"bad site", da.Booked, func(s *da.Snapshot) { s.SiteID = "" }, nil},
		{"bad door", da.Booked, func(s *da.Snapshot) { s.DoorCode = "" }, nil},
		{"bad direction", da.Booked, func(s *da.Snapshot) { s.Direction = "x" }, nil},
		{"bad carrier", da.Booked, func(s *da.Snapshot) { s.Carrier = "" }, nil},
		{"inbound without asns", da.Booked, func(s *da.Snapshot) { s.ASNNumbers = nil }, da.ErrUnknownAsn},
		{"inbound with cpt", da.Booked, func(s *da.Snapshot) { s.CPTID = "x" }, da.ErrInvalidSnapshot},
		{"inbound with shipments", da.Booked, func(s *da.Snapshot) { s.ShipmentRefs = []string{"x"} }, da.ErrInvalidSnapshot},
		{"outbound without refs", da.Booked, func(s *da.Snapshot) {
			s.Direction, s.ASNNumbers, s.CPTID, s.ShipmentRefs = "OUTBOUND", nil, "", nil
		}, da.ErrMissingOutboundRef},
		{"unknown risk cause", da.Booked, func(s *da.Snapshot) { s.AtRisk = "x" }, da.ErrInvalidSnapshot},
		{"risk on terminal", da.Cancelled, func(s *da.Snapshot) { s.AtRisk = da.LateEta }, da.ErrInvalidSnapshot},
		{"cancelled without reason", da.Cancelled, func(s *da.Snapshot) { s.CancellationReason = "" }, da.ErrInvalidSnapshot},
		{"reason on booked", da.Booked, func(s *da.Snapshot) { s.CancellationReason = da.SiteRequest }, da.ErrInvalidSnapshot},
		{"invalid reason", da.Cancelled, func(s *da.Snapshot) { s.CancellationReason = "x" }, da.ErrInvalidReason},
		{"completed without time", da.Completed, func(s *da.Snapshot) { s.CompletedAt = time.Time{} }, da.ErrInvalidSnapshot},
		{"completion time on booked", da.Booked, func(s *da.Snapshot) { s.CompletedAt = now }, da.ErrInvalidSnapshot},
		{"checked in without arrival", da.CheckedIn, func(s *da.Snapshot) { s.Arrival = nil }, da.ErrInvalidSnapshot},
		{"completed without arrival", da.Completed, func(s *da.Snapshot) { s.Arrival = nil }, da.ErrInvalidSnapshot},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := inState(t, tc.st).Snapshot()
			tc.mut(&s)
			a, err := da.Restore(s)
			if err == nil || a != nil {
				t.Fatalf("Restore = %v, %v; want error", a, err)
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("error = %v; want %v", err, tc.want)
			}
		})
	}
}

func legacy() da.LegacyParams {
	return da.LegacyParams{ID: apptID, SiteID: "WH1", DoorCode: "WH1-DOCK-IN-01", Carrier: "ACME Freight", WindowStart: t800.Add(time.Minute),
		WindowEnd: t1000, ASNNumbers: []string{"ASN-9"}, Rules: rules, Now: t1200}
}

func TestImportLegacyEnforcesNoBookingRule(t *testing.T) {
	// window off the grid, in the past (now is after the window) and unknown ASN: all accepted
	a, events, err := da.ImportLegacy(legacy())
	evs := mustEvents(t, events, err, "DockAppointmentBooked")
	b := evs[0].(da.BookedEvent)
	if a.Provenance() != da.LegacyImport || a.Direction() != "INBOUND" || a.Carrier().SCAC() != "" || a.State() != da.Booked || a.Version() != 1 {
		t.Fatalf("imported = %+v", a.Snapshot())
	}
	if b.Provenance != da.LegacyImport || b.CarrierSCAC != "" || b.ToleranceMinutes != 15 || len(b.ASNNumbers) != 1 || b.Direction != "INBOUND" {
		t.Fatalf("booked event = %+v", b)
	}
	if !a.CreatedAt().Equal(t1200) {
		t.Fatalf("created at %v", a.CreatedAt())
	}
}

func TestImportLegacyRejectsBrokenShape(t *testing.T) {
	tests := map[string]func(*da.LegacyParams){
		"bad id":      func(p *da.LegacyParams) { p.ID = "x" },
		"bad site":    func(p *da.LegacyParams) { p.SiteID = "" },
		"bad door":    func(p *da.LegacyParams) { p.DoorCode = "" },
		"bad carrier": func(p *da.LegacyParams) { p.Carrier = "" },
		"bad window":  func(p *da.LegacyParams) { p.WindowEnd = p.WindowStart },
		"no asns":     func(p *da.LegacyParams) { p.ASNNumbers = nil },
		"dup asns":    func(p *da.LegacyParams) { p.ASNNumbers = []string{"A", "A"} },
	}
	for name, mut := range tests {
		t.Run(name, func(t *testing.T) {
			p := legacy()
			mut(&p)
			if a, evs, err := da.ImportLegacy(p); err == nil || a != nil || evs != nil {
				t.Fatalf("ImportLegacy = %v %v %v", a, evs, err)
			}
		})
	}
}

func TestLegacyFollowUpEventsUseTheNormalCommands(t *testing.T) {
	a, _, _ := da.ImportLegacy(legacy())
	if _, err := a.RecordArrival(t900, "", rules, t1200); err != nil || a.State() != da.CheckedIn {
		t.Fatalf("legacy check-in: %v", err)
	}
	if _, err := a.RecordDoorRelease(t1000, t1200); err != nil || a.State() != da.Completed {
		t.Fatalf("legacy completion: %v", err)
	}
	b, _, _ := da.ImportLegacy(legacy())
	if _, err := b.Cancel(da.CarrierRequest, t1200); err != nil || b.State() != da.Cancelled {
		t.Fatalf("legacy cancel: %v", err)
	}
}
