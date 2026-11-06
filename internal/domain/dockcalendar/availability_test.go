package dockcalendar_test

import (
	"reflect"
	"testing"
	"time"

	dc "github.com/claudioed/dock-scheduling/internal/domain/dockcalendar"
)

type pair struct{ s, e time.Time }

func pairs(spans []dc.Span) []pair {
	out := make([]pair, len(spans))
	for i, s := range spans {
		out[i] = pair{s.Start, s.End}
	}
	return out
}

func TestAvailabilityOfAnOpenDoor(t *testing.T) {
	c := newCal(t)
	free, busy := c.Availability("D1", monUTC(8, 0), monUTC(12, 0), nil, 0)
	if !reflect.DeepEqual(pairs(free), []pair{{monUTC(8, 0), monUTC(12, 0)}}) || len(busy) != 0 {
		t.Fatalf("free %v busy %v", free, busy)
	}
}

func TestAvailabilitySubtractsAppointmentsAndClosures(t *testing.T) {
	c := newCal(t)
	_, _, _ = c.ScheduleClosure(closureA, "D1", monUTC(10, 0), monUTC(11, 0), "x", nil, now)
	_, _, _ = c.ScheduleClosure(closureB, "D2", monUTC(8, 0), monUTC(12, 0), "other door", nil, now)
	appts := []dc.Span{{Start: monUTC(8, 30), End: monUTC(9, 0), Ref: "appt-1"}, {Start: monUTC(20, 0), End: monUTC(21, 0), Ref: "outside"}}
	free, busy := c.Availability("D1", monUTC(8, 0), monUTC(12, 0), appts, 0)
	want := []pair{{monUTC(8, 0), monUTC(8, 30)}, {monUTC(9, 0), monUTC(10, 0)}, {monUTC(11, 0), monUTC(12, 0)}}
	if !reflect.DeepEqual(pairs(free), want) {
		t.Fatalf("free = %v; want %v", pairs(free), want)
	}
	if len(busy) != 2 || busy[0].Kind != dc.KindAppointment || busy[0].Ref != "appt-1" || busy[1].Kind != dc.KindClosure || busy[1].Ref != closureA {
		t.Fatalf("busy = %+v", busy)
	}
}

func TestAvailabilityClipsBusySpansToTheRange(t *testing.T) {
	c := newCal(t)
	appts := []dc.Span{{Start: monUTC(6, 0), End: monUTC(9, 0), Ref: "a"}, {Start: monUTC(11, 0), End: monUTC(14, 0), Ref: "b"}}
	free, busy := c.Availability("D1", monUTC(8, 0), monUTC(12, 0), appts, 0)
	if !reflect.DeepEqual(pairs(free), []pair{{monUTC(9, 0), monUTC(11, 0)}}) {
		t.Fatalf("free = %v", pairs(free))
	}
	if !busy[0].Start.Equal(monUTC(8, 0)) || !busy[1].End.Equal(monUTC(12, 0)) {
		t.Fatalf("busy not clipped: %+v", busy)
	}
}

func TestAvailabilityMinimumAndGranularity(t *testing.T) {
	c := newCal(t) // granularity 15 min
	// busy 08:10-08:20 leaves 08:00-08:10 (shorter than a granule after rounding) and 08:20-09:00 -> 08:30-09:00
	appts := []dc.Span{{Start: monUTC(8, 10), End: monUTC(8, 20), Ref: "a"}}
	free, _ := c.Availability("D1", monUTC(8, 0), monUTC(9, 0), appts, 0)
	if !reflect.DeepEqual(pairs(free), []pair{{monUTC(8, 30), monUTC(9, 0)}}) {
		t.Fatalf("free = %v", pairs(free))
	}
	free, _ = c.Availability("D1", monUTC(8, 0), monUTC(9, 0), appts, 45*time.Minute)
	if len(free) != 0 {
		t.Fatalf("a 30-minute window is shorter than the 45-minute minimum: %v", free)
	}
	free, _ = c.Availability("D1", monUTC(8, 0), monUTC(9, 0), appts, 30*time.Minute)
	if len(free) != 1 {
		t.Fatalf("a 30-minute window meets the 30-minute minimum: %v", free)
	}
	free, _ = c.Availability("D1", monUTC(8, 5), monUTC(8, 50), nil, 0)
	if !reflect.DeepEqual(pairs(free), []pair{{monUTC(8, 15), monUTC(8, 45)}}) {
		t.Fatalf("range edges are cut on the grid: %v", pairs(free))
	}
}

func TestAvailabilityWithOperatingHours(t *testing.T) {
	c := newCal(t)
	declare(t, c, row(t, "", dc.Mon, "09:00", "11:00", "UTC"))
	free, busy := c.Availability("D1", monUTC(8, 0), monUTC(12, 0), nil, 0)
	if !reflect.DeepEqual(pairs(free), []pair{{monUTC(9, 0), monUTC(11, 0)}}) {
		t.Fatalf("free = %v", pairs(free))
	}
	if len(busy) != 2 || busy[0].Kind != dc.KindOutOfHours || !busy[0].End.Equal(monUTC(9, 0)) || busy[1].Kind != dc.KindOutOfHours || !busy[1].Start.Equal(monUTC(11, 0)) {
		t.Fatalf("busy = %+v", busy)
	}
	// a closed weekday: everything is out of hours
	free, busy = c.Availability("D1", monUTC(32, 0), monUTC(36, 0), nil, 0)
	if len(free) != 0 || len(busy) != 1 || busy[0].Kind != dc.KindOutOfHours || !busy[0].Start.Equal(monUTC(32, 0)) || !busy[0].End.Equal(monUTC(36, 0)) {
		t.Fatalf("closed day: free %v busy %v", free, busy)
	}
}

func TestAvailabilityOverlappingBusySpansDoNotDoubleSubtract(t *testing.T) {
	c := newCal(t)
	_, _, _ = c.ScheduleClosure(closureA, "D1", monUTC(9, 0), monUTC(10, 0), "x", nil, now)
	appts := []dc.Span{{Start: monUTC(8, 30), End: monUTC(9, 30), Ref: "a"}}
	free, _ := c.Availability("D1", monUTC(8, 0), monUTC(11, 0), appts, 0)
	if !reflect.DeepEqual(pairs(free), []pair{{monUTC(8, 0), monUTC(8, 30)}, {monUTC(10, 0), monUTC(11, 0)}}) {
		t.Fatalf("free = %v", pairs(free))
	}
	// a busy span entirely inside an earlier one
	appts = []dc.Span{{Start: monUTC(8, 0), End: monUTC(10, 0), Ref: "big"}, {Start: monUTC(8, 30), End: monUTC(9, 0), Ref: "small"}}
	free, _ = c.Availability("D2", monUTC(8, 0), monUTC(11, 0), appts, 0)
	if !reflect.DeepEqual(pairs(free), []pair{{monUTC(10, 0), monUTC(11, 0)}}) {
		t.Fatalf("nested busy: %v", pairs(free))
	}
}

func TestAvailabilityClipsOpenHoursToTheRange(t *testing.T) {
	c := newCal(t)
	declare(t, c, row(t, "", dc.Mon, "09:00", "11:00", "UTC"))
	free, busy := c.Availability("D1", monUTC(10, 0), monUTC(10, 30), nil, 0)
	if !reflect.DeepEqual(pairs(free), []pair{{monUTC(10, 0), monUTC(10, 30)}}) || len(busy) != 0 {
		t.Fatalf("free %v busy %v", pairs(free), busy)
	}
}
