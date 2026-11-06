// Package adherence is the pure core of the analytics read side (ADR 0006): the
// six analytics events that decide whether an appointment was on time, early,
// late or a no-show, their validation, and the Projection port the Postgres
// store implements. It imports nothing internal and does no I/O; the
// classification itself is declared once, in the analytical schema
// (analytics/migrations, column appointment_facts.outcome), and exercised
// against a real Postgres.
package adherence

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrInvalidEvent marks an event that can never be projected (a required field
// missing, a window that does not close, an unknown arrival status). It is
// deterministic: the consumer dead-letters it, never retries it.
var ErrInvalidEvent = errors.New("adherence: invalid event")

// ErrRejected marks an event the store deterministically refused (a Postgres
// constraint or data exception): the same bytes can never succeed, so it is
// dead-lettered like an invalid one.
var ErrRejected = errors.New("adherence: event rejected by the store")

// Kind is the EventName of one analytics event the projection folds.
type Kind string

// The folded events. Every other event of the topic (door reassignments, ETA
// risk, calendar changes) is acknowledged and ignored: it cannot change the
// adherence of an appointment.
const (
	Booked      Kind = "DockAppointmentBooked"
	Rescheduled Kind = "DockAppointmentRescheduled"
	CheckedIn   Kind = "DockAppointmentCheckedIn"
	Completed   Kind = "DockAppointmentCompleted"
	Cancelled   Kind = "DockAppointmentCancelled"
	NoShow      Kind = "DockAppointmentNoShow"
)

// Kinds lists every folded event.
func Kinds() []Kind { return []Kind{Booked, Rescheduled, CheckedIn, Completed, Cancelled, NoShow} }

// ArrivalStatus is the status a DockAppointmentCheckedIn carries (X6).
type ArrivalStatus string

// The arrival statuses.
const (
	Early  ArrivalStatus = "EARLY"
	OnTime ArrivalStatus = "ON_TIME"
	Late   ArrivalStatus = "LATE"
)

// Valid reports whether s is one of the three statuses.
func (s ArrivalStatus) Valid() bool { return s == Early || s == OnTime || s == Late }

// Event is one analytics event, decoded and mapped from the wire. Fields that
// an event does not carry are left at their zero value.
type Event struct {
	Kind    Kind
	EventID string
	// At is the CloudEvents `time` (the domain instant): the freshness of the
	// projection and the milestone time of Booked, Cancelled and NoShow.
	At            time.Time
	AppointmentID string
	SiteID        string
	// CarrierSCAC is "" when the event names none (legacy imports).
	CarrierSCAC string
	// WindowStart and WindowEnd are the window of Booked and Rescheduled, and
	// the window end of NoShow.
	WindowStart, WindowEnd time.Time
	// Version is the aggregate version the event raised.
	Version       int64
	ArrivalStatus ArrivalStatus
	// CheckedInAt and CompletedAt are the physical instants of their events.
	CheckedInAt, CompletedAt time.Time
	// Reason is the cancellation reason.
	Reason string
}

// Validate reports why the event can never be projected, wrapping
// ErrInvalidEvent, or nil.
func (e Event) Validate() error {
	fail := func(format string, args ...any) error {
		return fmt.Errorf("%w: %s %s: %s", ErrInvalidEvent, e.Kind, e.EventID, fmt.Sprintf(format, args...))
	}
	switch {
	case !knownKind(e.Kind):
		return fmt.Errorf("%w: unknown kind %q", ErrInvalidEvent, e.Kind)
	case strings.TrimSpace(e.EventID) == "":
		return fail("no event id")
	case e.At.IsZero():
		return fail("no time")
	case strings.TrimSpace(e.AppointmentID) == "":
		return fail("no appointment_id")
	case strings.TrimSpace(e.SiteID) == "":
		return fail("no site_id")
	}
	return e.validateKind(fail)
}

func (e Event) validateKind(fail func(string, ...any) error) error {
	switch e.Kind {
	case Booked, Rescheduled:
		switch {
		case e.WindowStart.IsZero() || e.WindowEnd.IsZero():
			return fail("window_start and window_end are required")
		case !e.WindowEnd.After(e.WindowStart):
			return fail("window_end must be after window_start")
		case e.Version < 1:
			return fail("version must be at least 1")
		}
	case CheckedIn:
		if !e.ArrivalStatus.Valid() {
			return fail("arrival_status %q is not EARLY, ON_TIME or LATE", e.ArrivalStatus)
		}
		if e.CheckedInAt.IsZero() {
			return fail("no checked_in_at")
		}
	case Completed:
		if e.CompletedAt.IsZero() {
			return fail("no completed_at")
		}
	case Cancelled, NoShow:
	}
	return nil
}

func knownKind(k Kind) bool {
	for _, known := range Kinds() {
		if k == known {
			return true
		}
	}
	return false
}

// Projection folds events into the analytical model. Apply is idempotent per
// EventID: it records the id and the effect in ONE transaction, and a second
// call with the same id changes nothing (applied = false). A deterministic
// refusal of the store wraps ErrRejected; any other error is transient.
type Projection interface {
	Apply(ctx context.Context, e Event) (applied bool, err error)
}
