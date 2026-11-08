package dockcalendar

import (
	"github.com/claudioed/dock-scheduling/internal/domain/shared"
)

// HoursSnapshot is the persisted form of one operating-hours row.
type HoursSnapshot struct {
	DoorCode string
	Weekday  Weekday
	OpensAt  string
	ClosesAt string
	Timezone string
}

// Snapshot is the persisted form of a calendar. Restore validates it.
type Snapshot struct {
	SiteID   string
	Settings Settings
	Hours    []HoursSnapshot
	Closures []Closure
	Version  int64
}

// Snapshot returns the persisted form of the calendar.
func (c *DockCalendar) Snapshot() Snapshot {
	hs := make([]HoursSnapshot, len(c.hours))
	for i, h := range c.hours {
		hs[i] = HoursSnapshot{DoorCode: string(h.door), Weekday: h.weekday, OpensAt: h.OpensAt(), ClosesAt: h.ClosesAt(), Timezone: h.timezone}
	}
	return Snapshot{SiteID: string(c.site), Settings: c.settings, Hours: hs, Closures: c.Closures(), Version: c.version}
}

// Restore rebuilds a persisted calendar, re-validating every invariant.
func Restore(s Snapshot) (*DockCalendar, error) {
	site, err := shared.NewSiteID(s.SiteID)
	if err != nil {
		return nil, err
	}
	if err := s.Settings.Validate(); err != nil {
		return nil, err
	}
	if s.Version < 0 {
		return nil, ErrInvalidSnapshot
	}
	c := &DockCalendar{site: site, settings: s.Settings, version: s.Version}
	if c.hours, err = restoreHours(s.Hours); err != nil {
		return nil, err
	}
	if c.closures, err = restoreClosures(s.Closures); err != nil {
		return nil, err
	}
	return c, nil
}

func restoreHours(snaps []HoursSnapshot) ([]OperatingHours, error) {
	seen := map[[2]string]struct{}{}
	var rows []OperatingHours
	for _, h := range snaps {
		row, err := NewOperatingHours(h.DoorCode, h.Weekday, h.OpensAt, h.ClosesAt, h.Timezone)
		if err != nil {
			return nil, err
		}
		key := [2]string{h.DoorCode, string(h.Weekday)}
		if _, dup := seen[key]; dup {
			return nil, ErrInvalidOperatingHours
		}
		seen[key] = struct{}{}
		rows = append(rows, row)
	}
	return rows, nil
}

func restoreClosures(snaps []Closure) ([]Closure, error) {
	var out []Closure
	for _, k := range snaps {
		door, err := shared.NewDoorCode(string(k.Door))
		if err != nil || !closureIDPattern.MatchString(k.ID) || !k.To.After(k.From) || !validReason(k.Reason) {
			return nil, ErrInvalidClosure
		}
		for _, other := range out {
			if !k.Lifted && !other.Lifted && other.Door == door && overlaps(other.From, other.To, k.From, k.To) {
				return nil, ErrOverlappingClosure
			}
		}
		out = append(out, Closure{ID: k.ID, Door: door, From: k.From.UTC(), To: k.To.UTC(), Reason: k.Reason, Lifted: k.Lifted})
	}
	return out, nil
}
