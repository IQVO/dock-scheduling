package usecases

import (
	"context"
	"time"

	"github.com/claudioed/dock-scheduling/internal/domain/dockappointment"
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
