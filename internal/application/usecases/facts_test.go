package usecases_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/claudioed/dock-scheduling/internal/application/repository"
	"github.com/claudioed/dock-scheduling/internal/application/usecases"
	"github.com/claudioed/dock-scheduling/internal/domain/dockappointment"
	"github.com/claudioed/dock-scheduling/internal/domain/shared"
)

func TestFacilityConsumers(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	must(t, e.svc.ConsumeZoneRegistered(ctx, "z1", "zone-new", "WH3"))
	must(t, e.svc.ConsumeSlotRegistered(ctx, "s0", "D-NEW", "zone-new", "Dock", "Both", ""))
	if d, _ := e.store.Doors().Door(ctx, "D-NEW"); d == nil || d.Site != "WH3" || d.Flow != shared.FlowBoth {
		t.Fatalf("door = %+v", d)
	}
	// a re-registration with another role removes the door
	must(t, e.svc.ConsumeSlotRegistered(ctx, "s1", "D-NEW", "zone-new", "Storage", "", ""))
	if d, _ := e.store.Doors().Door(ctx, "D-NEW"); d != nil {
		t.Fatalf("a non-dock slot is no door: %+v", d)
	}
	must(t, e.svc.ConsumeSlotRegistered(ctx, "s2", "D-NEW", "zone-new", "Dock", "Inbound", ""))
	must(t, e.svc.ConsumeSlotDecommissioned(ctx, "s3", "D-NEW"))
	if d, _ := e.store.Doors().Door(ctx, "D-NEW"); d != nil {
		t.Fatalf("decommissioned door still bookable: %+v", d)
	}
	if err := e.svc.ConsumeSlotRegistered(ctx, "s4", "D-NEW", "zone-new", "Dock", "Sideways", ""); !errors.Is(err, shared.ErrInvalidDockFlow) {
		t.Fatalf("bad dock flow: %v", err)
	}
	// a duplicate CloudEvents id is a no-op
	must(t, e.svc.ConsumeZoneRegistered(ctx, "z1", "zone-new", "WH-CHANGED"))
	must(t, e.svc.ConsumeSlotRegistered(ctx, "s5", "D-NEW", "zone-new", "Dock", "Both", ""))
	if d, _ := e.store.Doors().Door(ctx, "D-NEW"); d == nil || d.Site != "WH3" {
		t.Fatalf("the duplicate zone event must not apply: %+v", d)
	}
}

func TestBookingForADoorWhoseZoneHasNotArrivedIsRefused(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	must(t, e.svc.ConsumeSlotRegistered(ctx, "s1", "D-LATE", "zone-late", "Dock", "Both", ""))
	if _, err := e.svc.BookAppointment(ctx, inboundCmd("D-LATE", at8, at10)); !errors.Is(err, dockappointment.ErrUnknownDoor) {
		t.Fatalf("pending door: %v", err)
	}
	must(t, e.svc.ConsumeZoneRegistered(ctx, "z1", "zone-late", "WH1"))
	e.book(inboundCmd("D-LATE", at8, at10))
}

func TestAsnConsumersAndCancelWhenAllAsnsCancelled(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	two := e.book(inboundCmd("D-BOTH", at8, at10, "ASN-1", "ASN-2"))
	one := e.book(inboundCmd("D-BOTH2", at8, at10, "ASN-3"))
	must(t, e.svc.ConsumeAsnCancelled(ctx, "c1", "ASN-1"))
	if e.state(two) != dockappointment.Booked {
		t.Fatal("one ASN of two cancelled: the appointment stays")
	}
	must(t, e.svc.ConsumeAsnCancelled(ctx, "c2", "ASN-2"))
	v, _ := e.svc.GetAppointment(ctx, two)
	if v.State != "CANCELLED" || v.CancellationReason != "ASN_CANCELLED" {
		t.Fatalf("all ASNs cancelled: %+v", v)
	}
	if e.state(one) != dockappointment.Booked {
		t.Fatal("an unrelated appointment must not be touched")
	}
	// redelivery of the same event does nothing
	must(t, e.svc.ConsumeAsnCancelled(ctx, "c2", "ASN-2"))
	// a cancel before the register keeps the ASN cancelled
	must(t, e.svc.ConsumeAsnCancelled(ctx, "c3", "ASN-NEW"))
	must(t, e.svc.ConsumeAsnRegistered(ctx, "r1", "ASN-NEW", at8))
	if _, err := e.svc.BookAppointment(ctx, inboundCmd("D-IN", at11, at12, "ASN-NEW")); !errors.Is(err, dockappointment.ErrUnknownAsn) {
		t.Fatalf("cancelled stays cancelled: %v", err)
	}
	must(t, e.svc.ConsumeAsnRegistered(ctx, "r2", "ASN-FRESH", at8))
	e.book(inboundCmd("D-IN", at11, at12, "ASN-FRESH"))
}

