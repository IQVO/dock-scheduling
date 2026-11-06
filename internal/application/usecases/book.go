// Package usecases holds the application's use cases: one method per command,
// query and event policy of the model, each running in a single UnitOfWork so
// an aggregate, its outbox rows and (for consumers) the processed-event claim
// commit or roll back together. The package depends on the domain and on the
// ports only.
package usecases

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/claudioed/dock-scheduling/internal/application/ports"
	"github.com/claudioed/dock-scheduling/internal/application/views"
	"github.com/claudioed/dock-scheduling/internal/domain/dockappointment"
	"github.com/claudioed/dock-scheduling/internal/domain/dockcalendar"
	"github.com/claudioed/dock-scheduling/internal/domain/shared"
)

// Deps are the ports the use cases drive.
type Deps struct {
	Appointments ports.AppointmentRepo
	Calendars    ports.CalendarRepo
	Doors        ports.DoorView
	Asns         ports.AsnView
	Cutoffs      ports.CutoffView
	Processed    ports.ProcessedEvents
	Events       ports.EventPublisher
	Metrics      ports.Metrics
	IDs          ports.IDGen
	Clock        ports.Clock
	UoW          ports.UnitOfWork
}

// Service is the set of use cases of the dock-scheduling context.
type Service struct {
	d Deps
}

// New builds the use cases over deps.
func New(d Deps) *Service { return &Service{d: d} }

// rulesOf converts the calendar settings to the rules a window is checked against.
func rulesOf(s dockcalendar.Settings) dockappointment.Rules {
	return dockappointment.Rules{
		MaxWindow: s.MaxWindow(), Granularity: s.Granularity(), ArrivalTolerance: s.ArrivalTolerance(), NoShowGrace: s.NoShowGrace(),
	}
}

// BookCommand is BookAppointment.
type BookCommand struct {
	DoorCode           string
	Direction          string
	Carrier            string
	CarrierSCAC        string
	WindowStart        time.Time
	WindowEnd          time.Time
	ASNNumbers         []string
	CPTID              string
	ShipmentRefs       []string
	ExpectedSealNumber string
}

// BookAppointment books a door window (BookAppointment). The door and the
// calendar are checked first, then the references, then the no-overlap rule
// under the per-door advisory lock; the aggregate and DockAppointmentBooked
// commit together.
func (s *Service) BookAppointment(ctx context.Context, cmd BookCommand) (views.AppointmentView, error) {
	var out views.AppointmentView
	err := s.d.UoW.Do(ctx, func(ctx context.Context) error {
		doorCode, err := shared.NewDoorCode(cmd.DoorCode)
		if err != nil {
			return err
		}
		door, cal, err := s.resolveDoor(ctx, doorCode)
		if err != nil {
			return err
		}
		now := s.d.Clock.Now()
		a, events, err := dockappointment.Book(dockappointment.BookParams{
			ID: s.d.IDs.NewAppointmentID(), SiteID: door.Site.String(), DoorCode: cmd.DoorCode, Direction: cmd.Direction,
			Carrier: cmd.Carrier, CarrierSCAC: cmd.CarrierSCAC, WindowStart: cmd.WindowStart, WindowEnd: cmd.WindowEnd,
			ASNNumbers: cmd.ASNNumbers, CPTID: cmd.CPTID, ShipmentRefs: cmd.ShipmentRefs, ExpectedSeal: cmd.ExpectedSealNumber,
			Rules: rulesOf(cal.Settings()), Now: now,
		})
		if err != nil {
			return err
		}
		if err := s.checkNew(ctx, a, door, cal, ""); err != nil {
			return err
		}
		if err := s.d.Appointments.Save(ctx, a, 0); err != nil {
			return err
		}
		if err := s.d.Events.PublishAppointmentEvents(ctx, events...); err != nil {
			return err
		}
		out = toView(a)
		return nil
	})
	return out, err
}

// resolveDoor takes the per-door advisory lock FIRST (so a closure committed
// by the previous holder is visible to the calendar read that follows), then
// loads the bookable door and its site's calendar.
func (s *Service) resolveDoor(ctx context.Context, code shared.DoorCode) (*dockappointment.Door, *dockcalendar.DockCalendar, error) {
	if err := s.d.Appointments.LockDoor(ctx, code); err != nil {
		return nil, nil, err
	}
	door, err := s.d.Doors.Door(ctx, code)
	if err != nil {
		return nil, nil, err
	}
	if door == nil {
		return nil, nil, dockappointment.ErrUnknownDoor
	}
	cal, err := s.d.Calendars.Get(ctx, door.Site)
	if err != nil {
		return nil, nil, err
	}
	return door, cal, nil
}

