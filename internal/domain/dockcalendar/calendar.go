package dockcalendar

import (
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/claudioed/dock-scheduling/internal/domain/shared"
)

const maxReasonLength = 200

// Closure is a period one door cannot be booked.
type Closure struct {
	ID     string
	Door   shared.DoorCode
	From   time.Time
	To     time.Time
	Reason string
	Lifted bool
}

// DockCalendar is the aggregate root, one per site.
//
// Invariants: opens_at < closes_at in every row and one row per (door,
// weekday); closures of one door do not overlap; a closure never covers an
// active appointment of its door; new operating hours never leave an active
// appointment outside them; Version starts at 0 (nothing declared) and
// increases by one per accepted change.
type DockCalendar struct {
	site     shared.SiteID
	settings Settings
	hours    []OperatingHours
	closures []Closure
	version  int64
}

// New returns the lazily created calendar of a site: defaults, no hours (open
// 24x7), no closures, version 0.
func New(siteID string) (*DockCalendar, error) {
	site, err := shared.NewSiteID(siteID)
	if err != nil {
		return nil, err
	}
	return &DockCalendar{site: site, settings: DefaultSettings()}, nil
}

// SiteID returns the site.
func (c *DockCalendar) SiteID() shared.SiteID { return c.site }

// Version returns the calendar version.
func (c *DockCalendar) Version() int64 { return c.version }

// Settings returns the booking settings.
func (c *DockCalendar) Settings() Settings { return c.settings }

// OperatingHours returns a copy of the declared rows.
func (c *DockCalendar) OperatingHours() []OperatingHours {
	return append([]OperatingHours(nil), c.hours...)
}

// Closures returns a copy of every closure, lifted ones included.
func (c *DockCalendar) Closures() []Closure { return append([]Closure(nil), c.closures...) }

// DeclareOperatingHours replaces the whole set of rows. It refuses with a
// ConflictError wrapping ErrHoursConflictWithAppointments, listing the ids,
// when an active appointment would fall outside the new hours.
func (c *DockCalendar) DeclareOperatingHours(rows []OperatingHours, active []Booking, now time.Time) ([]Event, error) {
	seen := make(map[[2]string]struct{}, len(rows))
	for _, r := range rows {
		key := [2]string{string(r.door), string(r.weekday)}
		if _, dup := seen[key]; dup || r.loc == nil {
			return nil, ErrInvalidOperatingHours
		}
		seen[key] = struct{}{}
	}
	candidate := &DockCalendar{site: c.site, settings: c.settings, hours: append([]OperatingHours(nil), rows...)}
	var stranded []string
	for _, b := range active {
		if candidate.openFor(b.Door, b.Start, b.End) != nil {
			stranded = append(stranded, b.AppointmentID)
		}
	}
	if len(stranded) > 0 {
		return nil, &ConflictError{Sentinel: ErrHoursConflictWithAppointments, AppointmentIDs: stranded}
	}
	c.hours = candidate.hours
	c.version++
	return []Event{OperatingHoursDeclared{Header: c.header(now), Rows: c.OperatingHours()}}, nil
}

// DeclareSettings sets the booking settings and bumps the version. No event is raised.
func (c *DockCalendar) DeclareSettings(s Settings) error {
	if err := s.Validate(); err != nil {
		return err
	}
	c.settings = s
	c.version++
	return nil
}

// ScheduleClosure closes a door for [from, to). active are the active
// appointments of the door; any that overlap the span refuse the closure with
// a ConflictError wrapping ErrClosureConflictsWithAppointments.
func (c *DockCalendar) ScheduleClosure(id, doorCode string, from, to time.Time, reason string, active []Booking, now time.Time) (Closure, []Event, error) {
	door, err := shared.NewDoorCode(doorCode)
	if err != nil || !closureIDPattern.MatchString(id) || !to.After(from) || !validReason(reason) {
		return Closure{}, nil, ErrInvalidClosure
	}
	for _, k := range c.closures {
		if !k.Lifted && k.Door == door && overlaps(k.From, k.To, from, to) {
			return Closure{}, nil, ErrOverlappingClosure
		}
	}
	var conflicting []string
	for _, b := range active {
		if b.Door == door && overlaps(b.Start, b.End, from, to) {
			conflicting = append(conflicting, b.AppointmentID)
		}
	}
	if len(conflicting) > 0 {
		return Closure{}, nil, &ConflictError{Sentinel: ErrClosureConflictsWithAppointments, AppointmentIDs: conflicting}
	}
	k := Closure{ID: id, Door: door, From: from.UTC(), To: to.UTC(), Reason: reason}
	c.closures = append(c.closures, k)
	c.version++
	return k, []Event{ClosureScheduled{Header: c.header(now), Closure: k}}, nil
}

func validReason(reason string) bool {
	return strings.TrimSpace(reason) != "" && utf8.RuneCountInString(reason) <= maxReasonLength && strings.IndexFunc(reason, unicode.IsControl) < 0
}

// LiftClosure lifts a scheduled closure so the door can be booked again.
func (c *DockCalendar) LiftClosure(id string, now time.Time) (Closure, []Event, error) {
	for i := range c.closures {
		if c.closures[i].ID != id {
			continue
		}
		if c.closures[i].Lifted {
			return Closure{}, nil, ErrClosureAlreadyLifted
		}
		c.closures[i].Lifted = true
		c.version++
		k := c.closures[i]
		return k, []Event{ClosureLifted{Header: c.header(now), Closure: k}}, nil
	}
	return Closure{}, nil, ErrUnknownClosure
}

// CheckWindow returns ErrDoorUnavailable when [start, end) is not entirely
// inside the door's operating hours or overlaps a scheduled closure of it.
func (c *DockCalendar) CheckWindow(door shared.DoorCode, start, end time.Time) error {
	for _, k := range c.closures {
		if !k.Lifted && k.Door == door && overlaps(k.From, k.To, start, end) {
			return ErrDoorUnavailable
		}
	}
	return c.openFor(door, start, end)
}

// openFor checks the operating hours only.
func (c *DockCalendar) openFor(door shared.DoorCode, start, end time.Time) error {
	rows := rowsFor(c.hours, door)
	if len(rows) == 0 {
		return nil
	}
	for _, s := range openSpans(rows, start, end) {
		if !s.Start.After(start) && !s.End.Before(end) {
			return nil
		}
	}
	return ErrDoorUnavailable
}

func (c *DockCalendar) header(at time.Time) Header {
	return Header{Site: string(c.site), Version: c.version, At: at.UTC()}
}
