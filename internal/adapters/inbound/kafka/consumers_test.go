package kafka

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	kafkago "github.com/segmentio/kafka-go"

	"github.com/claudioed/dock-scheduling/internal/application/repository"
	"github.com/claudioed/dock-scheduling/internal/application/usecases"
	"github.com/claudioed/dock-scheduling/internal/domain/dockappointment"
	"github.com/claudioed/dock-scheduling/internal/domain/shared"
)

func mustDoor(t *testing.T, code string) shared.DoorCode {
	t.Helper()
	d, err := shared.NewDoorCode(code)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// inboundBooking is a later-in-the-day INBOUND booking command on door.
func inboundBooking(door string, asns ...string) usecases.BookCommand {
	if len(asns) == 0 {
		asns = []string{fixtureASN}
	}
	start := bookedStart.Add(6 * time.Hour)
	return usecases.BookCommand{
		DoorCode: door, Direction: "INBOUND", Carrier: "Acme Freight", CarrierSCAC: "ACME",
		WindowStart: start, WindowEnd: start.Add(time.Hour), ASNNumbers: asns,
	}
}

// blockingReader fetches nothing until its context ends.
type blockingReader struct{ closed bool }

func (b *blockingReader) FetchMessage(ctx context.Context) (kafkago.Message, error) {
	<-ctx.Done()
	return kafkago.Message{}, ctx.Err()
}
func (b *blockingReader) CommitMessages(context.Context, ...kafkago.Message) error { return nil }
func (b *blockingReader) Close() error                                             { b.closed = true; return nil }

func assertPoison(t *testing.T, err error) {
	t.Helper()
	var p *deadLetterError
	if !errors.As(err, &p) {
		t.Fatalf("error = %v, want a dead-letter marker", err)
	}
}

// ---- yard-management -------------------------------------------------------

func TestYard_VisitCheckedInRecordsTheArrivalFromTheGate(t *testing.T) {
	w := newWorld(t)
	w.seedBooked()
	w.handle(w.yard(), fixture(t, "yard_VisitCheckedIn"))

	s := w.wantState(fixtureAppointment, "CHECKED_IN")
	if s.Arrival != "ON_TIME" || s.Implicit {
		t.Fatalf("arrival = %+v, want an explicit ON_TIME arrival (14:02 in a 14:00 window)", s)
	}
}

func TestYard_TheWholeVisitCheckedInDockedUndockedSequence(t *testing.T) {
	w := newWorld(t)
	w.seedBooked()
	yard := w.yard()

	w.handle(yard, fixture(t, "yard_VisitCheckedIn"))
	// the trailer docked at D02 although D01 was booked: the physical fact wins
	w.handle(yard, withData(t, "yard_TrailerDocked", eventIDFor(10), map[string]any{"door_code": "WH1-DOCK-D02"}))
	if s := w.wantState(fixtureAppointment, "CHECKED_IN"); s.Door != "WH1-DOCK-D02" {
		t.Fatalf("door = %s, want the actual door WH1-DOCK-D02", s.Door)
	}
	w.handle(yard, fixture(t, "yard_TrailerUndocked"))
	w.wantState(fixtureAppointment, "COMPLETED")
}

func TestYard_TrailerDockedAtTheBookedDoorChangesNothing(t *testing.T) {
	w := newWorld(t)
	w.seedBooked()
	w.handle(w.yard(), fixture(t, "yard_TrailerDocked"))
	if s := w.wantState(fixtureAppointment, "BOOKED"); s.Door != fixtureDoor || s.Version != 1 {
		t.Fatalf("appointment = %+v, want it untouched", s)
	}
}

func TestYard_VisitRejectedCancelsABookedAppointmentAsTurnedAway(t *testing.T) {
	w := newWorld(t)
	w.seedBooked()
	yard := w.yard()

	// the example is a walk-in (appointment_id null): nothing to cancel
	w.handle(yard, fixture(t, "yard_VisitRejected"))
	w.wantState(fixtureAppointment, "BOOKED")

	w.handle(yard, withData(t, "yard_VisitRejected", eventIDFor(11), map[string]any{"appointment_id": fixtureAppointment, "site_id": fixtureSite}))
	if s := w.wantState(fixtureAppointment, "CANCELLED"); s.Reason != "TURNED_AWAY" {
		t.Fatalf("reason = %s, want TURNED_AWAY", s.Reason)
	}
}

func TestYard_VisitRejectedForANonBookedAppointmentIsIgnoredAndCounted(t *testing.T) {
	w := newWorld(t)
	w.seedBooked()
	yard := w.yard()
	w.handle(yard, fixture(t, "yard_VisitCheckedIn"))

	w.handle(yard, withData(t, "yard_VisitRejected", eventIDFor(12), map[string]any{"appointment_id": fixtureAppointment, "site_id": fixtureSite}))

	w.wantState(fixtureAppointment, "CHECKED_IN")
	if n := w.metrics.Ignored("already_applied") + w.metrics.Ignored("terminal_appointment"); n != 1 {
		t.Fatalf("ignored facts = %d, want the rejection counted once", n)
	}
}

func TestYard_OutOfOrder_ReleaseBeforeArrivalCompletesWithAnImplicitArrival(t *testing.T) {
	w := newWorld(t)
	w.seedBooked()
	yard := w.yard()

	// partitions deliver the release before the gate fact
	w.handle(yard, fixture(t, "yard_TrailerUndocked"))
	s := w.wantState(fixtureAppointment, "COMPLETED")
	if !s.Implicit || s.Arrival != "ON_TIME" || w.metrics.Implicit() != 1 {
		t.Fatalf("arrival = %+v implicit counter = %d, want one implicit ON_TIME arrival", s, w.metrics.Implicit())
	}
	// the late VisitCheckedIn for a COMPLETED appointment is ignored and counted
	w.handle(yard, fixture(t, "yard_VisitCheckedIn"))
	w.wantState(fixtureAppointment, "COMPLETED")
	if w.metrics.Ignored("terminal_appointment") != 1 {
		t.Fatalf("terminal facts ignored = %d, want 1", w.metrics.Ignored("terminal_appointment"))
	}
}

func TestYard_FactsForACancelledOrUnknownAppointmentAreIgnoredAndCounted(t *testing.T) {
	w := newWorld(t)
	w.seedBooked()
	yard := w.yard()
	w.handle(yard, withData(t, "yard_VisitRejected", eventIDFor(13), map[string]any{"appointment_id": fixtureAppointment, "site_id": fixtureSite}))

	for i, name := range []string{"yard_VisitCheckedIn", "yard_TrailerDocked", "yard_TrailerUndocked"} {
		w.handle(yard, withID(t, name, eventIDFor(20+i)))
	}
	w.wantState(fixtureAppointment, "CANCELLED")
	if got := w.metrics.Ignored("terminal_appointment"); got != 3 {
		t.Fatalf("terminal facts ignored = %d, want the check-in, the docking and the release counted", got)
	}

	unknown := "appt-ffffffff-ffff-4fff-8fff-ffffffffffff"
	w.handle(yard, withData(t, "yard_VisitCheckedIn", eventIDFor(40), map[string]any{"appointment_id": unknown}))
	if w.metrics.Ignored("unknown_appointment") != 1 {
		t.Fatalf("unknown appointment facts ignored = %d, want 1", w.metrics.Ignored("unknown_appointment"))
	}
}

func TestYard_ASiteMismatchIsIgnoredAndCounted(t *testing.T) {
	w := newWorld(t)
	w.seedBooked()
	w.handle(w.yard(), withData(t, "yard_VisitCheckedIn", eventIDFor(41), map[string]any{"site_id": "WH9"}))
	w.wantState(fixtureAppointment, "BOOKED")
	if w.metrics.Ignored("site_mismatch") != 1 {
		t.Fatalf("site mismatches ignored = %d, want 1", w.metrics.Ignored("site_mismatch"))
	}
}

func TestYard_ARedeliveredEventIdIsAppliedOnce(t *testing.T) {
	w := newWorld(t)
	w.seedBooked()
	yard := w.yard()
	w.handle(yard, fixture(t, "yard_VisitCheckedIn"))
	// the same CloudEvents id again, with another payload: deduped, not applied
	w.handle(yard, withData(t, "yard_VisitCheckedIn", "7f3c9a52-1b8d-4e6f-9a07-000000000001", map[string]any{"site_id": "WH9"}))
	w.wantState(fixtureAppointment, "CHECKED_IN")
	if w.metrics.Ignored("site_mismatch") != 0 {
		t.Fatal("the replayed id was applied a second time")
	}
}

func TestYard_ExtraFieldsAreIgnoredAndTimeFallsBackToTheEnvelope(t *testing.T) {
	w := newWorld(t)
	w.seedBooked()
	raw := variant(t, "yard_VisitCheckedIn", func(env, data map[string]any) {
		data["a_future_field"] = map[string]any{"nested": true}
		delete(data, "arrived_at")
		env["time"] = "2026-11-05T13:20:00Z" // 40 minutes early
	})
	w.handle(w.yard(), raw)
	if s := w.wantState(fixtureAppointment, "CHECKED_IN"); s.Arrival != "EARLY" {
		t.Fatalf("arrival = %s, want EARLY from the envelope time", s.Arrival)
	}
}

func TestYard_UnknownTypesOfTheTopicAreIgnored(t *testing.T) {
	w := newWorld(t)
	w.seedBooked()
	yard := w.yard()
	for _, typ := range []string{
		"com.warehouse.wms.yard-management.trailer.TrailerParked",
		"com.warehouse.wms.yard-management.yardvisit.VisitCheckedIn.v2", // a full-type match, never a prefix
		"VisitCheckedIn", // never a short name
	} {
		w.handle(yard, typeOf(t, "yard_VisitCheckedIn", typ))
	}
	w.wantState(fixtureAppointment, "BOOKED")
}

func TestYard_MalformedPayloadsArePoison(t *testing.T) {
	w := newWorld(t)
	w.seedBooked()
	yard := w.yard()
	cases := map[string][]byte{
		"data is not an object":     variant(t, "yard_VisitCheckedIn", func(env, _ map[string]any) { env["data"] = "garbage" }),
		"blank visit id":            withData(t, "yard_VisitCheckedIn", eventIDFor(50), map[string]any{"visit_id": " "}),
		"blank site id":             withData(t, "yard_VisitRejected", eventIDFor(51), map[string]any{"site_id": ""}),
		"unparsable arrived_at":     withData(t, "yard_VisitCheckedIn", eventIDFor(52), map[string]any{"arrived_at": "yesterday"}),
		"docked without a door":     withData(t, "yard_TrailerDocked", eventIDFor(53), map[string]any{"door_code": ""}),
		"docked at an invalid door": withData(t, "yard_TrailerDocked", eventIDFor(54), map[string]any{"door_code": "WH1/DOCK"}),
		"undocked unparsable time":  withData(t, "yard_TrailerUndocked", eventIDFor(55), map[string]any{"undocked_at": "x"}),
		"undocked without trailer":  withData(t, "yard_TrailerUndocked", eventIDFor(56), map[string]any{"trailer_id": ""}),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) { assertPoison(t, yard.HandleMessage(context.Background(), raw)) })
	}
}

