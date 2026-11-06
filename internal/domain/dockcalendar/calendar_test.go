package dockcalendar_test

import (
	"errors"
	"reflect"
	"testing"
	"time"

	dc "github.com/claudioed/dock-scheduling/internal/domain/dockcalendar"
)

const (
	closureA = "clo-123e4567-e89b-12d3-a456-426614174001"
	closureB = "clo-223e4567-e89b-12d3-a456-426614174001"
	sp       = "America/Sao_Paulo" // UTC-3, no DST since 2019
)

var (
	now = time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	// 2026-11-09 is a Monday.
	monUTC = func(h, m int) time.Time { return time.Date(2026, 11, 9, h, m, 0, 0, time.UTC) }
)

func newCal(t *testing.T) *dc.DockCalendar {
	t.Helper()
	c, err := dc.New("WH1")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func row(t *testing.T, door string, wd dc.Weekday, open, closeAt, tz string) dc.OperatingHours {
	t.Helper()
	r, err := dc.NewOperatingHours(door, wd, open, closeAt, tz)
	if err != nil {
		t.Fatalf("NewOperatingHours(%s %s %s-%s %s): %v", door, wd, open, closeAt, tz, err)
	}
	return r
}

func declare(t *testing.T, c *dc.DockCalendar, rows ...dc.OperatingHours) {
	t.Helper()
	if _, err := c.DeclareOperatingHours(rows, nil, now); err != nil {
		t.Fatal(err)
	}
}

func TestNewCalendarHasTheDefaults(t *testing.T) {
	c := newCal(t)
	want := dc.Settings{MaxWindowMinutes: 240, SlotGranularityMinutes: 15, ArrivalToleranceMinutes: 15, NoShowGraceMinutes: 60}
	if c.Settings() != want || c.Version() != 0 || c.SiteID() != "WH1" || len(c.OperatingHours()) != 0 || len(c.Closures()) != 0 {
		t.Fatalf("defaults wrong: %+v v%d", c.Settings(), c.Version())
	}
	if _, err := dc.New(""); err == nil {
		t.Fatal("blank site must be refused")
	}
}

func TestSettingsValidate(t *testing.T) {
	ok := dc.DefaultSettings()
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	if ok.MaxWindow() != 4*time.Hour || ok.Granularity() != 15*time.Minute || ok.ArrivalTolerance() != 15*time.Minute || ok.NoShowGrace() != time.Hour {
		t.Fatalf("durations: %v %v %v %v", ok.MaxWindow(), ok.Granularity(), ok.ArrivalTolerance(), ok.NoShowGrace())
	}
	edge := dc.Settings{MaxWindowMinutes: 1, SlotGranularityMinutes: 240, ArrivalToleranceMinutes: 0, NoShowGraceMinutes: 1440}
	if err := edge.Validate(); err != nil {
		t.Fatalf("edges are valid: %v", err)
	}
	edge2 := dc.Settings{MaxWindowMinutes: 240, SlotGranularityMinutes: 1, ArrivalToleranceMinutes: 240, NoShowGraceMinutes: 0}
	if err := edge2.Validate(); err != nil {
		t.Fatalf("edges are valid: %v", err)
	}
	for name, mut := range map[string]func(*dc.Settings){
		"max 0":         func(s *dc.Settings) { s.MaxWindowMinutes = 0 },
		"max 241":       func(s *dc.Settings) { s.MaxWindowMinutes = 241 },
		"gran 0":        func(s *dc.Settings) { s.SlotGranularityMinutes = 0 },
		"gran 241":      func(s *dc.Settings) { s.SlotGranularityMinutes = 241 },
		"tolerance -1":  func(s *dc.Settings) { s.ArrivalToleranceMinutes = -1 },
		"tolerance 241": func(s *dc.Settings) { s.ArrivalToleranceMinutes = 241 },
		"grace -1":      func(s *dc.Settings) { s.NoShowGraceMinutes = -1 },
		"grace 1441":    func(s *dc.Settings) { s.NoShowGraceMinutes = 1441 },
	} {
		s := dc.DefaultSettings()
		mut(&s)
		if err := s.Validate(); !errors.Is(err, dc.ErrInvalidSettings) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestDeclareSettings(t *testing.T) {
	c := newCal(t)
	s := dc.Settings{MaxWindowMinutes: 120, SlotGranularityMinutes: 30, ArrivalToleranceMinutes: 10, NoShowGraceMinutes: 90}
	if err := c.DeclareSettings(s); err != nil || c.Settings() != s || c.Version() != 1 {
		t.Fatalf("DeclareSettings: %v %+v v%d", err, c.Settings(), c.Version())
	}
	if err := c.DeclareSettings(dc.Settings{}); !errors.Is(err, dc.ErrInvalidSettings) || c.Version() != 1 || c.Settings() != s {
		t.Fatalf("invalid settings must change nothing: %v", err)
	}
}

func TestNewOperatingHours(t *testing.T) {
	r, err := dc.NewOperatingHours("D1", dc.Mon, "06:00", "24:00", sp)
	if err != nil || r.DoorCode() != "D1" || r.Weekday() != dc.Mon || r.OpensAt() != "06:00" || r.ClosesAt() != "24:00" || r.Timezone() != sp {
		t.Fatalf("row = %+v, %v", r, err)
	}
	if r, err := dc.NewOperatingHours("", dc.Sun, "00:00", "00:01", "UTC"); err != nil || r.DoorCode() != "" || r.OpensAt() != "00:00" || r.ClosesAt() != "00:01" {
		t.Fatalf("site default row, one-minute day: %+v %v", r, err)
	}
	bad := []struct {
		name              string
		door              string
		wd                dc.Weekday
		open, closeAt, tz string
	}{
		{"bad door", "a b", dc.Mon, "06:00", "22:00", "UTC"},
		{"bad weekday", "", "FUNDAY", "06:00", "22:00", "UTC"},
		{"opens after closes", "", dc.Mon, "22:00", "06:00", "UTC"},
		{"opens equals closes", "", dc.Mon, "06:00", "06:00", "UTC"},
		{"opens at 24:00", "", dc.Mon, "24:00", "24:00", "UTC"},
		{"closes past 24:00", "", dc.Mon, "06:00", "24:01", "UTC"},
		{"hour 25", "", dc.Mon, "25:00", "26:00", "UTC"},
		{"minute 60", "", dc.Mon, "06:60", "22:00", "UTC"},
		{"short form", "", dc.Mon, "6:00", "22:00", "UTC"},
		{"no colon", "", dc.Mon, "06-00", "22:00", "UTC"},
		{"letters", "", dc.Mon, "ab:cd", "22:00", "UTC"},
		{"signed hour", "", dc.Mon, "+6:00", "22:00", "UTC"},
		{"signed minute", "", dc.Mon, "06:+5", "22:00", "UTC"},
		{"negative hour", "", dc.Mon, "-6:00", "22:00", "UTC"},
		{"negative minute", "", dc.Mon, "06:-5", "22:00", "UTC"},
		{"empty time zone", "", dc.Mon, "06:00", "22:00", ""},
		{"local time zone", "", dc.Mon, "06:00", "22:00", "Local"},
		{"unknown time zone", "", dc.Mon, "06:00", "22:00", "Mars/Olympus"},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := dc.NewOperatingHours(tc.door, tc.wd, tc.open, tc.closeAt, tc.tz); !errors.Is(err, dc.ErrInvalidOperatingHours) {
				t.Fatalf("error = %v; want ErrInvalidOperatingHours", err)
			}
		})
	}
}

func TestDeclareOperatingHours(t *testing.T) {
	c := newCal(t)
	r := row(t, "", dc.Mon, "06:00", "22:00", sp)
	evs, err := c.DeclareOperatingHours([]dc.OperatingHours{r}, nil, now)
	if err != nil || len(evs) != 1 || c.Version() != 1 || len(c.OperatingHours()) != 1 {
		t.Fatalf("declare: %v %v v%d", evs, err, c.Version())
	}
	e := evs[0].(dc.OperatingHoursDeclared)
	if e.EventName() != "DoorOperatingHoursDeclared" || e.SiteID() != "WH1" || e.CalendarVersion() != 1 || !e.OccurredAt().Equal(now) || len(e.Rows) != 1 {
		t.Fatalf("event = %+v", e)
	}
	// replacing with the empty set is legal and means 24x7 again
	if _, err := c.DeclareOperatingHours(nil, nil, now); err != nil || len(c.OperatingHours()) != 0 || c.Version() != 2 {
		t.Fatalf("clear: %v", err)
	}
}

func TestDeclareOperatingHoursRejectsDuplicates(t *testing.T) {
	c := newCal(t)
	a, b := row(t, "D1", dc.Mon, "06:00", "12:00", sp), row(t, "D1", dc.Mon, "13:00", "20:00", sp)
	if _, err := c.DeclareOperatingHours([]dc.OperatingHours{a, b}, nil, now); !errors.Is(err, dc.ErrInvalidOperatingHours) || c.Version() != 0 {
		t.Fatalf("duplicate (door, weekday): %v", err)
	}
	other := row(t, "D2", dc.Mon, "13:00", "20:00", sp)
	tue := row(t, "D1", dc.Tue, "13:00", "20:00", sp)
	if _, err := c.DeclareOperatingHours([]dc.OperatingHours{a, other, tue}, nil, now); err != nil {
		t.Fatalf("distinct (door, weekday) rows: %v", err)
	}
	if _, err := c.DeclareOperatingHours([]dc.OperatingHours{{}}, nil, now); !errors.Is(err, dc.ErrInvalidOperatingHours) {
		t.Fatalf("zero row: %v", err)
	}
}

func TestDeclareOperatingHoursRefusesToStrandAppointments(t *testing.T) {
	c := newCal(t)
	// Monday 2026-11-09 08:00-10:00 Sao Paulo = 11:00-13:00 UTC
	inside := dc.Booking{AppointmentID: "appt-1", Door: "D1", Start: monUTC(11, 0), End: monUTC(13, 0)}
	outside := dc.Booking{AppointmentID: "appt-2", Door: "D1", Start: monUTC(1, 0), End: monUTC(3, 0)} // Sunday 22:00 local
	rows := []dc.OperatingHours{row(t, "", dc.Mon, "06:00", "22:00", sp)}
	_, err := c.DeclareOperatingHours(rows, []dc.Booking{inside, outside}, now)
	var ce *dc.ConflictError
	if !errors.Is(err, dc.ErrHoursConflictWithAppointments) || !errors.As(err, &ce) || !reflect.DeepEqual(ce.AppointmentIDs, []string{"appt-2"}) {
		t.Fatalf("err = %v", err)
	}
	if c.Version() != 0 || len(c.OperatingHours()) != 0 {
		t.Fatal("a refused declaration must change nothing")
	}
	if msg := err.Error(); msg == "" || ce.Unwrap() != dc.ErrHoursConflictWithAppointments {
		t.Fatalf("message %q", msg)
	}
	if _, err := c.DeclareOperatingHours(rows, []dc.Booking{inside}, now); err != nil {
		t.Fatalf("appointment inside the hours: %v", err)
	}
}

func TestCheckWindowOperatingHours(t *testing.T) {
	c := newCal(t)
	declare(t, c, row(t, "", dc.Mon, "06:00", "22:00", sp)) // 09:00-01:00(+1) UTC
	tests := []struct {
		name string
		s, e time.Time
		ok   bool
	}{
		{"inside", monUTC(12, 0), monUTC(14, 0), true},
		{"exactly the whole day", monUTC(9, 0), monUTC(25, 0), true},
		{"starts one minute before opening", monUTC(8, 59), monUTC(10, 0), false},
		{"ends one minute after closing", monUTC(23, 0), monUTC(25, 1), false},
		{"touches opening", monUTC(9, 0), monUTC(10, 0), true},
		{"touches closing", monUTC(24, 0), monUTC(25, 0), true},
		{"tuesday is closed", monUTC(33, 0), monUTC(34, 0), false},
		{"thursday has no interval near the range", monUTC(84, 0), monUTC(85, 0), false},
		{"sunday is closed", monUTC(-5, 0), monUTC(-4, 0), false},
		{"spans into the closed night", monUTC(23, 0), monUTC(27, 0), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := c.CheckWindow("ANY", tc.s, tc.e)
			if tc.ok != (err == nil) || (err != nil && !errors.Is(err, dc.ErrDoorUnavailable)) {
				t.Fatalf("CheckWindow = %v; ok want %v", err, tc.ok)
			}
		})
	}
}

