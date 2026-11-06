package dockappointment

import "github.com/claudioed/dock-scheduling/internal/domain/shared"

// Door is what the DoorView knows about a bookable door: a Dock slot of a
// site with the dock flow facility-layout registered for it.
type Door struct {
	Code shared.DoorCode
	Site shared.SiteID
	Flow shared.DockFlow
}

// DoorSchedule is the domain service guarding the cross-aggregate door rules.
type DoorSchedule struct{}

// CheckDoor returns ErrUnknownDoor when door is nil (not a known, active Dock
// slot with a resolved site) and ErrDoorFlowMismatch when the door's dock flow
// does not allow the direction.
func (DoorSchedule) CheckDoor(door *Door, direction shared.Direction) error {
	if door == nil {
		return ErrUnknownDoor
	}
	if !door.Flow.Allows(direction) {
		return ErrDoorFlowMismatch
	}
	return nil
}

// CheckNoOverlap returns ErrDoorDoubleBooked when candidate's window overlaps
// the window of any other active appointment on its door. activeOnSameDoor is
// what the repository found for the candidate's door; the candidate itself,
// appointments on another door and appointments that no longer occupy a door
// are ignored defensively. Windows are half-open: [10:00,12:00) and
// [12:00,14:00) are compatible. Inbound and outbound share one calendar.
func (DoorSchedule) CheckNoOverlap(candidate *DockAppointment, activeOnSameDoor []*DockAppointment) error {
	for _, other := range activeOnSameDoor {
		if other.id == candidate.id || other.door != candidate.door || !other.state.Active() {
			continue
		}
		if candidate.window.Overlaps(other.window) {
			return ErrDoorDoubleBooked
		}
	}
	return nil
}

// AsnStatus is what the local ASN copy knows about an ASN number.
type AsnStatus int

// The ASN copy states; the zero value is "never seen".
const (
	AsnUnknown AsnStatus = iota
	AsnRegistered
	AsnCancelledStatus
)

// KnownRefs is the local-copy evidence References.Check needs, filled by the
// application for the numbers and the cpt id the appointment names.
type KnownRefs struct {
	ASNs map[string]AsnStatus
	CPTs map[string]bool
}

// References is the domain service checking that what an appointment covers
// exists in the local copies.
type References struct{}

// Check returns ErrUnknownAsn when an INBOUND appointment covers an ASN that is
// unknown or cancelled in the ASN copy, and ErrMissingOutboundRef when an
// OUTBOUND appointment has no shipment reference and its cpt id is not in the
// CPT copy.
func (References) Check(a *DockAppointment, known KnownRefs) error {
	if a.direction == shared.Inbound {
		for _, n := range a.asns {
			if known.ASNs[n] != AsnRegistered {
				return ErrUnknownAsn
			}
		}
		return nil
	}
	if len(a.shipments) > 0 || (a.cptID != "" && known.CPTs[a.cptID]) {
		return nil
	}
	return ErrMissingOutboundRef
}