// ---- facility-layout -------------------------------------------------------

func TestFacility_ZoneThenDockSlotMakesTheDoorBookable(t *testing.T) {
	w := newWorld(t)
	fac := w.facility()
	w.handle(fac, fixture(t, "facility_ZoneRegistered"))
	w.handle(fac, fixture(t, "facility_LocationSlotRegistered"))

	door, err := w.store.Doors().Door(context.Background(), mustDoor(t, fixtureDoor))
	if err != nil || door == nil || door.Site != fixtureSite || door.Flow != shared.FlowBoth {
		t.Fatalf("door = %+v err=%v, want WH1 / Both", door, err)
	}
}

func TestFacility_X5_ASlotBeforeItsZoneIsPendingUntilTheZoneArrives(t *testing.T) {
	w := newWorld(t)
	fac := w.facility()
	w.handle(fac, fixture(t, "facility_LocationSlotRegistered"))
	if door, _ := w.store.Doors().Door(context.Background(), mustDoor(t, fixtureDoor)); door != nil {
		t.Fatalf("a door whose zone has not arrived is bookable: %+v", door)
	}
	w.handle(fac, fixture(t, "facility_ZoneRegistered"))
	if door, _ := w.store.Doors().Door(context.Background(), mustDoor(t, fixtureDoor)); door == nil || door.Site != fixtureSite {
		t.Fatalf("door = %+v, want it resolved to WH1 once the zone arrived", door)
	}
}

