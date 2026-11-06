package dockcalendar

import "time"

// Event is a domain fact raised by the DockCalendar aggregate. Every event
// carries the site id, the calendar version AFTER the change and when it
// happened.
type Event interface {
	EventName() string
	SiteID() string
	CalendarVersion() int64
	OccurredAt() time.Time
}

// Header is embedded in every calendar event.
type Header struct {
	Site    string
	Version int64
	At      time.Time
}

// SiteID returns the site the event is about.
func (h Header) SiteID() string { return h.Site }

// CalendarVersion returns the calendar version after the change.
func (h Header) CalendarVersion() int64 { return h.Version }

// OccurredAt returns when the change happened.
func (h Header) OccurredAt() time.Time { return h.At }

// OperatingHoursDeclared is raised by DeclareOperatingHours (analytics only).
type OperatingHoursDeclared struct {
	Header
	Rows []OperatingHours
}

// EventName returns "DoorOperatingHoursDeclared".
func (OperatingHoursDeclared) EventName() string { return "DoorOperatingHoursDeclared" }

// ClosureScheduled is raised by ScheduleClosure.
type ClosureScheduled struct {
	Header
	Closure Closure
}

// EventName returns "DoorClosureScheduled".
func (ClosureScheduled) EventName() string { return "DoorClosureScheduled" }

// ClosureLifted is raised by LiftClosure.
type ClosureLifted struct {
	Header
	Closure Closure
}

// EventName returns "DoorClosureLifted".
func (ClosureLifted) EventName() string { return "DoorClosureLifted" }
