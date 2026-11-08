package usecases

import (
	"context"
	"time"

	"github.com/claudioed/dock-scheduling/internal/application/views"
	"github.com/claudioed/dock-scheduling/internal/domain/dockappointment"
	"github.com/claudioed/dock-scheduling/internal/domain/dockcalendar"
	"github.com/claudioed/dock-scheduling/internal/domain/shared"
)

// HoursInput is one operating-hours row of DeclareOperatingHours.
type HoursInput struct {
	DoorCode string
	Weekday  string
	OpensAt  string
	ClosesAt string
	Timezone string
}

// DeclareOperatingHours replaces the whole set of operating-hours rows of a
// site (DeclareOperatingHours). It is refused when an active appointment would
// fall outside the new hours (D-8).
func (s *Service) DeclareOperatingHours(ctx context.Context, siteID string, rows []HoursInput) (views.DockCalendarView, error) {
	var out views.DockCalendarView
	err := s.d.UoW.Do(ctx, func(ctx context.Context) error {
		site, err := shared.NewSiteID(siteID)
		if err != nil {
			return err
		}
		domainRows := make([]dockcalendar.OperatingHours, 0, len(rows))
		for _, r := range rows {
			row, err := dockcalendar.NewOperatingHours(r.DoorCode, dockcalendar.Weekday(r.Weekday), r.OpensAt, r.ClosesAt, r.Timezone)
			if err != nil {
				return err
			}
			domainRows = append(domainRows, row)
		}
		cal, err := s.d.Calendars.GetForUpdate(ctx, site)
		if err != nil {
			return err
		}
		loaded := cal.Version()
		active, err := s.d.Appointments.ActiveBookings(ctx, site, "")
		if err != nil {
			return err
		}
		events, err := cal.DeclareOperatingHours(domainRows, active, s.d.Clock.Now())
		if err != nil {
			return err
		}
		out = toCalendarView(cal)
		return s.saveCalendar(ctx, cal, loaded, events)
	})
	return out, err
}

// DeclareCalendarSettings sets the booking settings of a site (DeclareCalendarSettings).
func (s *Service) DeclareCalendarSettings(ctx context.Context, siteID string, settings views.SettingsView) (views.DockCalendarView, error) {
	var out views.DockCalendarView
	err := s.d.UoW.Do(ctx, func(ctx context.Context) error {
		site, err := shared.NewSiteID(siteID)
		if err != nil {
			return err
		}
		cal, err := s.d.Calendars.GetForUpdate(ctx, site)
		if err != nil {
			return err
		}
		loaded := cal.Version()
		err = cal.DeclareSettings(dockcalendar.Settings{
			MaxWindowMinutes: settings.MaxWindowMinutes, SlotGranularityMinutes: settings.SlotGranularityMinutes,
			ArrivalToleranceMinutes: settings.ArrivalToleranceMinutes, NoShowGraceMinutes: settings.NoShowGraceMinutes,
		})
		if err != nil {
			return err
		}
		out = toCalendarView(cal)
		return s.saveCalendar(ctx, cal, loaded, nil)
	})
	return out, err
}

// ClosureCommand is ScheduleDoorClosure.
type ClosureCommand struct {
	SiteID   string
	DoorCode string
	From     time.Time
	To       time.Time
	Reason   string
}

// ScheduleDoorClosure closes a door of the site for [From, To)
// (ScheduleDoorClosure). Refused when it overlaps another closure of the door
// or covers an active appointment (the refusal lists the ids).
func (s *Service) ScheduleDoorClosure(ctx context.Context, cmd ClosureCommand) (views.ClosureView, error) {
	var out views.ClosureView
	err := s.d.UoW.Do(ctx, func(ctx context.Context) error {
		site, err := shared.NewSiteID(cmd.SiteID)
		if err != nil {
			return err
		}
		door, err := shared.NewDoorCode(cmd.DoorCode)
		if err != nil {
			return err
		}
		known, err := s.d.Doors.Door(ctx, door)
		if err != nil {
			return err
		}
		if known == nil || known.Site != site {
			return dockappointment.ErrUnknownDoor
		}
		cal, err := s.d.Calendars.GetForUpdate(ctx, site)
		if err != nil {
			return err
		}
		loaded := cal.Version()
		if err := s.d.Appointments.LockDoor(ctx, door); err != nil {
			return err
		}
		active, err := s.d.Appointments.ActiveBookings(ctx, site, door)
		if err != nil {
			return err
		}
		k, events, err := cal.ScheduleClosure(s.d.IDs.NewClosureID(), cmd.DoorCode, cmd.From, cmd.To, cmd.Reason, active, s.d.Clock.Now())
		if err != nil {
			return err
		}
		out = toClosureView(site.String(), k)
		return s.saveCalendar(ctx, cal, loaded, events)
	})
	return out, err
}

// LiftDoorClosure lifts a scheduled closure (LiftDoorClosure).
func (s *Service) LiftDoorClosure(ctx context.Context, siteID, closureID string) (views.ClosureView, error) {
	var out views.ClosureView
	err := s.d.UoW.Do(ctx, func(ctx context.Context) error {
		site, err := shared.NewSiteID(siteID)
		if err != nil {
			return err
		}
		cal, err := s.d.Calendars.GetForUpdate(ctx, site)
		if err != nil {
			return err
		}
		loaded := cal.Version()
		k, events, err := cal.LiftClosure(closureID, s.d.Clock.Now())
		if err != nil {
			return err
		}
		out = toClosureView(site.String(), k)
		return s.saveCalendar(ctx, cal, loaded, events)
	})
	return out, err
}

func (s *Service) saveCalendar(ctx context.Context, cal *dockcalendar.DockCalendar, loaded int64, events []dockcalendar.Event) error {
	if err := s.d.Calendars.Save(ctx, cal, loaded); err != nil {
		return err
	}
	return s.d.Events.PublishCalendarEvents(ctx, events...)
}