func TestFacility_NonDockRolesAreNotDoorsAndAbsentRoleIsStorage(t *testing.T) {
	w := newWorld(t)
	fac := w.facility()
	w.handle(fac, fixture(t, "facility_ZoneRegistered"))
	for _, name := range []string{"facility_LocationSlotRegistered_storage", "facility_LocationSlotRegistered_norole"} {
		w.handle(fac, fixture(t, name))
	}
	for _, code := range []string{"WH1-STOR-AMB-A07-03-02-B", "WH1-OLD-01"} {
		if door, _ := w.store.Doors().Door(context.Background(), mustDoor(t, code)); door != nil {
			t.Fatalf("%s is a door: %+v", code, door)
		}
	}
	// a re-registration of a door with another role removes it
	w.handle(fac, fixture(t, "facility_LocationSlotRegistered"))
	w.handle(fac, withData(t, "facility_LocationSlotRegistered", eventIDFor(60), map[string]any{"role": "Yard"}))
	if door, _ := w.store.Doors().Door(context.Background(), mustDoor(t, fixtureDoor)); door != nil {
		t.Fatalf("a door re-registered as Yard is still bookable: %+v", door)
	}
}

func TestFacility_DecommissionBlocksNewBookingsButKeepsExistingAppointments(t *testing.T) {
	w := newWorld(t)
	w.seedBooked()
	w.handle(w.facility(), fixture(t, "facility_LocationSlotDecommissioned"))
	w.wantState(fixtureAppointment, "BOOKED") // D-7: existing appointments untouched
	w.ids.ids = []string{"appt-bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"}
	_, err := w.svc.BookAppointment(context.Background(), inboundBooking(fixtureDoor))
	if !errors.Is(err, dockappointment.ErrUnknownDoor) {
		t.Fatalf("booking a decommissioned door = %v, want ErrUnknownDoor", err)
	}
}