func TestNoHoursMeansOpenAllTheTime(t *testing.T) {
	c := newCal(t)
	if err := c.CheckWindow("D1", monUTC(3, 0), monUTC(7, 0)); err != nil {
		t.Fatalf("24x7: %v", err)
	}
}

func TestDoorRowsOverrideTheSiteDefault(t *testing.T) {
	c := newCal(t)
	declare(t, c, row(t, "", dc.Mon, "06:00", "22:00", sp), row(t, "D1", dc.Mon, "08:00", "10:00", sp)) // D1: 11:00-13:00 UTC
	if err := c.CheckWindow("D1", monUTC(11, 0), monUTC(13, 0)); err != nil {
		t.Fatalf("D1 inside its own hours: %v", err)
	}
	if err := c.CheckWindow("D1", monUTC(14, 0), monUTC(15, 0)); !errors.Is(err, dc.ErrDoorUnavailable) {
		t.Fatalf("D1 outside its own hours although inside the site default: %v", err)
	}
	if err := c.CheckWindow("D2", monUTC(14, 0), monUTC(15, 0)); err != nil {
		t.Fatalf("D2 uses the site default: %v", err)
	}
	if err := c.CheckWindow("D1", monUTC(34, 0), monUTC(35, 0)); !errors.Is(err, dc.ErrDoorUnavailable) {
		t.Fatalf("D1 has rows for Monday only: Tuesday is closed: %v", err)
	}
}