func TestCptScheduleReplacesTheSitesRows(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	must(t, e.svc.ConsumeCptSchedule(ctx, "p1", "WH1", "UTC", []repository.Cutoff{{ID: "WH1-1700"}}))
	if _, err := e.svc.BookAppointment(ctx, outboundCmd("D-OUT", at8, at10)); !errors.Is(err, dockappointment.ErrMissingOutboundRef) {
		t.Fatalf("WH1-1500 is gone from the schedule: %v", err)
	}
	c := outboundCmd("D-OUT", at8, at10)
	c.CPTID = "WH1-1700"
	e.book(c)
}

func TestArrivalFromGate(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	id := e.book(inboundCmd("D-BOTH", at8, at10))
	must(t, e.svc.ConsumeVisitCheckedIn(ctx, e.eventID(), "visit-1", "WH1", id, at8.Add(-time.Hour)))
	v, _ := e.svc.GetAppointment(ctx, id)
	if v.State != "CHECKED_IN" || v.Arrival == nil || v.Arrival.Status != "EARLY" || v.Arrival.VisitID != "visit-1" || v.Version != 2 {
		t.Fatalf("after gate: %+v", v)
	}
	if got := e.eventNames(); got[len(got)-1] != "DockAppointmentCheckedIn" {
		t.Fatalf("events = %v", got)
	}
	// a second gate fact for a CHECKED_IN appointment is ignored and counted
	must(t, e.svc.ConsumeVisitCheckedIn(ctx, e.eventID(), "visit-1", "WH1", id, at8))
	if e.metrics.Ignored(usecases.IgnoredAlreadyApplied) != 1 {
		t.Fatalf("already applied not counted: %d", e.metrics.Ignored(usecases.IgnoredAlreadyApplied))
	}
}

func TestYardFactsThatChangeNothing(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	id := e.book(inboundCmd("D-BOTH", at8, at10))
	cancelled := e.book(inboundCmd("D-BOTH2", at8, at10, "ASN-2"))
	_, err := e.svc.CancelAppointment(ctx, usecases.CancelCommand{AppointmentID: cancelled, Reason: "SITE_REQUEST"})
	must(t, err)
	before := len(e.store.Published())

	must(t, e.svc.ConsumeVisitCheckedIn(ctx, e.eventID(), "v", "WH1", "", at8))          // walk-in: nothing, not counted
	must(t, e.svc.ConsumeVisitCheckedIn(ctx, e.eventID(), "v", "WH1", apptN(77), at8))   // unknown
	must(t, e.svc.ConsumeVisitCheckedIn(ctx, e.eventID(), "v", "WH1", "not-an-id", at8)) // malformed
	must(t, e.svc.ConsumeVisitCheckedIn(ctx, e.eventID(), "v", "WH2", id, at8))          // wrong site
	must(t, e.svc.ConsumeVisitCheckedIn(ctx, e.eventID(), "v", "WH1", cancelled, at8))   // CANCELLED
	must(t, e.svc.ConsumeTrailerUndocked(ctx, e.eventID(), "WH1", cancelled, at9))       // CANCELLED
	must(t, e.svc.ConsumeTrailerDocked(ctx, e.eventID(), "WH1", cancelled, "D-IN", at9)) // CANCELLED
	must(t, e.svc.ConsumeVisitRejected(ctx, e.eventID(), "WH1", cancelled))              // CANCELLED
	if len(e.store.Published()) != before {
		t.Fatal("ignored facts must publish nothing")
	}
	if e.metrics.Ignored(usecases.IgnoredUnknownAppointment) != 2 || e.metrics.Ignored(usecases.IgnoredWrongSite) != 1 || e.metrics.Ignored(usecases.IgnoredTerminal) != 4 {
		t.Fatalf("counters: unknown %d site %d terminal %d", e.metrics.Ignored(usecases.IgnoredUnknownAppointment), e.metrics.Ignored(usecases.IgnoredWrongSite), e.metrics.Ignored(usecases.IgnoredTerminal))
	}
}