func TestFacility_TolerantOfSnakeCaseAndExtraFields(t *testing.T) {
	w := newWorld(t)
	fac := w.facility()
	w.handle(fac, variant(t, "facility_ZoneRegistered", func(_, data map[string]any) {
		data["zone_id"], data["site_code"] = data["zoneId"], data["siteCode"]
		delete(data, "zoneId")
		delete(data, "siteCode")
		data["somethingNew"] = 1
	}))
	w.handle(fac, variant(t, "facility_LocationSlotRegistered", func(_, data map[string]any) {
		data["location_code"], data["zone_id"], data["dock_flow"], data["role"] = data["locationCode"], data["zoneId"], data["dockFlow"], "dock"
		delete(data, "locationCode")
		delete(data, "zoneId")
		delete(data, "dockFlow")
	}))
	if door, _ := w.store.Doors().Door(context.Background(), mustDoor(t, fixtureDoor)); door == nil || door.Site != fixtureSite {
		t.Fatalf("door = %+v, want the snake_case spelling and a lower-case role accepted", door)
	}
}

func TestFacility_ARedeliveredEventIdIsAppliedOnce(t *testing.T) {
	w := newWorld(t)
	fac := w.facility()
	w.handle(fac, fixture(t, "facility_ZoneRegistered"))
	w.handle(fac, withData(t, "facility_ZoneRegistered", "3b0d6c1e-8f2a-4d57-9c41-5a7e2b9d0c01", map[string]any{"siteCode": "WH-CHANGED"}))
	w.handle(fac, fixture(t, "facility_LocationSlotRegistered"))
	if door, _ := w.store.Doors().Door(context.Background(), mustDoor(t, fixtureDoor)); door == nil || door.Site != fixtureSite {
		t.Fatalf("door = %+v: the replayed zone id must not apply", door)
	}
}

func TestFacility_MalformedPayloadsArePoison(t *testing.T) {
	fac := newWorld(t).facility()
	cases := map[string][]byte{
		"data is an array":            variant(t, "facility_ZoneRegistered", func(env, _ map[string]any) { env["data"] = []any{1} }),
		"zone without site":           withData(t, "facility_ZoneRegistered", eventIDFor(70), map[string]any{"siteCode": ""}),
		"zone without id":             withData(t, "facility_ZoneRegistered", eventIDFor(71), map[string]any{"zoneId": ""}),
		"slot without code":           withData(t, "facility_LocationSlotRegistered", eventIDFor(72), map[string]any{"locationCode": ""}),
		"dock without zone":           withData(t, "facility_LocationSlotRegistered", eventIDFor(73), map[string]any{"zoneId": ""}),
		"dock with an unknown flow":   withData(t, "facility_LocationSlotRegistered", eventIDFor(74), map[string]any{"dockFlow": "Sideways"}),
		"dock without a flow":         withData(t, "facility_LocationSlotRegistered", eventIDFor(75), map[string]any{"dockFlow": nil}),
		"decommission without a code": withData(t, "facility_LocationSlotDecommissioned", eventIDFor(76), map[string]any{"locationCode": ""}),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) { assertPoison(t, fac.HandleMessage(context.Background(), raw)) })
	}
}

