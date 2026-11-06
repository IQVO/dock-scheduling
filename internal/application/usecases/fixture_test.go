package usecases_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/claudioed/dock-scheduling/internal/adapters/outbound/memory"
	"github.com/claudioed/dock-scheduling/internal/application/repository"
	"github.com/claudioed/dock-scheduling/internal/application/usecases"
	"github.com/claudioed/dock-scheduling/internal/domain/dockappointment"
	"github.com/claudioed/dock-scheduling/internal/domain/shared"
)

var (
	t0   = time.Date(2030, 1, 7, 6, 0, 0, 0, time.UTC) // a Monday, "now"
	at8  = time.Date(2030, 1, 7, 8, 0, 0, 0, time.UTC)
	at9  = at8.Add(time.Hour)
	at10 = at8.Add(2 * time.Hour)
	at11 = at8.Add(3 * time.Hour)
	at12 = at8.Add(4 * time.Hour)
)

type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time { return c.now }

type fakeIDs struct{ appt, closure int }

func (f *fakeIDs) NewAppointmentID() string {
	f.appt++
	return fmt.Sprintf("appt-%08x-e89b-12d3-a456-426614174000", f.appt)
}

func (f *fakeIDs) NewClosureID() string {
	f.closure++
	return fmt.Sprintf("clo-%08x-e89b-12d3-a456-426614174001", f.closure)
}

func apptN(n int) string { return fmt.Sprintf("appt-%08x-e89b-12d3-a456-426614174000", n) }
func cloN(n int) string  { return fmt.Sprintf("clo-%08x-e89b-12d3-a456-426614174001", n) }

type env struct {
	t       *testing.T
	store   *memory.Store
	metrics *memory.Metrics
	clock   *fakeClock
	ids     *fakeIDs
	svc     *usecases.Service
	evSeq   int
}

func newEnv(t *testing.T) *env {
	t.Helper()
	s := memory.NewStore()
	e := &env{t: t, store: s, metrics: &memory.Metrics{}, clock: &fakeClock{now: t0}, ids: &fakeIDs{}}
	e.svc = usecases.New(usecases.Deps{
		Appointments: s.Appointments(), Calendars: s.Calendars(), Doors: s.Doors(), Asns: s.Asns(), Cutoffs: s.Cutoffs(),
		Processed: s, Events: s, Metrics: e.metrics, IDs: e.ids, Clock: e.clock, UoW: s,
	})
	ctx := context.Background()
	must(t, s.Doors().UpsertZone(ctx, "zone-dock", "WH1"))
	must(t, s.Doors().UpsertZone(ctx, "zone-dock2", "WH2"))
	must(t, s.Doors().UpsertDoor(ctx, "D-IN", "zone-dock", shared.FlowInbound))
	must(t, s.Doors().UpsertDoor(ctx, "D-OUT", "zone-dock", shared.FlowOutbound))
	must(t, s.Doors().UpsertDoor(ctx, "D-BOTH", "zone-dock", shared.FlowBoth))
	must(t, s.Doors().UpsertDoor(ctx, "D-BOTH2", "zone-dock", shared.FlowBoth))
	must(t, s.Doors().UpsertDoor(ctx, "D-OTHER", "zone-dock2", shared.FlowBoth))
	must(t, s.Asns().Register(ctx, "ASN-1", at8))
	must(t, s.Asns().Register(ctx, "ASN-2", at8))
	must(t, s.Asns().Register(ctx, "ASN-3", at8))
	must(t, s.Cutoffs().Replace(ctx, "WH1", "UTC", []repository.Cutoff{{ID: "WH1-1500"}}))
	return e
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// eventID returns a fresh CloudEvents id for a consumer call.
func (e *env) eventID() string {
	e.evSeq++
	return fmt.Sprintf("evt-%d", e.evSeq)
}

func inboundCmd(door string, start, end time.Time, asns ...string) usecases.BookCommand {
	if len(asns) == 0 {
		asns = []string{"ASN-1"}
	}
	return usecases.BookCommand{
		DoorCode: door, Direction: "INBOUND", Carrier: "ACME Freight", CarrierSCAC: "ACMF", WindowStart: start, WindowEnd: end, ASNNumbers: asns,
	}
}

func outboundCmd(door string, start, end time.Time) usecases.BookCommand {
	return usecases.BookCommand{
		DoorCode: door, Direction: "OUTBOUND", Carrier: "ACME Freight", WindowStart: start, WindowEnd: end, CPTID: "WH1-1500",
	}
}

func (e *env) book(cmd usecases.BookCommand) string {
	e.t.Helper()
	v, err := e.svc.BookAppointment(context.Background(), cmd)
	if err != nil {
		e.t.Fatalf("BookAppointment(%+v): %v", cmd, err)
	}
	return v.AppointmentID
}

func (e *env) state(id string) dockappointment.State {
	e.t.Helper()
	v, err := e.svc.GetAppointment(context.Background(), id)
	if err != nil {
		e.t.Fatal(err)
	}
	return dockappointment.State(v.State)
}

// eventNames returns the names of the domain events published so far.
func (e *env) eventNames() []string {
	var out []string
	for _, ev := range e.store.Published() {
		switch v := ev.(type) {
		case dockappointment.Event:
			out = append(out, v.EventName())
		case interface{ EventName() string }:
			out = append(out, v.EventName())
		}
	}
	return out
}
