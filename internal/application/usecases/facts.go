package usecases

import (
	"context"
	"errors"
	"time"

	"github.com/claudioed/dock-scheduling/internal/application/ports"
	"github.com/claudioed/dock-scheduling/internal/application/repository"
	"github.com/claudioed/dock-scheduling/internal/domain/dockappointment"
	"github.com/claudioed/dock-scheduling/internal/domain/shared"
)

// Reasons a consumed fact changed nothing; they are the `reason` label of the
// ignored-facts counter.
const (
	IgnoredUnknownAppointment = "unknown_appointment"
	IgnoredTerminal           = "terminal_appointment"
	IgnoredWrongSite          = "site_mismatch"
	IgnoredUnknownDoor        = "unknown_door"
	IgnoredAlreadyApplied     = "already_applied"
)

// consume runs fn in one unit of work together with the claim of the
// CloudEvents id, so a rollback un-claims it and a redelivery is processed. A
// duplicate id is a no-op.
func (s *Service) consume(ctx context.Context, eventID string, fn func(ctx context.Context) error) error {
	return s.d.UoW.Do(ctx, func(ctx context.Context) error {
		first, err := s.d.Processed.Claim(ctx, eventID)
		if err != nil || !first {
			return err
		}
		return fn(ctx)
	})
}

// ConsumeZoneRegistered keeps the zone -> site copy (facility-layout).
func (s *Service) ConsumeZoneRegistered(ctx context.Context, eventID, zoneID, siteCode string) error {
	return s.consume(ctx, eventID, func(ctx context.Context) error {
		return s.d.Doors.UpsertZone(ctx, zoneID, siteCode)
	})
}

// ConsumeSlotRegistered keeps the doors copy: a Dock slot is upserted, any
// other role removes the row (a slot can be re-registered with another role).
func (s *Service) ConsumeSlotRegistered(ctx context.Context, eventID, code, zoneID, role, dockFlow string) error {
	return s.consume(ctx, eventID, func(ctx context.Context) error {
		if role != "Dock" {
			return s.d.Doors.RemoveDoor(ctx, code)
		}
		flow, err := shared.ParseDockFlow(dockFlow)
		if err != nil {
			return err
		}
		return s.d.Doors.UpsertDoor(ctx, code, zoneID, flow)
	})
}

// ConsumeSlotDecommissioned deletes the door: it blocks NEW bookings, existing
// appointments are untouched (D-7).
func (s *Service) ConsumeSlotDecommissioned(ctx context.Context, eventID, code string) error {
	return s.consume(ctx, eventID, func(ctx context.Context) error {
		return s.d.Doors.RemoveDoor(ctx, code)
	})
}

// ConsumeAsnRegistered keeps the ASN copy.
func (s *Service) ConsumeAsnRegistered(ctx context.Context, eventID, number string, expectedArrival time.Time) error {
	return s.consume(ctx, eventID, func(ctx context.Context) error {
		return s.d.Asns.Register(ctx, number, expectedArrival)
	})
}

