package dockcalendar

import (
	"sort"
	"strconv"
	"time"

	"github.com/claudioed/dock-scheduling/internal/domain/shared"
)

// Weekday is a day of the week in the contract's MON..SUN spelling.
type Weekday string

// The seven weekdays.
const (
	Mon Weekday = "MON"
	Tue Weekday = "TUE"
	Wed Weekday = "WED"
	Thu Weekday = "THU"
	Fri Weekday = "FRI"
	Sat Weekday = "SAT"
	Sun Weekday = "SUN"
)

var weekdays = map[Weekday]time.Weekday{
	Mon: time.Monday, Tue: time.Tuesday, Wed: time.Wednesday, Thu: time.Thursday, Fri: time.Friday, Sat: time.Saturday, Sun: time.Sunday,
}

// minutesPerDay is the exclusive upper bound of closes_at ("24:00").
const minutesPerDay = 1440

// OperatingHours is one row of the door's opening hours: a weekday interval
// [opens_at, closes_at) in a time zone. An empty door means the site default.
type OperatingHours struct {
	door     shared.DoorCode
	weekday  Weekday
	opens    int
	closes   int
	timezone string
	loc      *time.Location
}

// NewOperatingHours validates a row. doorCode "" is the site default; opensAt
// is "HH:MM", closesAt is "HH:MM" or "24:00", opensAt must be before closesAt
// and timezone an IANA zone.
func NewOperatingHours(doorCode string, weekday Weekday, opensAt, closesAt, timezone string) (OperatingHours, error) {
	var door shared.DoorCode
	if doorCode != "" {
		var err error
		if door, err = shared.NewDoorCode(doorCode); err != nil {
			return OperatingHours{}, ErrInvalidOperatingHours
		}
	}
	if _, ok := weekdays[weekday]; !ok {
		return OperatingHours{}, ErrInvalidOperatingHours
	}
	opens, okO := parseClock(opensAt, false)
	closes, okC := parseClock(closesAt, true)
	if !okO || !okC || opens >= closes {
		return OperatingHours{}, ErrInvalidOperatingHours
	}
	loc, err := time.LoadLocation(timezone)
	if err != nil || timezone == "" || timezone == "Local" {
		return OperatingHours{}, ErrInvalidOperatingHours
	}
	return OperatingHours{door: door, weekday: weekday, opens: opens, closes: closes, timezone: timezone, loc: loc}, nil
}

// parseClock parses "HH:MM" into minutes since midnight; allow24 accepts "24:00".
func parseClock(v string, allow24 bool) (int, bool) {
	if len(v) != 5 || v[2] != ':' {
		return 0, false
	}
	h, errH := strconv.Atoi(v[:2])
	m, errM := strconv.Atoi(v[3:])
	if errH != nil || errM != nil || h < 0 || m < 0 || m > 59 || v[0] == '+' || v[3] == '+' || v[0] == '-' || v[3] == '-' {
		return 0, false
	}
	total := h*60 + m
	if total > minutesPerDay || (total == minutesPerDay && !allow24) {
		return 0, false
	}
	return total, true
}

// DoorCode returns the door the row applies to, "" for the site default.
func (o OperatingHours) DoorCode() shared.DoorCode { return o.door }

// Weekday returns the weekday.
func (o OperatingHours) Weekday() Weekday { return o.weekday }

// OpensAt returns the opening time as "HH:MM".
func (o OperatingHours) OpensAt() string { return formatClock(o.opens) }

// ClosesAt returns the closing time as "HH:MM" ("24:00" closes at midnight).
func (o OperatingHours) ClosesAt() string { return formatClock(o.closes) }

// Timezone returns the IANA zone name.
func (o OperatingHours) Timezone() string { return o.timezone }

func formatClock(minutes int) string {
	return pad2(minutes/60) + ":" + pad2(minutes%60)
}

func pad2(n int) string {
	if n < 10 {
		return "0" + strconv.Itoa(n)
	}
	return strconv.Itoa(n)
}

// intervals returns the UTC intervals this row opens around [from, to): the
// local days of the zone that touch the range, one day either side.
func (o OperatingHours) intervals(from, to time.Time) []Span {
	var out []Span
	day := from.In(o.loc).AddDate(0, 0, -1)
	last := to.In(o.loc).AddDate(0, 0, 1)
	for y, m, d := day.Date(); !time.Date(y, m, d, 0, 0, 0, 0, o.loc).After(last); {
		date := time.Date(y, m, d, 0, 0, 0, 0, o.loc)
		if date.Weekday() == weekdays[o.weekday] {
			out = append(out, Span{
				Start: time.Date(y, m, d, 0, o.opens, 0, 0, o.loc).UTC(),
				End:   time.Date(y, m, d, 0, o.closes, 0, 0, o.loc).UTC(),
			})
		}
		y, m, d = date.AddDate(0, 0, 1).Date()
	}
	return out
}

// mergeSpans sorts spans and merges the ones that touch or overlap.
func mergeSpans(spans []Span) []Span {
	if len(spans) == 0 {
		return nil
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].Start.Before(spans[j].Start) })
	merged := []Span{spans[0]}
	for _, s := range spans[1:] {
		last := &merged[len(merged)-1]
		if s.Start.After(last.End) {
			merged = append(merged, s)
			continue
		}
		last.End = maxTime(last.End, s.End)
	}
	return merged
}

// openSpans returns the merged UTC spans the rows are open inside [from, to).
func openSpans(rows []OperatingHours, from, to time.Time) []Span {
	var all []Span
	for _, r := range rows {
		all = append(all, r.intervals(from, to)...)
	}
	return mergeSpans(all)
}

// rowsFor returns the rows that apply to a door: the door's own rows when it
// has any, else the site-default rows. Empty means open 24x7.
func rowsFor(rows []OperatingHours, door shared.DoorCode) []OperatingHours {
	var own, site []OperatingHours
	for _, r := range rows {
		switch r.door {
		case door:
			own = append(own, r)
		case "":
			site = append(site, r)
		}
	}
	if len(own) > 0 {
		return own
	}
	return site
}
