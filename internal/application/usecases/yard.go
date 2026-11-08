package usecases

import (
	"context"
	"time"

	"github.com/claudioed/dock-scheduling/internal/domain/dockappointment"
	"github.com/claudioed/dock-scheduling/internal/domain/shared"
)

// ConsumeTrailerDocked is ActualDoorWins: when the trailer docked at another
// door than the booked one, ReassignDoor WITHOUT the overlap refusal (the
// physical fact wins), then every other BOOKED appointment of the new door
// whose window now overlaps gets DockAppointmentAtRisk (cause
// DOOR_REASSIGNED_OVERLAP).
func (s *Service) ConsumeTrailerDocked(ctx context.Context, eventID, siteID, appointmentID, doorCode string, dockedAt time.Time) error {
	return s.yardFact(ctx, eventID, appointmentID, siteID, func(ctx context.Context, a *dockappointment.DockAppointment) ([]dockappointment.Event, error) {
		events, err := a.ReassignDoor(doorCode, s.d.Clock.Now())
		if err != nil || len(events) == 0 {
			return nil, err
		}
		return events, s.flagOverlaps(ctx, a)
	})
}

// flagOverlaps marks the other BOOKED appointments of a's (new) door that now
// overlap a's window as at risk.
func (s *Service) flagOverlaps(ctx context.Context, a *dockappointment.DockAppointment) error {
	if err := s.d.Appointments.LockDoor(ctx, a.DoorCode()); err != nil {
		return err
	}
	others, err := s.d.Appointments.ListActiveOnDoor(ctx, a.DoorCode())
	if err != nil {
		return err
	}
	for _, o := range others {
		if o.ID() == a.ID() || o.State() != dockappointment.Booked || !o.Window().Overlaps(a.Window()) {
			continue
		}
		if err := s.markAtRisk(ctx, o.ID().String()); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) markAtRisk(ctx context.Context, id string) error {
	o, err := s.d.Appointments.GetForUpdate(ctx, id)
	if err != nil {
		return err
	}
	loaded := o.Version()
	events, err := o.MarkAtRisk(dockappointment.DoorReassignedOverlap, s.d.Clock.Now())
	if err != nil {
		return err
	}
	return s.saveAndPublish(ctx, o, loaded, events)
}

// LegacyBooking is a legacy inbound-receiving DockAppointmentBooked.
type LegacyBooking struct {
	AppointmentID string
	DoorCode      string
	Carrier       string
	WindowStart   time.Time
	WindowEnd     time.Time
	ASNNumbers    []string
}

// ConsumeLegacyBooked is LegacyImport (strangler stage A, D-2): it imports the
// appointment once, enforcing none of the booking rules. A door whose site is
// unknown skips the event (counted); a repeat of the same appointment id is a
// no-op.
func (s *Service) ConsumeLegacyBooked(ctx context.Context, eventID string, b LegacyBooking) error {
	return s.consume(ctx, eventID, func(ctx context.Context) error {
		code, err := shared.NewDoorCode(b.DoorCode)
		if err != nil {
			return err
		}
		door, err := s.d.Doors.Door(ctx, code)
		if err != nil {
			return err
		}
		if door == nil {
			s.d.Metrics.IgnoredFact(IgnoredUnknownDoor)
			return nil
		}
		if _, err := dockappointment.NewID(b.AppointmentID); err != nil {
			return err
		}
		if _, err := s.d.Appointments.Get(ctx, b.AppointmentID); err == nil {
			s.d.Metrics.IgnoredFact(IgnoredAlreadyApplied)
			return nil
		} else if !isNotFound(err) {
			return err
		}
		rules, err := s.rulesOfSite(ctx, door.Site)
		if err != nil {
			return err
		}
		a, events, err := dockappointment.ImportLegacy(dockappointment.LegacyParams{
			ID: b.AppointmentID, SiteID: door.Site.String(), DoorCode: b.DoorCode, Carrier: b.Carrier,
			WindowStart: b.WindowStart, WindowEnd: b.WindowEnd, ASNNumbers: b.ASNNumbers, Rules: rules, Now: s.d.Clock.Now(),
		})
		if err != nil {
			return err
		}
		return s.saveAndPublish(ctx, a, 0, events)
	})
}

// legacyFact applies a later legacy event to an appointment imported from the
// legacy owner; native appointments are never touched by the importer.
func (s *Service) legacyFact(ctx context.Context, eventID, appointmentID string, apply func(*dockappointment.DockAppointment, dockappointment.Rules) ([]dockappointment.Event, error)) error {
	return s.yardFact(ctx, eventID, appointmentID, "", func(ctx context.Context, a *dockappointment.DockAppointment) ([]dockappointment.Event, error) {
		if a.Provenance() != dockappointment.LegacyImport {
			s.d.Metrics.IgnoredFact(IgnoredNotLegacy)
			return nil, nil
		}
		rules, err := s.rulesOfSite(ctx, a.SiteID())
		if err != nil {
			return nil, err
		}
		return apply(a, rules)
	})
}

// ConsumeLegacyCheckedIn moves an imported appointment to CHECKED_IN.
func (s *Service) ConsumeLegacyCheckedIn(ctx context.Context, eventID, appointmentID string, at time.Time) error {
	return s.legacyFact(ctx, eventID, appointmentID, func(a *dockappointment.DockAppointment, r dockappointment.Rules) ([]dockappointment.Event, error) {
		return a.RecordArrival(at, "", r, s.d.Clock.Now())
	})
}

// ConsumeLegacyCancelled moves an imported appointment to CANCELLED (CARRIER_REQUEST).
func (s *Service) ConsumeLegacyCancelled(ctx context.Context, eventID, appointmentID string) error {
	return s.legacyFact(ctx, eventID, appointmentID, func(a *dockappointment.DockAppointment, _ dockappointment.Rules) ([]dockappointment.Event, error) {
		return a.Cancel(dockappointment.CarrierRequest, s.d.Clock.Now())
	})
}

// ConsumeLegacyCompleted moves an imported appointment to COMPLETED; a missing
// completion time falls back to the clock.
func (s *Service) ConsumeLegacyCompleted(ctx context.Context, eventID, appointmentID string, at time.Time) error {
	return s.legacyFact(ctx, eventID, appointmentID, func(a *dockappointment.DockAppointment, _ dockappointment.Rules) ([]dockappointment.Event, error) {
		now := s.d.Clock.Now()
		if at.IsZero() {
			at = now
		}
		return a.RecordDoorRelease(at, now)
	})
}