// ---- inbound-receiving ASNs ------------------------------------------------

func TestAsn_CancelledCancelsTheAppointmentWhenAllItsAsnsAreCancelled(t *testing.T) {
	w := newWorld(t)
	w.seedBooked()
	asn := w.asn()
	w.handle(asn, fixture(t, "inbound_ASNCancelled"))

	if s := w.wantState(fixtureAppointment, "CANCELLED"); s.Reason != "ASN_CANCELLED" {
		t.Fatalf("reason = %s, want ASN_CANCELLED", s.Reason)
	}
	st, _ := w.store.Asns().Statuses(context.Background(), []string{fixtureASN})
	if st[fixtureASN] != dockappointment.AsnCancelledStatus {
		t.Fatalf("asn status = %v, want cancelled", st[fixtureASN])
	}
	// a late or replayed registration never revives a cancelled ASN
	w.handle(asn, withID(t, "inbound_ASNRegistered", eventIDFor(80)))
	st, _ = w.store.Asns().Statuses(context.Background(), []string{fixtureASN})
	if st[fixtureASN] != dockappointment.AsnCancelledStatus {
		t.Fatalf("asn status after a late registration = %v, want still cancelled", st[fixtureASN])
	}
}

func TestAsn_AnAppointmentWithTwoAsnsIsCancelledOnlyWhenBothAreCancelled(t *testing.T) {
	w := newWorld(t)
	w.seedBooked()
	asn := w.asn()
	for i, n := range []string{"ASN-1002", "ASN-1003"} {
		w.handle(asn, variant(t, "inbound_ASNRegistered", func(env, data map[string]any) {
			env["id"], data["asn_number"] = eventIDFor(81+i), n
		}))
	}
	const two = "appt-cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	w.ids.ids = []string{two}
	if _, err := w.svc.BookAppointment(context.Background(), inboundBooking("WH1-DOCK-D02", "ASN-1002", "ASN-1003")); err != nil {
		t.Fatal(err)
	}

	w.handle(asn, withData(t, "inbound_ASNCancelled", eventIDFor(83), map[string]any{"asn_number": "ASN-1002"}))
	w.wantState(two, "BOOKED")
	w.handle(asn, withData(t, "inbound_ASNCancelled", eventIDFor(84), map[string]any{"asn_number": "ASN-1003"}))
	if s := w.wantState(two, "CANCELLED"); s.Reason != "ASN_CANCELLED" {
		t.Fatalf("reason = %s, want ASN_CANCELLED", s.Reason)
	}
	w.wantState(fixtureAppointment, "BOOKED") // another ASN, another appointment
}

func TestAsn_ReplayedAndUnknownTypesAreHarmless(t *testing.T) {
	w := newWorld(t)
	w.seedBooked()
	asn := w.asn()
	w.handle(asn, fixture(t, "inbound_ASNCancelled"))
	w.handle(asn, fixture(t, "inbound_ASNCancelled")) // the same id again
	w.handle(asn, typeOf(t, "inbound_ASNCancelled", "com.warehouse.wms.inbound-receiving.asn.ASNClosed"))
	w.wantState(fixtureAppointment, "CANCELLED")
}

func TestAsn_MalformedPayloadsArePoison(t *testing.T) {
	asn := newWorld(t).asn()
	cases := map[string][]byte{
		"data is a number":         variant(t, "inbound_ASNRegistered", func(env, _ map[string]any) { env["data"] = 7 }),
		"registered without asn":   withData(t, "inbound_ASNRegistered", eventIDFor(90), map[string]any{"asn_number": ""}),
		"unparsable expected date": withData(t, "inbound_ASNRegistered", eventIDFor(91), map[string]any{"expected_arrival": "tomorrow"}),
		"cancelled without asn":    withData(t, "inbound_ASNCancelled", eventIDFor(92), map[string]any{"asn_number": ""}),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) { assertPoison(t, asn.HandleMessage(context.Background(), raw)) })
	}
}

