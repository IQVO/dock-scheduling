//go:build integration

package postgres_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/claudioed/dock-scheduling/internal/domain/dockappointment"
)

var base = time.Date(2030, 1, 7, 8, 0, 0, 0, time.UTC) // a Monday

func apptID(n int) string { return fmt.Sprintf("appt-%08x-e89b-12d3-a456-426614174000", n) }

// booked builds a BOOKED INBOUND appointment on door, starting hoursFromBase after base for one hour,
// with the events its booking raised.
func booked(t *testing.T, n int, door string, hoursFromBase int) (*dockappointment.DockAppointment, []dockappointment.Event) {
	t.Helper()
	start := base.Add(time.Duration(hoursFromBase) * time.Hour)
	a, events, err := dockappointment.Book(dockappointment.BookParams{
		ID: apptID(n), SiteID: "WH1", DoorCode: door, Direction: "INBOUND", Carrier: "ACME", CarrierSCAC: "ACMF",
		WindowStart: start, WindowEnd: start.Add(time.Hour), ASNNumbers: []string{fmt.Sprintf("ASN-%d", n)}, Now: base.Add(-24 * time.Hour),
	})
	if err != nil {
		t.Fatalf("Book: %v", err)
	}
	return a, events
}
