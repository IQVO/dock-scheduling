package usecases

import (
	"context"
	"errors"
	"time"

	"github.com/claudioed/dock-scheduling/internal/application/views"
	"github.com/claudioed/dock-scheduling/internal/domain/dockappointment"
)

// CancelCommand is CancelAppointment.
type CancelCommand struct {
	AppointmentID   string
	Reason          string
	ExpectedVersion int64
}

// CancelAppointment cancels a BOOKED or CHECKED_IN appointment, freeing its
// door window. Over REST only CARRIER_REQUEST and SITE_REQUEST are accepted
// (the HTTP adapter narrows the set); the policies use the others.
func (s *Service) CancelAppointment(ctx context.Context, cmd CancelCommand) (views.AppointmentView, error) {
	var out views.AppointmentView
	err := s.d.UoW.Do(ctx, func(ctx context.Context) error {
		reason, err := dockappointment.ParseReason(cmd.Reason)
		if err != nil {
			return err
		}
		a, err := s.loadForCommand(ctx, cmd.AppointmentID, cmd.ExpectedVersion)
		if err != nil {
			return err
		}
		loaded := a.Version()
		events, err := a.Cancel(reason, s.d.Clock.Now())
		if err != nil {
			return err
		}
		out = toView(a)
		return s.saveAndPublish(ctx, a, loaded, events)
	})
	return out, err
}

// saveAndPublish persists a changed appointment guarded by the version the
// caller loaded and enqueues its events.
func (s *Service) saveAndPublish(ctx context.Context, a *dockappointment.DockAppointment, loaded int64, events []dockappointment.Event) error {
	if err := s.d.Appointments.Save(ctx, a, loaded); err != nil {
		return err
	}
	return s.d.Events.PublishAppointmentEvents(ctx, events...)
}

// EtaCommand is ReportEta; an empty Source is recorded as "api".
type EtaCommand struct {
	AppointmentID string
	EtaAt         time.Time
	Source        string
}

// ReportEta records the carrier's latest ETA on a BOOKED appointment. A late
// ETA raises the advisory DockAppointmentAtRisk once for this report.
func (s *Service) ReportEta(ctx context.Context, cmd EtaCommand) (views.EtaResult, error) {
	var out views.EtaResult
	err := s.d.UoW.Do(ctx, func(ctx context.Context) error {
		a, err := s.loadForCommand(ctx, cmd.AppointmentID, 0)
		if err != nil {
			return err
		}
		loaded := a.Version()
		cal, err := s.d.Calendars.Get(ctx, a.SiteID())
		if err != nil {
			return err
		}
		source := cmd.Source
		if source == "" {
			source = "api"
		}
		events, err := a.ReportEta(cmd.EtaAt, source, rulesOf(cal.Settings()), s.d.Clock.Now())
		if err != nil {
			return err
		}
		out = views.EtaResult{Appointment: toView(a), AtRisk: len(events) > 0}
		return s.saveAndPublish(ctx, a, loaded, events)
	})
	return out, err
}

// RunNoShowSweep marks every BOOKED appointment whose window end plus its
// site's no-show grace has passed as NO_SHOW (NoShowSweep). Each appointment
// commits in its own transaction, so a failure on one never blocks the rest;
// the first failure is returned after the pass. It is naturally idempotent.
func (s *Service) RunNoShowSweep(ctx context.Context) (views.SweepResult, error) {
	now := s.d.Clock.Now()
	ids, err := s.d.Appointments.ListBookedEndedBy(ctx, now, sweepBatch)
	if err != nil {
		return views.SweepResult{}, err
	}
	res := views.SweepResult{AppointmentIDs: []string{}, RanAt: now}
	var firstErr error
	for _, id := range ids {
		marked, err := s.markNoShow(ctx, id)
		if err != nil && firstErr == nil {
			firstErr = err
		}
		if marked {
			res.AppointmentIDs = append(res.AppointmentIDs, id)
		}
	}
	res.Count = len(res.AppointmentIDs)
	return res, firstErr
}

// sweepBatch bounds how many candidates one sweep pass loads.
const sweepBatch = 500

func (s *Service) markNoShow(ctx context.Context, id string) (bool, error) {
	marked := false
	err := s.d.UoW.Do(ctx, func(ctx context.Context) error {
		a, err := s.d.Appointments.GetForUpdate(ctx, id)
		if err != nil {
			return err
		}
		loaded := a.Version()
		cal, err := s.d.Calendars.Get(ctx, a.SiteID())
		if err != nil {
			return err
		}
		events, err := a.MarkNoShow(rulesOf(cal.Settings()), s.d.Clock.Now())
		if errors.Is(err, dockappointment.ErrNoShowTooEarly) || errors.Is(err, dockappointment.ErrIllegalTransition) {
			return nil // grace not over, or the appointment moved on since the listing: nothing to do
		}
		if err != nil {
			return err
		}
		marked = true
		return s.saveAndPublish(ctx, a, loaded, events)
	})
	return marked && err == nil, err
}