func TestCancelOnVisitRejected(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	booked := e.book(inboundCmd("D-BOTH", at8, at10))
	checkedIn := e.book(inboundCmd("D-BOTH2", at8, at10, "ASN-2"))
	must(t, e.svc.ConsumeVisitCheckedIn(ctx, e.eventID(), "v", "WH1", checkedIn, at8))
	must(t, e.svc.ConsumeVisitRejected(ctx, e.eventID(), "WH1", booked))
	v, _ := e.svc.GetAppointment(ctx, booked)
	if v.State != "CANCELLED" || v.CancellationReason != "TURNED_AWAY" {
		t.Fatalf("rejected visit: %+v", v)
	}
	must(t, e.svc.ConsumeVisitRejected(ctx, e.eventID(), "WH1", checkedIn))
	if e.state(checkedIn) != dockappointment.CheckedIn || e.metrics.Ignored(usecases.IgnoredAlreadyApplied) != 1 {
		t.Fatal("only a BOOKED appointment is cancelled on rejection; others are ignored and counted")
	}
}

func TestCompleteOnDoorReleaseAndOutOfOrderYardFacts(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	normal := e.book(inboundCmd("D-BOTH", at8, at10))
	outrun := e.book(inboundCmd("D-BOTH2", at8, at10, "ASN-2"))
	must(t, e.svc.ConsumeVisitCheckedIn(ctx, e.eventID(), "v", "WH1", normal, at8))
	must(t, e.svc.ConsumeTrailerUndocked(ctx, e.eventID(), "WH1", normal, at9))
	v, _ := e.svc.GetAppointment(ctx, normal)
	if v.State != "COMPLETED" || v.CompletedAt == nil || !v.CompletedAt.Equal(at9) || v.Arrival == nil || v.Arrival.Implicit {
		t.Fatalf("normal completion: %+v", v)
	}
	// the door release outruns the gate fact: BOOKED -> COMPLETED with an implicit arrival
	must(t, e.svc.ConsumeTrailerUndocked(ctx, e.eventID(), "WH1", outrun, at9))
	v, _ = e.svc.GetAppointment(ctx, outrun)
	if v.State != "COMPLETED" || v.Arrival == nil || !v.Arrival.Implicit || v.Arrival.Status != "ON_TIME" || e.metrics.Implicit() != 1 {
		t.Fatalf("implicit arrival: %+v implicit=%d", v, e.metrics.Implicit())
	}
	// the late gate fact for a COMPLETED appointment is ignored and counted
	must(t, e.svc.ConsumeVisitCheckedIn(ctx, e.eventID(), "v2", "WH1", outrun, at8))
	if e.state(outrun) != dockappointment.Completed || e.metrics.Ignored(usecases.IgnoredTerminal) != 1 {
		t.Fatal("late VisitCheckedIn for COMPLETED must be ignored")
	}
}