func TestOnlyDoorRowsAndNoSiteDefault(t *testing.T) {
	c := newCal(t)
	declare(t, c, row(t, "D1", dc.Mon, "08:00", "10:00", sp))
	if err := c.CheckWindow("D2", monUTC(14, 0), monUTC(15, 0)); err != nil {
		t.Fatalf("a door with no rows and no site default is open 24x7: %v", err)
	}
}

func TestWindowSpanningMidnightNeedsBothDays(t *testing.T) {
	c := newCal(t)
	declare(t, c, row(t, "", dc.Mon, "20:00", "24:00", "UTC"), row(t, "", dc.Tue, "00:00", "02:00", "UTC"))
	if err := c.CheckWindow("D", monUTC(23, 0), monUTC(25, 0)); err != nil {
		t.Fatalf("Monday 23:00 to Tuesday 01:00 is covered by the union: %v", err)
	}
	if err := c.CheckWindow("D", monUTC(23, 0), monUTC(26, 1)); !errors.Is(err, dc.ErrDoorUnavailable) {
		t.Fatalf("one minute past the Tuesday hours: %v", err)
	}
	c2 := newCal(t)
	declare(t, c2, row(t, "", dc.Mon, "20:00", "23:59", "UTC"), row(t, "", dc.Tue, "00:00", "02:00", "UTC"))
	if err := c2.CheckWindow("D", monUTC(23, 0), monUTC(25, 0)); !errors.Is(err, dc.ErrDoorUnavailable) {
		t.Fatalf("a one-minute hole at midnight: %v", err)
	}
}