// ---- process-path-management cutoffs ---------------------------------------

func TestCutoff_StoresTheWholeScheduleAndReplacesIt(t *testing.T) {
	w := newWorld(t)
	cut := w.cutoff()
	w.handle(cut, fixture(t, "ppm_CPTScheduleChanged"))
	known, err := w.store.Cutoffs().Known(context.Background(), "sp1", []string{"sp1-1500", "sp1-1800"})
	if err != nil || !known["sp1-1500"] || known["sp1-1800"] {
		t.Fatalf("known = %v err=%v, want sp1-1500 only", known, err)
	}
	w.handle(cut, variant(t, "ppm_CPTScheduleChanged", func(env, data map[string]any) {
		env["id"] = eventIDFor(100)
		data["cutoffs"] = []any{map[string]any{"cpt_id": "sp1-1800", "local_time": "18:00", "days_of_week": []any{"Mon"}, "ship_method": "air"}}
	}))
	known, _ = w.store.Cutoffs().Known(context.Background(), "sp1", []string{"sp1-1500", "sp1-1800"})
	if known["sp1-1500"] || !known["sp1-1800"] {
		t.Fatalf("known = %v, want the schedule replaced as a whole", known)
	}
	w.handle(cut, withData(t, "ppm_CPTScheduleChanged", eventIDFor(101), map[string]any{"cutoffs": []any{}}))
	known, _ = w.store.Cutoffs().Known(context.Background(), "sp1", []string{"sp1-1800"})
	if known["sp1-1800"] {
		t.Fatalf("known = %v, want an empty schedule to clear the site", known)
	}
}

func TestCutoff_StoresOnlyAndCreatesNoAppointment(t *testing.T) {
	w := newWorld(t)
	w.handle(w.cutoff(), fixture(t, "ppm_CPTScheduleChanged"))
	page, err := w.svc.ListAppointments(context.Background(), repository.AppointmentFilter{}, 50, "")
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("appointments = %v err=%v: PickupFromCutoffs is phase 2", page.Items, err)
	}
}

func TestCutoff_MalformedPayloadsArePoison(t *testing.T) {
	cut := newWorld(t).cutoff()
	cases := map[string][]byte{
		"data is a string":   variant(t, "ppm_CPTScheduleChanged", func(env, _ map[string]any) { env["data"] = "x" }),
		"no site":            withData(t, "ppm_CPTScheduleChanged", eventIDFor(110), map[string]any{"site_id": ""}),
		"cutoff without id":  withData(t, "ppm_CPTScheduleChanged", eventIDFor(111), map[string]any{"cutoffs": []any{map[string]any{"local_time": "15:00"}}}),
		"cutoffs wrong type": withData(t, "ppm_CPTScheduleChanged", eventIDFor(112), map[string]any{"cutoffs": "all"}),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) { assertPoison(t, cut.HandleMessage(context.Background(), raw)) })
	}
}

// ---- every consumer: the retired flat envelope ------------------------------

func TestEveryConsumer_RejectsTheRetiredFlatEnvelopeWithoutHandling(t *testing.T) {
	w := newWorld(t)
	flat := []byte(`{"event_id":"e-flat","event_type":"TrailerDocked","occurred_at":"2026-11-05T14:10:00Z","trailer_id":"t","site_id":"WH1","door_code":"D"}`)
	for _, c := range []*Consumer{w.yard(), w.facility(), w.asn(), w.cutoff()} {
		if err := c.HandleMessage(context.Background(), flat); err != nil {
			t.Fatalf("%s: %v, want the flat message skipped (WARN, commit past)", c.Name, err)
		}
	}
}

// ---- constructors, groups, wiring -------------------------------------------

func TestConstructors_ABlankGroupIsABootErrorNamingTheVariable(t *testing.T) {
	w := newWorld(t)
	for env, build := range map[string]func(string) (*Consumer, error){
		EnvYardGroup:     func(g string) (*Consumer, error) { return NewYardConsumer(w.tr, g, w.svc) },
		EnvFacilityGroup: func(g string) (*Consumer, error) { return NewFacilityConsumer(w.tr, g, w.svc) },
		EnvAsnGroup:      func(g string) (*Consumer, error) { return NewAsnConsumer(w.tr, g, w.svc) },
		EnvCptGroup:      func(g string) (*Consumer, error) { return NewCutoffConsumer(w.tr, g, w.svc) },
	} {
		for _, group := range []string{"", "   "} {
			c, err := build(group)
			if c != nil || !errors.Is(err, ErrNoConsumerGroup) || !strings.Contains(err.Error(), env) {
				t.Fatalf("group %q: consumer=%v err=%v, want ErrNoConsumerGroup naming %s", group, c, err, env)
			}
		}
	}
	if len(w.ft.asked) != 0 {
		t.Fatalf("readers were built for a consumer without a group: %v", w.ft.asked)
	}
}

