package dockcalendar

import (
	"sort"
	"time"

	"github.com/claudioed/dock-scheduling/internal/domain/shared"
)

// Availability answers the free windows of a door inside [from, to): the
// operating hours minus the closures minus the active appointments of the
// door (busy), cut on the site's slot granularity (start rounded up, end
// rounded down) and dropped when shorter than min (a zero min keeps every
// window of at least one granule). It also returns the busy spans in order.
func (c *DockCalendar) Availability(door shared.DoorCode, from, to time.Time, appointments []Span, min time.Duration) (free, busy []Span) {
	rows := rowsFor(c.hours, door)
	open := []Span{{Start: from, End: to}}
	if len(rows) > 0 {
		open = clip(openSpans(rows, from, to), from, to)
		busy = append(busy, outOfHours(open, from, to)...)
	}
	for _, k := range c.closures {
		if !k.Lifted && k.Door == door && overlaps(k.From, k.To, from, to) {
			busy = append(busy, Span{Start: maxTime(k.From, from), End: minTime(k.To, to), Kind: KindClosure, Ref: k.ID})
		}
	}
	for _, a := range appointments {
		if overlaps(a.Start, a.End, from, to) {
			busy = append(busy, Span{Start: maxTime(a.Start, from), End: minTime(a.End, to), Kind: KindAppointment, Ref: a.Ref})
		}
	}
	sort.SliceStable(busy, func(i, j int) bool { return busy[i].Start.Before(busy[j].Start) })
	g := c.settings.Granularity()
	if min < g {
		min = g
	}
	for _, o := range open {
		for _, piece := range subtract(o, busy) {
			s, e := roundUp(piece.Start, g), floor(piece.End, g)
			if e.Sub(s) >= min {
				free = append(free, Span{Start: s, End: e})
			}
		}
	}
	return free, busy
}

// clip limits spans to [from, to).
func clip(spans []Span, from, to time.Time) []Span {
	var out []Span
	for _, s := range spans {
		if s.Start.Before(from) {
			s.Start = from
		}
		if s.End.After(to) {
			s.End = to
		}
		if s.End.After(s.Start) {
			out = append(out, s)
		}
	}
	return out
}

// outOfHours returns the parts of [from, to) not covered by the open spans.
func outOfHours(open []Span, from, to time.Time) []Span {
	var out []Span
	cursor := from
	for _, o := range open {
		if o.Start.After(cursor) {
			out = append(out, Span{Start: cursor, End: o.Start, Kind: KindOutOfHours})
		}
		cursor = o.End
	}
	if cursor.Before(to) {
		out = append(out, Span{Start: cursor, End: to, Kind: KindOutOfHours})
	}
	return out
}

// subtract removes the busy spans (sorted by start) from one open span.
func subtract(o Span, busy []Span) []Span {
	var out []Span
	cursor := o.Start
	for _, b := range busy {
		if !overlaps(b.Start, b.End, cursor, o.End) {
			continue
		}
		if b.Start.After(cursor) {
			out = append(out, Span{Start: cursor, End: b.Start})
		}
		if b.End.After(cursor) {
			cursor = b.End
		}
	}
	if cursor.Before(o.End) {
		out = append(out, Span{Start: cursor, End: o.End})
	}
	return out
}

// floor rounds t down to the slot grid: whole multiples of g since the Unix
// epoch, the same grid dockappointment.NewWindow enforces.
func floor(t time.Time, g time.Duration) time.Time {
	step := int64(g / time.Second)
	secs := t.Unix()
	return time.Unix(secs-secs%step, 0).UTC()
}

func roundUp(t time.Time, g time.Duration) time.Time {
	r := floor(t, g)
	if r.Before(t) {
		r = r.Add(g)
	}
	return r
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