func TestSplitShiftsOfOneDoorAreSeparateRowsOnDifferentDoorsOnly(t *testing.T) {
	c := newCal(t)
	declare(t, c, row(t, "D1", dc.Mon, "06:00", "12:00", "UTC"), row(t, "D1", dc.Tue, "06:00", "12:00", "UTC"))
	if err := c.CheckWindow("D1", monUTC(6, 0), monUTC(7, 0)); err != nil {
		t.Fatal(err)
	}
}

func TestScheduleClosure(t *testing.T) {
	c := newCal(t)
	k, evs, err := c.ScheduleClosure(closureA, "D1", monUTC(0, 0), monUTC(12, 0), "Leveller maintenance", nil, now)
	if err != nil || len(evs) != 1 || c.Version() != 1 {
		t.Fatalf("schedule: %v %v v%d", evs, err, c.Version())
	}
	e := evs[0].(dc.ClosureScheduled)
	if e.EventName() != "DoorClosureScheduled" || e.Closure != k || e.CalendarVersion() != 1 || e.SiteID() != "WH1" {
		t.Fatalf("event = %+v", e)
	}
	if k.ID != closureA || k.Door != "D1" || !k.From.Equal(monUTC(0, 0)) || !k.To.Equal(monUTC(12, 0)) || k.Reason != "Leveller maintenance" || k.Lifted {
		t.Fatalf("closure = %+v", k)
	}
	if len(c.Closures()) != 1 {
		t.Fatal("closure not stored")
	}
}