// checkNew runs the cross-aggregate rules of a booked or rescheduled
// appointment: the door's flow, the calendar, the known references, the ASN
// uniqueness and the no-overlap rule (the door lock is already held, see resolveDoor). excluding is the id
// of the appointment being rescheduled ("" when booking).
func (s *Service) checkNew(ctx context.Context, a *dockappointment.DockAppointment, door *dockappointment.Door, cal *dockcalendar.DockCalendar, excluding string) error {
	var schedule dockappointment.DoorSchedule
	if err := schedule.CheckDoor(door, a.Direction()); err != nil {
		return err
	}
	w := a.Window()
	if err := cal.CheckWindow(a.DoorCode(), w.Start(), w.End()); err != nil {
		return err
	}
	if excluding == "" {
		if err := s.checkReferences(ctx, a); err != nil {
			return err
		}
	}
	active, err := s.d.Appointments.ListActiveOnDoor(ctx, a.DoorCode())
	if err != nil {
		return err
	}
	return schedule.CheckNoOverlap(a, active)
}

// checkReferences checks the ASNs and the cpt id against the local copies and
// that no ASN sits on another active appointment (D-12).
func (s *Service) checkReferences(ctx context.Context, a *dockappointment.DockAppointment) error {
	known := dockappointment.KnownRefs{}
	if a.Direction() == shared.Inbound {
		statuses, err := s.d.Asns.Statuses(ctx, a.ASNNumbers())
		if err != nil {
			return err
		}
		known.ASNs = statuses
	} else if a.CPTID() != "" {
		cpts, err := s.d.Cutoffs.Known(ctx, a.SiteID(), []string{a.CPTID()})
		if err != nil {
			return err
		}
		known.CPTs = cpts
	}
	var refs dockappointment.References
	if err := refs.Check(a, known); err != nil {
		return err
	}
	if a.Direction() != shared.Inbound {
		return nil
	}
	taken, err := s.d.Appointments.ActiveIDsByASN(ctx, a.ASNNumbers(), a.ID().String())
	if err != nil {
		return err
	}
	if len(taken) > 0 {
		return fmt.Errorf("%w: on %s", dockappointment.ErrAsnAlreadyScheduled, strings.Join(taken, ", "))
	}
	return nil
}

// RescheduleCommand is RescheduleAppointment; an empty DoorCode keeps the door.
type RescheduleCommand struct {
	AppointmentID   string
	DoorCode        string
	WindowStart     time.Time
	WindowEnd       time.Time
	ExpectedVersion int64
}

// RescheduleAppointment moves a BOOKED appointment (D-4: window and/or door);
// the same checks as a booking apply to the new door and window.
func (s *Service) RescheduleAppointment(ctx context.Context, cmd RescheduleCommand) (views.AppointmentView, error) {
	var out views.AppointmentView
	err := s.d.UoW.Do(ctx, func(ctx context.Context) error {
		a, err := s.loadForCommand(ctx, cmd.AppointmentID, cmd.ExpectedVersion)
		if err != nil {
			return err
		}
		loaded := a.Version()
		doorCode := cmd.DoorCode
		if doorCode == "" {
			doorCode = a.DoorCode().String()
		}
		code, err := shared.NewDoorCode(doorCode)
		if err != nil {
			return err
		}
		door, cal, err := s.resolveDoor(ctx, code)
		if err != nil {
			return err
		}
		if door.Site != a.SiteID() {
			return dockappointment.ErrUnknownDoor
		}
		events, err := a.Reschedule(cmd.DoorCode, cmd.WindowStart, cmd.WindowEnd, rulesOf(cal.Settings()), s.d.Clock.Now())
		if err != nil {
			return err
		}
		if err := s.checkNew(ctx, a, door, cal, a.ID().String()); err != nil {
			return err
		}
		if err := s.d.Appointments.Save(ctx, a, loaded); err != nil {
			return err
		}
		if err := s.d.Events.PublishAppointmentEvents(ctx, events...); err != nil {
			return err
		}
		out = toView(a)
		return nil
	})
	return out, err
}

// ErrPreconditionFailed is returned when an If-Match version is not the
// current version of the appointment; the HTTP adapter maps it to 412.
var ErrPreconditionFailed = errors.New("the appointment version differs from If-Match")

// loadForCommand loads the appointment under a row lock and checks the
// caller's expected version (0 = no precondition).
func (s *Service) loadForCommand(ctx context.Context, id string, expected int64) (*dockappointment.DockAppointment, error) {
	if _, err := dockappointment.NewID(id); err != nil {
		return nil, err
	}
	a, err := s.d.Appointments.GetForUpdate(ctx, id)
	if err != nil {
		return nil, err
	}
	if expected != 0 && a.Version() != expected {
		return nil, fmt.Errorf("%w: now at version %d", ErrPreconditionFailed, a.Version())
	}
	return a, nil
}
