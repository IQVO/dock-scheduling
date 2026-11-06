package dockcalendar_test

import (
	"errors"
	"reflect"
	"testing"

	dc "github.com/claudioed/dock-scheduling/internal/domain/dockcalendar"
)

func populated(t *testing.T) *dc.DockCalendar {
	t.Helper()
	c := newCal(t)
	declare(t, c, row(t, "", dc.Mon, "06:00", "22:00", sp), row(t, "D1", dc.Tue, "08:00", "24:00", "UTC"))
	_, _, _ = c.ScheduleClosure(closureA, "D1", monUTC(10, 0), monUTC(12, 0), "x", nil, now)
	_, _, _ = c.LiftClosure(closureA, now)
	_, _, _ = c.ScheduleClosure(closureB, "D1", monUTC(10, 0), monUTC(12, 0), "again", nil, now)
	return c
}

func TestSnapshotRestoreRoundTrip(t *testing.T) {
	c := populated(t)
	s := c.Snapshot()
	r, err := dc.Restore(s)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(s, r.Snapshot()) || r.Version() != c.Version() || r.Settings() != c.Settings() {
		t.Fatalf("round trip differs:\n%+v\n%+v", s, r.Snapshot())
	}
	if err := r.CheckWindow("D1", monUTC(10, 0), monUTC(11, 0)); !errors.Is(err, dc.ErrDoorUnavailable) {
		t.Fatalf("the restored calendar still closes the door: %v", err)
	}
	if (dc.Snapshot{}).SiteID != "" {
		t.Fatal("zero snapshot")
	}
	if len(s.Hours) != 2 || s.Hours[1].ClosesAt != "24:00" || s.Hours[1].Timezone != "UTC" {
		t.Fatalf("hours snapshot = %+v", s.Hours)
	}
}

func TestRestoreLiftedClosuresMayOverlap(t *testing.T) {
	s := populated(t).Snapshot() // closureA lifted, closureB scheduled, same span
	if _, err := dc.Restore(s); err != nil {
		t.Fatalf("a lifted closure may overlap a scheduled one: %v", err)
	}
	s.Closures[0].Lifted = false
	if _, err := dc.Restore(s); !errors.Is(err, dc.ErrOverlappingClosure) {
		t.Fatalf("two scheduled overlapping closures: %v", err)
	}
	s.Closures[0].Lifted, s.Closures[1].Lifted = true, true
	if _, err := dc.Restore(s); err != nil {
		t.Fatalf("two lifted closures: %v", err)
	}
	s.Closures[0].Lifted, s.Closures[1].Lifted = false, true
	if _, err := dc.Restore(s); err != nil {
		t.Fatalf("scheduled + lifted: %v", err)
	}
}

func TestRestoreRejectsCorruptRows(t *testing.T) {
	tests := []struct {
		name string
		mut  func(*dc.Snapshot)
		want error
	}{
		{"bad site", func(s *dc.Snapshot) { s.SiteID = "" }, nil},
		{"bad settings", func(s *dc.Snapshot) { s.Settings.MaxWindowMinutes = 0 }, dc.ErrInvalidSettings},
		{"negative version", func(s *dc.Snapshot) { s.Version = -1 }, dc.ErrInvalidSnapshot},
		{"bad hours row", func(s *dc.Snapshot) { s.Hours[0].OpensAt = "25:00" }, dc.ErrInvalidOperatingHours},
		{"duplicate hours row", func(s *dc.Snapshot) { s.Hours[1] = s.Hours[0] }, dc.ErrInvalidOperatingHours},
		{"bad closure id", func(s *dc.Snapshot) { s.Closures[0].ID = "x" }, dc.ErrInvalidClosure},
		{"bad closure door", func(s *dc.Snapshot) { s.Closures[0].Door = "a b" }, dc.ErrInvalidClosure},
		{"empty closure span", func(s *dc.Snapshot) { s.Closures[0].To = s.Closures[0].From }, dc.ErrInvalidClosure},
		{"blank closure reason", func(s *dc.Snapshot) { s.Closures[0].Reason = "" }, dc.ErrInvalidClosure},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := populated(t).Snapshot()
			tc.mut(&s)
			r, err := dc.Restore(s)
			if err == nil || r != nil {
				t.Fatalf("Restore = %v, %v; want error", r, err)
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("error = %v; want %v", err, tc.want)
			}
		})
	}
	s := populated(t).Snapshot()
	s.Version = 0
	if _, err := dc.Restore(s); err != nil {
		t.Fatalf("version 0 is valid: %v", err)
	}
}