func TestScheduleClosureRejects(t *testing.T) {
	tests := []struct {
		name     string
		id, door string
		from, to time.Time
		reason   string
		want     error
	}{
		{"bad id", "clo-1", "D1", monUTC(0, 0), monUTC(1, 0), "x", dc.ErrInvalidClosure},
		{"bad door", closureA, "a b", monUTC(0, 0), monUTC(1, 0), "x", dc.ErrInvalidClosure},
		{"empty span", closureA, "D1", monUTC(1, 0), monUTC(1, 0), "x", dc.ErrInvalidClosure},
		{"reversed span", closureA, "D1", monUTC(2, 0), monUTC(1, 0), "x", dc.ErrInvalidClosure},
		{"blank reason", closureA, "D1", monUTC(0, 0), monUTC(1, 0), "  ", dc.ErrInvalidClosure},
		{"leading control reason", closureA, "D1", monUTC(0, 0), monUTC(1, 0), "\x00abc", dc.ErrInvalidClosure},
		{"control reason", closureA, "D1", monUTC(0, 0), monUTC(1, 0), "a\x00", dc.ErrInvalidClosure},
		{"long reason", closureA, "D1", monUTC(0, 0), monUTC(1, 0), repeat("r", 201), dc.ErrInvalidClosure},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := newCal(t)
			if _, evs, err := c.ScheduleClosure(tc.id, tc.door, tc.from, tc.to, tc.reason, nil, now); !errors.Is(err, tc.want) || evs != nil || c.Version() != 0 {
				t.Fatalf("err = %v", err)
			}
		})
	}
	c := newCal(t)
	if _, _, err := c.ScheduleClosure(closureA, "D1", monUTC(0, 0), monUTC(1, 0), repeat("r", 200), nil, now); err != nil {
		t.Fatalf("a 200-character reason is valid: %v", err)
	}
}

func repeat(s string, n int) string {
	out := ""
	for i := 0; i < n; i++ {
		out += s
	}
	return out
}

func TestClosuresOfOneDoorDoNotOverlap(t *testing.T) {
	c := newCal(t)
	if _, _, err := c.ScheduleClosure(closureA, "D1", monUTC(10, 0), monUTC(12, 0), "x", nil, now); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name     string
		door     string
		from, to time.Time
		want     error
	}{
		{"overlap tail", "D1", monUTC(11, 0), monUTC(13, 0), dc.ErrOverlappingClosure},
		{"overlap head", "D1", monUTC(9, 0), monUTC(11, 0), dc.ErrOverlappingClosure},
		{"contained", "D1", monUTC(10, 30), monUTC(11, 0), dc.ErrOverlappingClosure},
		{"adjacent after", "D1", monUTC(12, 0), monUTC(13, 0), nil},
		{"adjacent before", "D1", monUTC(9, 0), monUTC(10, 0), nil},
		{"other door", "D2", monUTC(10, 0), monUTC(12, 0), nil},
	}
	for i, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			id := "clo-0000000" + string(rune('0'+i)) + "-e89b-12d3-a456-426614174001"
			_, _, err := c.ScheduleClosure(id, tc.door, tc.from, tc.to, "x", nil, now)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v; want %v", err, tc.want)
			}
		})
	}
}