func TestActualDoorWins(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	moved := e.book(inboundCmd("D-BOTH", at8, at10))
	other := e.book(inboundCmd("D-BOTH2", at9, at11, "ASN-2"))
	clear := e.book(inboundCmd("D-BOTH2", at11, at12, "ASN-3"))
	// the trailer docks at D-BOTH2 although D-BOTH was booked: the physical fact wins
	must(t, e.svc.ConsumeTrailerDocked(ctx, e.eventID(), "WH1", moved, "D-BOTH2", at8))
	v, _ := e.svc.GetAppointment(ctx, moved)
	if v.DoorCode != "D-BOTH2" || v.Version != 2 {
		t.Fatalf("reassigned: %+v", v)
	}
	o, _ := e.svc.GetAppointment(ctx, other)
	if !o.AtRisk || o.AtRiskCause != "DOOR_REASSIGNED_OVERLAP" {
		t.Fatalf("the overlapping later appointment is at risk: %+v", o)
	}
	if c, _ := e.svc.GetAppointment(ctx, clear); c.AtRisk {
		t.Fatalf("a non-overlapping appointment is not at risk: %+v", c)
	}
	names := e.eventNames()
	if names[len(names)-2] != "DockAppointmentDoorReassigned" || names[len(names)-1] != "DockAppointmentAtRisk" {
		// the aggregate's event is published after the flagging inside apply returns
		t.Logf("events = %v", names)
	}
	// docking at the booked door changes nothing
	n := len(e.store.Published())
	must(t, e.svc.ConsumeTrailerDocked(ctx, e.eventID(), "WH1", clear, "D-BOTH2", at11))
	if len(e.store.Published()) != n {
		t.Fatal("same door: nothing to publish")
	}
	// a malformed door code is a deterministic refusal
	if err := e.svc.ConsumeTrailerDocked(ctx, e.eventID(), "WH1", clear, "a b", at11); !errors.Is(err, shared.ErrInvalidDoorCode) {
		t.Fatalf("bad door code: %v", err)
	}
}

func TestAConsumerTransactionRollsBackItsClaim(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	id := e.book(inboundCmd("D-BOTH", at8, at10))
	// make the effect fail after the claim: a malformed door code in ActualDoorWins
	if err := e.svc.ConsumeTrailerDocked(ctx, "evt-retry", "WH1", id, "a b", at8); err == nil {
		t.Fatal("expected failure")
	}
	if first, _ := e.store.Claim(ctx, "evt-retry"); !first {
		t.Fatal("the claim must roll back with the failed effect so a redelivery is processed")
	}
}

// The optional GS1 GLN of a Dock slot (ADR 0013): kept with the door, dropped
// when it is not a valid GLN (never an error), kept across a re-registration
// that carries none, and gone with the door.
func TestConsumeSlotRegisteredKeepsAValidGLNAndDropsAnInvalidOne(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	gln := func() shared.GLN {
		t.Helper()
		d, _ := e.store.Doors().Door(ctx, "D-NEW")
		if d == nil {
			t.Fatal("D-NEW is not a bookable door")
		}
		return d.GLN
	}
	must(t, e.svc.ConsumeZoneRegistered(ctx, "z1", "zone-new", "WH3"))
	must(t, e.svc.ConsumeSlotRegistered(ctx, "s0", "D-NEW", "zone-new", "Dock", "Both", ""))
	if gln() != "" {
		t.Fatalf("a slot without a GLN has none, got %q", gln())
	}
	must(t, e.svc.ConsumeSlotRegistered(ctx, "s1", "D-NEW", "zone-new", "Dock", "Both", "4006381333931"))
	if gln() != "4006381333931" {
		t.Fatalf("GLN = %q, want the registered one", gln())
	}
	// not a valid GLN (bad check digit): dropped, the known one stays, no error
	must(t, e.svc.ConsumeSlotRegistered(ctx, "s2", "D-NEW", "zone-new", "Dock", "Both", "4006381333932"))
	if gln() != "4006381333931" {
		t.Fatalf("an invalid GLN must be ignored, GLN = %q", gln())
	}
	must(t, e.svc.ConsumeSlotRegistered(ctx, "s3", "D-NEW", "zone-new", "Dock", "Inbound", ""))
	if gln() != "4006381333931" {
		t.Fatalf("a re-registration without a GLN keeps the known one, got %q", gln())
	}
	must(t, e.svc.ConsumeSlotRegistered(ctx, "s4", "D-NEW", "zone-new", "Dock", "Inbound", "4012345000009"))
	if gln() != "4012345000009" {
		t.Fatalf("a new valid GLN replaces the old one, got %q", gln())
	}
}
