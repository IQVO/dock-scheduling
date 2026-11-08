// Package repocontract is the behavioural contract every implementation of the
// outbound ports must satisfy; the in-memory store and the Postgres adapter
// both run it, so they cannot drift apart.
//
// The suite is split by concern to stay under the fleet's 300-line code cap:
// this file holds the harness (Env, Factory, Run and the shared builders);
// appointments_contract.go the appointment repository; views_contract.go the
// calendar, door, ASN and cutoff local copies; infra_contract.go the claim,
// idempotency and unit-of-work behaviour.
package repocontract

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/claudioed/dock-scheduling/internal/application/ports"
	"github.com/claudioed/dock-scheduling/internal/domain/dockappointment"
)

// Env is one fresh, empty implementation under test.
type Env struct {
	Appointments ports.AppointmentRepo
	Calendars    ports.CalendarRepo
	Doors        ports.DoorView
	Asns         ports.AsnView
	Cutoffs      ports.CutoffView
	Processed    ports.ProcessedEvents
	Idempotency  ports.IdempotencyStore
	Events       ports.EventPublisher
	UoW          ports.UnitOfWork
}

// Factory builds a fresh Env per test.
type Factory func(t *testing.T) Env

// Run executes the whole contract.
func Run(t *testing.T, f Factory) {
	t.Helper()
	for name, fn := range map[string]func(*testing.T, Factory){
		"appointments": runAppointments, "paging": runPaging, "backstops": runBackstops, "legacy-backstops": runLegacyBackstops, "door-override": runDoorOverride, "active": runActiveQueries, "ended-and-lock": runEndedAndLock, "active-by-asn": runActiveByAsnAndDoor, "calendars": runCalendars,
		"calendar-updates": runCalendarUpdates, "doors": runDoors, "door-updates": runDoorUpdates, "asns": runAsns, "cutoffs": runCutoffs, "claims": runClaims, "idempotency": runIdempotency, "rollback": runRollback,
	} {
		t.Run(name, func(t *testing.T) { fn(t, f) })
	}
}

var base = time.Date(2030, 1, 7, 8, 0, 0, 0, time.UTC) // a Monday

func apptID(n int) string { return fmt.Sprintf("appt-%08x-e89b-12d3-a456-426614174000", n) }

// book builds a BOOKED INBOUND appointment on door starting hoursFromBase after base for one hour.
func book(t *testing.T, n int, door string, hoursFromBase int, asns ...string) *dockappointment.DockAppointment {
	t.Helper()
	if len(asns) == 0 {
		asns = []string{fmt.Sprintf("ASN-%d", n)}
	}
	start := base.Add(time.Duration(hoursFromBase) * time.Hour)
	a, _, err := dockappointment.Book(dockappointment.BookParams{
		ID: apptID(n), SiteID: "WH1", DoorCode: door, Direction: "INBOUND", Carrier: "ACME", CarrierSCAC: "ACMF",
		WindowStart: start, WindowEnd: start.Add(time.Hour), ASNNumbers: asns, Now: base.Add(-24 * time.Hour),
	})
	if err != nil {
		t.Fatalf("Book: %v", err)
	}
	return a
}

func mustSave(t *testing.T, e Env, a *dockappointment.DockAppointment, expected int64) {
	t.Helper()
	if err := e.Appointments.Save(context.Background(), a, expected); err != nil {
		t.Fatalf("Save(%s, %d): %v", a.ID(), expected, err)
	}
}
