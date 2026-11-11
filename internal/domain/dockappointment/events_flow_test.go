package dockappointment_test

import (
	"testing"
	"time"

	da "github.com/claudioed/dock-scheduling/internal/domain/dockappointment"
	"github.com/claudioed/dock-scheduling/internal/domain/shared"
)

// Every event carries the direction of its appointment in its Header: the
// standard vocabularies a fact maps to (ADR 0013) depend on it, and an event
// that forgot it would silently publish no status.
func TestEveryEventCarriesTheDirectionOfItsAppointment(t *testing.T) {
	for _, c := range []struct {
		name string
		p    da.BookParams
		want shared.Direction
	}{{"inbound", inbound(), shared.Inbound}, {"outbound", outbound(), shared.Outbound}} {
		t.Run(c.name, func(t *testing.T) {
			var raised []da.Event
			collect := func(evs []da.Event, err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
				raised = append(raised, evs...)
			}
			a, booked, err := da.Book(c.p)
			collect(booked, err)
			collect(a.Reschedule("", t900, t1200, rules, now))
			collect(a.ReportEta(t1200.Add(time.Hour), "api", rules, now)) // late: AtRisk
			collect(a.MarkAtRisk(da.DoorReassignedOverlap, now))
			collect(a.ReassignDoor("WH1-DOCK-IN-02", now))
			collect(a.RecordArrival(t900, "visit-1", rules, now))
			collect(a.RecordDoorRelease(t1000, now))

			cancelled := mustBook(t, c.p)
			collect(cancelled.Cancel(da.CarrierRequest, now))
			missed := mustBook(t, c.p)
			collect(missed.MarkNoShow(rules, t1000.Add(2*time.Hour)))

			want := map[string]bool{
				"DockAppointmentBooked": true, "DockAppointmentRescheduled": true, "DockAppointmentAtRisk": true, "DockAppointmentDoorReassigned": true,
				"DockAppointmentCheckedIn": true, "DockAppointmentCompleted": true, "DockAppointmentCancelled": true, "DockAppointmentNoShow": true,
			}
			for _, e := range raised {
				delete(want, e.EventName())
				flow := eventFlow(t, e)
				if flow != c.want {
					t.Errorf("%s carries direction %q, want %q", e.EventName(), flow, c.want)
				}
			}
			if len(want) != 0 {
				t.Errorf("the test did not raise %v", want)
			}
		})
	}
}

func eventFlow(t *testing.T, e da.Event) shared.Direction {
	t.Helper()
	switch f := e.(type) {
	case da.BookedEvent:
		return f.Flow
	case da.RescheduledEvent:
		return f.Flow
	case da.CancelledEvent:
		return f.Flow
	case da.NoShowEvent:
		return f.Flow
	case da.CheckedInEvent:
		return f.Flow
	case da.DoorReassigned:
		return f.Flow
	case da.CompletedEvent:
		return f.Flow
	case da.AtRisk:
		return f.Flow
	}
	t.Fatalf("unknown event %T", e)
	return ""
}