func TestClosureRefusedWhenItCoversActiveAppointments(t *testing.T) {
	c := newCal(t)
	active := []dc.Booking{
		{AppointmentID: "appt-a", Door: "D1", Start: monUTC(10, 0), End: monUTC(11, 0)},
		{AppointmentID: "appt-b", Door: "D1", Start: monUTC(11, 30), End: monUTC(12, 30)},
		{AppointmentID: "appt-c", Door: "D1", Start: monUTC(12, 0), End: monUTC(13, 0)}, // adjacent to the closure end
		{AppointmentID: "appt-d", Door: "D2", Start: monUTC(10, 0), End: monUTC(11, 0)}, // other door
		{AppointmentID: "appt-e", Door: "D1", Start: monUTC(8, 0), End: monUTC(9, 0)},   // before
	}
	_, evs, err := c.ScheduleClosure(closureA, "D1", monUTC(9, 0), monUTC(12, 0), "x", active, now)
	var ce *dc.ConflictError
	if !errors.Is(err, dc.ErrClosureConflictsWithAppointments) || !errors.As(err, &ce) || !reflect.DeepEqual(ce.AppointmentIDs, []string{"appt-a", "appt-b"}) || evs != nil {
		t.Fatalf("err = %v ids %v", err, ce)
	}
	if c.Version() != 0 || len(c.Closures()) != 0 {
		t.Fatal("a refused closure must change nothing")
	}
}

func TestLiftClosure(t *testing.T) {
	c := newCal(t)
	_, _, _ = c.ScheduleClosure(closureA, "D1", monUTC(10, 0), monUTC(12, 0), "x", nil, now)
	if err := c.CheckWindow("D1", monUTC(10, 0), monUTC(11, 0)); !errors.Is(err, dc.ErrDoorUnavailable) {
		t.Fatalf("a scheduled closure blocks the door: %v", err)
	}
	if err := c.CheckWindow("D1", monUTC(12, 0), monUTC(13, 0)); err != nil {
		t.Fatalf("the window after the closure is free: %v", err)
	}
	if err := c.CheckWindow("D1", monUTC(8, 0), monUTC(10, 0)); err != nil {
		t.Fatalf("the window before the closure is free: %v", err)
	}
	k, evs, err := c.LiftClosure(closureA, now)
	if err != nil || len(evs) != 1 || !k.Lifted || c.Version() != 2 {
		t.Fatalf("lift: %v %v v%d", evs, err, c.Version())
	}
	if e := evs[0].(dc.ClosureLifted); e.EventName() != "DoorClosureLifted" || e.Closure.ID != closureA || e.CalendarVersion() != 2 {
		t.Fatalf("event = %+v", e)
	}
	if err := c.CheckWindow("D1", monUTC(10, 0), monUTC(11, 0)); err != nil {
		t.Fatalf("a lifted closure frees the door: %v", err)
	}
	if _, _, err := c.LiftClosure(closureA, now); !errors.Is(err, dc.ErrClosureAlreadyLifted) {
		t.Fatalf("second lift: %v", err)
	}
	if _, _, err := c.LiftClosure(closureB, now); !errors.Is(err, dc.ErrUnknownClosure) {
		t.Fatalf("unknown closure: %v", err)
	}
	// a lifted closure no longer blocks a new overlapping closure
	if _, _, err := c.ScheduleClosure(closureB, "D1", monUTC(10, 0), monUTC(12, 0), "again", nil, now); err != nil {
		t.Fatalf("closure over a lifted one: %v", err)
	}
}

func TestClosureOtherDoorDoesNotBlock(t *testing.T) {
	c := newCal(t)
	_, _, _ = c.ScheduleClosure(closureA, "D1", monUTC(10, 0), monUTC(12, 0), "x", nil, now)
	if err := c.CheckWindow("D2", monUTC(10, 0), monUTC(12, 0)); err != nil {
		t.Fatalf("closure of D1 must not block D2: %v", err)
	}
}