// ConsumeAsnCancelled marks the ASN cancelled and runs
// CancelWhenAllAsnsCancelled: a BOOKED INBOUND appointment whose ASNs are all
// cancelled is cancelled with reason ASN_CANCELLED.
func (s *Service) ConsumeAsnCancelled(ctx context.Context, eventID, number string) error {
	return s.consume(ctx, eventID, func(ctx context.Context) error {
		if err := s.d.Asns.Cancel(ctx, number); err != nil {
			return err
		}
		covering, err := s.d.Appointments.ActiveByASN(ctx, number)
		if err != nil {
			return err
		}
		for _, a := range covering {
			if err := s.cancelIfAllAsnsCancelled(ctx, a.ID().String()); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Service) cancelIfAllAsnsCancelled(ctx context.Context, id string) error {
	a, err := s.d.Appointments.GetForUpdate(ctx, id)
	if err != nil {
		return err
	}
	if a.State() != dockappointment.Booked {
		return nil
	}
	statuses, err := s.d.Asns.Statuses(ctx, a.ASNNumbers())
	if err != nil {
		return err
	}
	for _, n := range a.ASNNumbers() {
		if statuses[n] != dockappointment.AsnCancelledStatus {
			return nil
		}
	}
	loaded := a.Version()
	events, err := a.Cancel(dockappointment.AsnCancelled, s.d.Clock.Now())
	if err != nil {
		return err
	}
	return s.saveAndPublish(ctx, a, loaded, events)
}

// ConsumeCptSchedule replaces the CPT schedule copy of a site (the event is
// the whole schedule). Phase 2 reads it; v1 creates no appointment from it.
func (s *Service) ConsumeCptSchedule(ctx context.Context, eventID, siteID, timezone string, cutoffs []repository.Cutoff) error {
	return s.consume(ctx, eventID, func(ctx context.Context) error {
		return s.d.Cutoffs.Replace(ctx, siteID, timezone, cutoffs)
	})
}

// yardFact applies a yard fact to the appointment it names: the lookup,
// locking and the ignore-and-count rules of OutOfOrderYardFacts. apply gets
// the locked, active-or-booked appointment; states a fact cannot touch are
// ignored and counted. siteID "" skips the site check.
func (s *Service) yardFact(ctx context.Context, eventID, appointmentID, siteID string, apply func(ctx context.Context, a *dockappointment.DockAppointment) ([]dockappointment.Event, error)) error {
	return s.consume(ctx, eventID, func(ctx context.Context) error {
		if appointmentID == "" {
			return nil // a walk-in or a drop-off: nothing to apply, nothing to count
		}
		if _, err := dockappointment.NewID(appointmentID); err != nil {
			s.d.Metrics.IgnoredFact(IgnoredUnknownAppointment)
			return nil
		}
		a, err := s.d.Appointments.GetForUpdate(ctx, appointmentID)
		if errors.Is(err, ports.ErrNotFound) {
			s.d.Metrics.IgnoredFact(IgnoredUnknownAppointment)
			return nil
		}
		if err != nil {
			return err
		}
		if siteID != "" && a.SiteID().String() != siteID {
			s.d.Metrics.IgnoredFact(IgnoredWrongSite)
			return nil
		}
		loaded := a.Version()
		events, err := apply(ctx, a)
		if errors.Is(err, dockappointment.ErrIllegalTransition) {
			s.ignoreIllegal(a)
			return nil
		}
		if err != nil || len(events) == 0 {
			return err
		}
		return s.saveAndPublish(ctx, a, loaded, events)
	})
}

func (s *Service) ignoreIllegal(a *dockappointment.DockAppointment) {
	if a.State().Terminal() {
		s.d.Metrics.IgnoredFact(IgnoredTerminal)
		return
	}
	s.d.Metrics.IgnoredFact(IgnoredAlreadyApplied)
}

// ConsumeVisitCheckedIn is ArrivalFromGate: RecordArrival on the named
// appointment, the status computed from the window and the site's tolerance.
func (s *Service) ConsumeVisitCheckedIn(ctx context.Context, eventID, visitID, siteID, appointmentID string, arrivedAt time.Time) error {
	return s.yardFact(ctx, eventID, appointmentID, siteID, func(ctx context.Context, a *dockappointment.DockAppointment) ([]dockappointment.Event, error) {
		rules, err := s.rulesOfSite(ctx, a.SiteID())
		if err != nil {
			return nil, err
		}
		return a.RecordArrival(arrivedAt, visitID, rules, s.d.Clock.Now())
	})
}

// ConsumeVisitRejected is CancelOnVisitRejected: a BOOKED appointment is
// cancelled with reason TURNED_AWAY; any other state is ignored and counted.
func (s *Service) ConsumeVisitRejected(ctx context.Context, eventID, siteID, appointmentID string) error {
	return s.yardFact(ctx, eventID, appointmentID, siteID, func(_ context.Context, a *dockappointment.DockAppointment) ([]dockappointment.Event, error) {
		if a.State() != dockappointment.Booked {
			return nil, dockappointment.ErrIllegalTransition
		}
		return a.Cancel(dockappointment.TurnedAway, s.d.Clock.Now())
	})
}

// ConsumeTrailerUndocked is CompleteOnDoorRelease: RecordDoorRelease. A
// release outrunning the gate fact completes a BOOKED appointment with an
// implicit arrival (OutOfOrderYardFacts).
func (s *Service) ConsumeTrailerUndocked(ctx context.Context, eventID, siteID, appointmentID string, undockedAt time.Time) error {
	return s.yardFact(ctx, eventID, appointmentID, siteID, func(_ context.Context, a *dockappointment.DockAppointment) ([]dockappointment.Event, error) {
		implicit := a.State() == dockappointment.Booked
		events, err := a.RecordDoorRelease(undockedAt, s.d.Clock.Now())
		if err == nil && implicit {
			s.d.Metrics.ImplicitArrival()
		}
		return events, err
	})
}

func (s *Service) rulesOfSite(ctx context.Context, site shared.SiteID) (dockappointment.Rules, error) {
	cal, err := s.d.Calendars.Get(ctx, site)
	if err != nil {
		return dockappointment.Rules{}, err
	}
	return rulesOf(cal.Settings()), nil
}
