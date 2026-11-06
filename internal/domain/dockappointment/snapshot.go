package dockappointment

import (
	"time"

	"github.com/claudioed/dock-scheduling/internal/domain/shared"
)

// Snapshot is the persisted form of an appointment. Restore validates it, so
// a corrupt row can never become a live aggregate.
type Snapshot struct {
	ID                 string
	SiteID             string
	DoorCode           string
	Direction          string
	Carrier            string
	CarrierSCAC        string
	WindowStart        time.Time
	WindowEnd          time.Time
	ASNNumbers         []string
	CPTID              string
	ShipmentRefs       []string
	ExpectedSeal       string
	Provenance         Provenance
	DoorOverride       bool
	State              State
	Version            int64
	Eta                *Eta
	Arrival            *Arrival
	AtRisk             AtRiskCause
	CompletedAt        time.Time
	CancellationReason Reason
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// Snapshot returns the persisted form of the appointment.
func (a *DockAppointment) Snapshot() Snapshot {
	return Snapshot{
		ID: string(a.id), SiteID: string(a.site), DoorCode: string(a.door), Direction: string(a.direction), Carrier: a.carrier.Name(),
		CarrierSCAC: a.carrier.SCAC(), WindowStart: a.window.start, WindowEnd: a.window.end, ASNNumbers: a.ASNNumbers(), CPTID: a.cptID,
		ShipmentRefs: a.ShipmentRefs(), ExpectedSeal: a.seal, Provenance: a.provenance, DoorOverride: a.override, State: a.state, Version: a.version, Eta: a.Eta(),
		Arrival: a.Arrival(), AtRisk: a.atRisk, CompletedAt: a.completed, CancellationReason: a.reason, CreatedAt: a.createdAt, UpdatedAt: a.updatedAt,
	}
}

// Restore rebuilds a persisted appointment, re-validating every invariant a
// stored row can show except that the window may be in the past. The restored
// appointment has no pending events.
func Restore(s Snapshot) (*DockAppointment, error) {
	if !s.State.valid() {
		return nil, ErrInvalidState
	}
	if s.Version < 1 {
		return nil, ErrInvalidVersion
	}
	if s.Provenance != Native && s.Provenance != LegacyImport {
		return nil, ErrInvalidSnapshot
	}
	a, err := buildParts(s.ID, s.SiteID, s.DoorCode, s.Direction, s.Carrier, s.CarrierSCAC)
	if err != nil {
		return nil, err
	}
	if a.window, err = newBareWindow(s.WindowStart, s.WindowEnd); err != nil {
		return nil, err
	}
	if err = restoreRefs(a, s); err != nil {
		return nil, err
	}
	if err = restoreState(a, s); err != nil {
		return nil, err
	}
	a.provenance, a.state, a.version, a.override = s.Provenance, s.State, s.Version, s.DoorOverride
	a.seal, a.createdAt, a.updatedAt = s.ExpectedSeal, s.CreatedAt.UTC(), s.UpdatedAt.UTC()
	if s.Eta != nil {
		e := *s.Eta
		a.eta = &e
	}
	return a, nil
}

func restoreRefs(a *DockAppointment, s Snapshot) error {
	if a.direction == shared.Inbound {
		if s.CPTID != "" || len(s.ShipmentRefs) > 0 {
			return ErrInvalidSnapshot
		}
		var err error
		a.asns, err = cleanAsns(s.ASNNumbers)
		return err
	}
	return a.setRefs(s.ASNNumbers, s.CPTID, s.ShipmentRefs)
}

func restoreState(a *DockAppointment, s Snapshot) error {
	if s.AtRisk != "" && s.AtRisk != LateEta && s.AtRisk != DoorReassignedOverlap {
		return ErrInvalidSnapshot
	}
	if s.AtRisk != "" && s.State != Booked {
		return ErrInvalidSnapshot
	}
	if (s.State == Cancelled) != (s.CancellationReason != "") {
		return ErrInvalidSnapshot
	}
	if s.State == Cancelled {
		if _, err := ParseReason(string(s.CancellationReason)); err != nil {
			return err
		}
	}
	if (s.State == Completed) != !s.CompletedAt.IsZero() {
		return ErrInvalidSnapshot
	}
	if (s.State == CheckedIn || s.State == Completed) && s.Arrival == nil {
		return ErrInvalidSnapshot
	}
	if s.Arrival != nil {
		v := *s.Arrival
		a.arrival = &v
	}
	a.atRisk, a.reason, a.completed = s.AtRisk, s.CancellationReason, s.CompletedAt.UTC()
	return nil
}
