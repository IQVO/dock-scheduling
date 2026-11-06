package dockappointment_test

import (
	"errors"
	"testing"
	"time"

	da "github.com/claudioed/dock-scheduling/internal/domain/dockappointment"
	"github.com/claudioed/dock-scheduling/internal/domain/shared"
)

func bookAt(t *testing.T, id, door string, s, e time.Time) *da.DockAppointment {
	t.Helper()
	p := inbound()
	p.ID, p.DoorCode, p.WindowStart, p.WindowEnd = id, door, s, e
	return mustBook(t, p)
}

func TestCheckDoor(t *testing.T) {
	var s da.DoorSchedule
	door := func(flow shared.DockFlow) *da.Door {
		return &da.Door{Code: "WH1-DOCK-IN-01", Site: "WH1", Flow: flow}
	}
	tests := []struct {
		name string
		door *da.Door
		dir  shared.Direction
		want error
	}{
		{"unknown door", nil, shared.Inbound, da.ErrUnknownDoor},
		{"inbound on inbound", door(shared.FlowInbound), shared.Inbound, nil},
		{"outbound on inbound", door(shared.FlowInbound), shared.Outbound, da.ErrDoorFlowMismatch},
		{"outbound on outbound", door(shared.FlowOutbound), shared.Outbound, nil},
		{"inbound on outbound", door(shared.FlowOutbound), shared.Inbound, da.ErrDoorFlowMismatch},
		{"inbound on both", door(shared.FlowBoth), shared.Inbound, nil},
		{"outbound on both", door(shared.FlowBoth), shared.Outbound, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := s.CheckDoor(tc.door, tc.dir); !errors.Is(err, tc.want) {
				t.Fatalf("CheckDoor = %v; want %v", err, tc.want)
			}
		})
	}
}

func TestCheckNoOverlap(t *testing.T) {
	var s da.DoorSchedule
	cand := bookAt(t, apptID, "D1", t900, t1000)
	other := func(door string, st, en time.Time) *da.DockAppointment { return bookAt(t, apptID2, door, st, en) }
	tests := []struct {
		name   string
		others []*da.DockAppointment
		want   error
	}{
		{"none", nil, nil},
		{"overlap", []*da.DockAppointment{other("D1", t800, t1000)}, da.ErrDoorDoubleBooked},
		{"same window", []*da.DockAppointment{other("D1", t900, t1000)}, da.ErrDoorDoubleBooked},
		{"adjacent before", []*da.DockAppointment{other("D1", t800, t900)}, nil},
		{"adjacent after", []*da.DockAppointment{other("D1", t1000, t1200)}, nil},
		{"other door", []*da.DockAppointment{other("D2", t900, t1000)}, nil},
		{"candidate itself", []*da.DockAppointment{cand}, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := s.CheckNoOverlap(cand, tc.others); !errors.Is(err, tc.want) {
				t.Fatalf("CheckNoOverlap = %v; want %v", err, tc.want)
			}
		})
	}
	for _, st := range allStates {
		o := bookAt(t, apptID2, "D1", t900, t1000)
		o2 := inStateOf(t, o, st)
		err := s.CheckNoOverlap(cand, []*da.DockAppointment{o2})
		if st.Active() != errors.Is(err, da.ErrDoorDoubleBooked) {
			t.Errorf("an appointment in %s: overlap refusal = %v", st, err)
		}
	}
}

// inStateOf drives an appointment into state st.
func inStateOf(t *testing.T, a *da.DockAppointment, st da.State) *da.DockAppointment {
	t.Helper()
	var err error
	switch st {
	case da.CheckedIn:
		_, err = a.RecordArrival(t900, "", rules, now)
	case da.Completed:
		_, err = a.RecordDoorRelease(t1000, now)
	case da.Cancelled:
		_, err = a.Cancel(da.SiteRequest, now)
	case da.NoShow:
		_, err = a.MarkNoShow(rules, t1200.Add(24*time.Hour))
	}
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestOutboundAndInboundShareOneCalendar(t *testing.T) {
	var s da.DoorSchedule
	in := bookAt(t, apptID, "D1", t800, t1000)
	p := outbound()
	p.ID, p.DoorCode, p.WindowStart, p.WindowEnd = apptID2, "D1", t900, t1200
	out := mustBook(t, p)
	if err := s.CheckNoOverlap(out, []*da.DockAppointment{in}); !errors.Is(err, da.ErrDoorDoubleBooked) {
		t.Fatalf("cross-direction overlap on one door: %v", err)
	}
}

func TestReferencesCheck(t *testing.T) {
	var r da.References
	in := mustBook(t, inbound()) // ASN-1, ASN-2
	reg := map[string]da.AsnStatus{"ASN-1": da.AsnRegistered, "ASN-2": da.AsnRegistered}
	tests := []struct {
		name string
		a    *da.DockAppointment
		k    da.KnownRefs
		want error
	}{
		{"all known", in, da.KnownRefs{ASNs: reg}, nil},
		{"one unknown", in, da.KnownRefs{ASNs: map[string]da.AsnStatus{"ASN-1": da.AsnRegistered}}, da.ErrUnknownAsn},
		{"one cancelled", in, da.KnownRefs{ASNs: map[string]da.AsnStatus{"ASN-1": da.AsnRegistered, "ASN-2": da.AsnCancelledStatus}}, da.ErrUnknownAsn},
		{"empty evidence", in, da.KnownRefs{}, da.ErrUnknownAsn},
		{"outbound known cpt", mustBook(t, withShipments(nil)), da.KnownRefs{CPTs: map[string]bool{"WH1-1500": true}}, nil},
		{"outbound unknown cpt no shipments", mustBook(t, withShipments(nil)), da.KnownRefs{}, da.ErrMissingOutboundRef},
		{"outbound shipments only", mustBook(t, withCPT("", []string{"S1"})), da.KnownRefs{}, nil},
		{"outbound unknown cpt with shipments", mustBook(t, withShipments([]string{"S1"})), da.KnownRefs{}, nil},
		{"outbound no cpt unknown", mustBook(t, withCPT("", []string{"S1"})), da.KnownRefs{CPTs: map[string]bool{"WH1-1500": true}}, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := r.Check(tc.a, tc.k); !errors.Is(err, tc.want) {
				t.Fatalf("Check = %v; want %v", err, tc.want)
			}
		})
	}
}

func withShipments(s []string) da.BookParams {
	p := outbound()
	p.ShipmentRefs = s
	return p
}

func withCPT(cpt string, s []string) da.BookParams {
	p := outbound()
	p.CPTID, p.ShipmentRefs = cpt, s
	return p
}
