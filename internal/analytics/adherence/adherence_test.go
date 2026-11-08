package adherence_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/claudioed/dock-scheduling/internal/analytics/adherence"
)

var (
	t0 = time.Date(2026, 11, 6, 8, 0, 0, 0, time.UTC)
	t1 = t0.Add(time.Hour)
)

func base(k adherence.Kind) adherence.Event {
	return adherence.Event{Kind: k, EventID: "e-1", At: t0, AppointmentID: "appt-1", SiteID: "WH1"}
}

func with(k adherence.Kind, f func(*adherence.Event)) adherence.Event {
	e := base(k)
	f(&e)
	return e
}

func TestValidate(t *testing.T) {
	window := func(e *adherence.Event) { e.WindowStart, e.WindowEnd, e.Version = t0, t1, 1 }
	tests := []struct {
		name    string
		event   adherence.Event
		wantErr string // "" = valid
	}{
		{"booked", with(adherence.Booked, window), ""},
		{"rescheduled", with(adherence.Rescheduled, window), ""},
		{"cancelled needs only the common fields", base(adherence.Cancelled), ""},
		{"no-show needs only the common fields", base(adherence.NoShow), ""},
		{"checked in", with(adherence.CheckedIn, func(e *adherence.Event) { e.ArrivalStatus, e.CheckedInAt = adherence.Late, t0 }), ""},
		{"completed", with(adherence.Completed, func(e *adherence.Event) { e.CompletedAt = t1 }), ""},

		{"unknown kind", base("DockAppointmentAtRisk"), "unknown kind"},
		{"no event id", with(adherence.Cancelled, func(e *adherence.Event) { e.EventID = " " }), "no event id"},
		{"no time", with(adherence.Cancelled, func(e *adherence.Event) { e.At = time.Time{} }), "no time"},
		{"no appointment", with(adherence.Cancelled, func(e *adherence.Event) { e.AppointmentID = "" }), "no appointment_id"},
		{"no site", with(adherence.Cancelled, func(e *adherence.Event) { e.SiteID = "" }), "no site_id"},
		{"booked without a window", with(adherence.Booked, func(e *adherence.Event) { e.Version = 1 }), "window_start and window_end are required"},
		{"booked with an empty window", with(adherence.Booked, func(e *adherence.Event) { e.WindowStart, e.WindowEnd, e.Version = t0, t0, 1 }), "window_end must be after"},
		{"booked without a version", with(adherence.Booked, func(e *adherence.Event) { e.WindowStart, e.WindowEnd = t0, t1 }), "version must be at least 1"},
		{"rescheduled with an inverted window", with(adherence.Rescheduled, func(e *adherence.Event) { e.WindowStart, e.WindowEnd, e.Version = t1, t0, 2 }), "window_end must be after"},
		{"checked in with an unknown status", with(adherence.CheckedIn, func(e *adherence.Event) { e.ArrivalStatus, e.CheckedInAt = "EARLIER", t0 }), "arrival_status"},
		{"checked in without a status", with(adherence.CheckedIn, func(e *adherence.Event) { e.CheckedInAt = t0 }), "arrival_status"},
		{"checked in without an instant", with(adherence.CheckedIn, func(e *adherence.Event) { e.ArrivalStatus = adherence.OnTime }), "no checked_in_at"},
		{"completed without an instant", base(adherence.Completed), "no completed_at"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.event.Validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, adherence.ErrInvalidEvent) || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Validate() = %v, want ErrInvalidEvent containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestKindsAreTheSixFoldedEvents(t *testing.T) {
	want := []adherence.Kind{adherence.Booked, adherence.Rescheduled, adherence.CheckedIn, adherence.Completed, adherence.Cancelled, adherence.NoShow}
	got := adherence.Kinds()
	if len(got) != len(want) {
		t.Fatalf("Kinds() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Kinds()[%d] = %s, want %s", i, got[i], want[i])
		}
	}
}

func TestArrivalStatusValid(t *testing.T) {
	for _, s := range []adherence.ArrivalStatus{adherence.Early, adherence.OnTime, adherence.Late} {
		if !s.Valid() {
			t.Errorf("%s must be valid", s)
		}
	}
	for _, s := range []adherence.ArrivalStatus{"", "on_time", "NO_SHOW"} {
		if s.Valid() {
			t.Errorf("%q must not be valid", s)
		}
	}
}
