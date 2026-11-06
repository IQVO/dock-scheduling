// Package dockcalendar holds the DockCalendar aggregate: the per-site door
// calendar settings every booking is checked against, i.e. door operating
// hours, door closures and the booking settings (ADR 0002). It is created
// lazily with the defaults of D-1; a site that never declared anything is open
// 24x7. Pure Go, no I/O.
package dockcalendar

import (
	"errors"
	"regexp"
	"time"

	"github.com/claudioed/dock-scheduling/internal/domain/shared"
)

var (
	// ErrInvalidOperatingHours is returned when an operating-hours row is malformed:
	// opens_at not before closes_at, an unknown weekday or time zone, or a duplicate (door, weekday).
	ErrInvalidOperatingHours = errors.New("operating hours need a weekday, a time zone and opens_at before closes_at, one row per door and weekday")
	// ErrInvalidSettings is returned when a booking setting is out of range.
	ErrInvalidSettings = errors.New("calendar settings out of range")
	// ErrInvalidClosure is returned for a malformed closure (id, span or reason).
	ErrInvalidClosure = errors.New("closure needs an id, a door, a span ending after it starts and a reason")
	// ErrOverlappingClosure is returned when a closure overlaps another closure of the same door.
	ErrOverlappingClosure = errors.New("door already has a closure in an overlapping span")
	// ErrClosureConflictsWithAppointments is returned when a closure would cover active appointments.
	ErrClosureConflictsWithAppointments = errors.New("closure covers active appointments")
	// ErrHoursConflictWithAppointments is returned when new operating hours would strand active appointments.
	ErrHoursConflictWithAppointments = errors.New("operating hours would leave active appointments outside them")
	// ErrDoorUnavailable is returned when a window is outside the door's operating hours or inside a closure.
	ErrDoorUnavailable = errors.New("door is closed in this window")
	// ErrUnknownClosure is returned when a closure id is not in the calendar.
	ErrUnknownClosure = errors.New("closure not found")
	// ErrClosureAlreadyLifted is returned when lifting a lifted closure.
	ErrClosureAlreadyLifted = errors.New("closure is already lifted")
	// ErrInvalidSnapshot is returned when a persisted calendar breaks an invariant.
	ErrInvalidSnapshot = errors.New("calendar snapshot is inconsistent")
)

// ConflictError carries the appointment ids that made a refusal. It matches
// the sentinel it wraps with errors.Is.
type ConflictError struct {
	Sentinel       error
	AppointmentIDs []string
}

// Error implements error, listing the ids.
func (e *ConflictError) Error() string {
	s := e.Sentinel.Error() + ":"
	for _, id := range e.AppointmentIDs {
		s += " " + id
	}
	return s
}

// Unwrap returns the sentinel.
func (e *ConflictError) Unwrap() error { return e.Sentinel }

// Settings are the booking settings of a site.
type Settings struct {
	MaxWindowMinutes        int
	SlotGranularityMinutes  int
	ArrivalToleranceMinutes int
	NoShowGraceMinutes      int
}

// DefaultSettings are the defaults of D8: max window 240, granularity 15,
// arrival tolerance 15, no-show grace 60.
func DefaultSettings() Settings {
	return Settings{MaxWindowMinutes: 240, SlotGranularityMinutes: 15, ArrivalToleranceMinutes: 15, NoShowGraceMinutes: 60}
}

// Validate returns ErrInvalidSettings unless every setting is in range:
// max window and granularity 1..240, tolerance 0..240, grace 0..1440.
func (s Settings) Validate() error {
	if s.MaxWindowMinutes < 1 || s.MaxWindowMinutes > 240 || s.SlotGranularityMinutes < 1 || s.SlotGranularityMinutes > 240 ||
		s.ArrivalToleranceMinutes < 0 || s.ArrivalToleranceMinutes > 240 || s.NoShowGraceMinutes < 0 || s.NoShowGraceMinutes > 1440 {
		return ErrInvalidSettings
	}
	return nil
}

// MaxWindow returns the maximum window as a duration.
func (s Settings) MaxWindow() time.Duration { return time.Duration(s.MaxWindowMinutes) * time.Minute }

// Granularity returns the slot granularity as a duration.
func (s Settings) Granularity() time.Duration {
	return time.Duration(s.SlotGranularityMinutes) * time.Minute
}

// ArrivalTolerance returns the arrival tolerance as a duration.
func (s Settings) ArrivalTolerance() time.Duration {
	return time.Duration(s.ArrivalToleranceMinutes) * time.Minute
}

// NoShowGrace returns the no-show grace as a duration.
func (s Settings) NoShowGrace() time.Duration {
	return time.Duration(s.NoShowGraceMinutes) * time.Minute
}

// Booking is an active appointment as the calendar sees it: the id, the door
// and the half-open window.
type Booking struct {
	AppointmentID string
	Door          shared.DoorCode
	Start         time.Time
	End           time.Time
}

// Span is a half-open interval with an optional reference (appointment or
// closure id) and a kind.
type Span struct {
	Start time.Time
	End   time.Time
	Kind  string
	Ref   string
}

// The kinds of busy span.
const (
	KindAppointment = "APPOINTMENT"
	KindClosure     = "CLOSURE"
	KindOutOfHours  = "OUT_OF_HOURS"
)

var closureIDPattern = regexp.MustCompile(`^clo-[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// overlaps reports whether [s1,e1) and [s2,e2) share any instant.
func overlaps(s1, e1, s2, e2 time.Time) bool { return s1.Before(e2) && s2.Before(e1) }