func TestConstructors_ARequiredReaderFactoryIsChecked(t *testing.T) {
	w := newWorld(t)
	if _, err := NewYardConsumer(Transport{}, "g", w.svc); err == nil {
		t.Fatal("a consumer without a reader factory was built")
	}
}

func TestConstructors_ReadTheDocumentedTopicsUnderTheirGroups(t *testing.T) {
	w := newWorld(t)
	cs, err := Build(w.tr, Groups{Yard: "y", Facility: "f", Asn: "a", Cpt: "c"}, w.svc)
	if err != nil {
		t.Fatal(err)
	}
	want := [][2]string{
		{"warehouse.yard-management.events", "y"}, {"warehouse.facility.events", "f"},
		{"warehouse.inbound-receiving.events", "a"}, {"warehouse.process-path-management.events", "c"},
	}
	if len(cs) != 4 || !slices.Equal(w.ft.asked, want) {
		t.Fatalf("consumers=%d readers=%v, want %v", len(cs), w.ft.asked, want)
	}
}

func TestBuild_FailsOnABlankMandatoryGroupAndClosesWhatItBuilt(t *testing.T) {
	w := newWorld(t)
	_, err := Build(w.tr, Groups{Yard: "y", Facility: "f", Asn: ""}, w.svc)
	if !errors.Is(err, ErrNoConsumerGroup) || !strings.Contains(err.Error(), EnvAsnGroup) {
		t.Fatalf("Build = %v, want ErrNoConsumerGroup naming %s", err, EnvAsnGroup)
	}
	if len(w.ft.readers) != 2 || !w.ft.readers[0].closed || !w.ft.readers[1].closed {
		t.Fatal("the consumers built before the failure were not closed")
	}
}

func TestGroupsFromEnv(t *testing.T) {
	env := map[string]string{EnvYardGroup: " y ", EnvFacilityGroup: "f", EnvAsnGroup: "a", EnvCptGroup: "c"}
	g, err := GroupsFromEnv(func(k string) string { return env[k] })
	if err != nil || g != (Groups{Yard: "y", Facility: "f", Asn: "a", Cpt: "c"}) {
		t.Fatalf("groups = %+v err=%v", g, err)
	}
	delete(env, EnvYardGroup)
	delete(env, EnvCptGroup)
	_, err = GroupsFromEnv(func(k string) string { return env[k] })
	if !errors.Is(err, ErrNoConsumerGroup) || !strings.Contains(err.Error(), EnvYardGroup) || !strings.Contains(err.Error(), EnvCptGroup) {
		t.Fatalf("err = %v, want both missing variables named", err)
	}
}

func TestStart_RunsTheConsumerAndTheFirstToStopEndsTheSet(t *testing.T) {
	w := newWorld(t)
	w.seedBooked()
	w.ft.queue = [][]byte{fixture(t, "yard_VisitCheckedIn")}
	yard := w.yard()
	run := Start(context.Background(), nil, yard)
	if err := run.Wait(); !errors.Is(err, errDrained) {
		t.Fatalf("Wait = %v, want the drained reader's error", err)
	}
	w.wantState(fixtureAppointment, "CHECKED_IN")
	if !w.ft.readers[len(w.ft.readers)-1].closed || !w.ft.dlq.closed {
		t.Fatal("the consumer was not closed after it stopped")
	}
}

func TestStop_CancelsAndWaits(t *testing.T) {
	blocked := &blockingReader{}
	run := Start(context.Background(), &Consumer{Name: "blocked", Reader: blocked})
	time.Sleep(10 * time.Millisecond)
	if err := run.Stop(); err != nil {
		t.Fatalf("Stop = %v, want nil (cancellation is not an error)", err)
	}
	if !blocked.closed {
		t.Fatal("the reader was not closed")
	}
}
