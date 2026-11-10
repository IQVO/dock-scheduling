//go:build integration

package kafka

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/claudioed/dock-scheduling/internal/domain/dockappointment"
	"github.com/claudioed/dock-scheduling/internal/testing/kafkatest"
)

// assertDeadLetter reads the single message of dlq and checks its fleet headers.
func assertDeadLetter(t *testing.T, dlq, sourceTopic, wantID string) {
	t.Helper()
	dead := kafkatest.ReadN(t, dlq, 1, 90*time.Second)
	if want := `"id":"` + wantID + `"`; !strings.Contains(string(dead[0].Value), want) {
		t.Fatalf("dead letter = %s, want the poison occurrence %s", dead[0].Value, wantID)
	}
	if dead[0].Headers["x-dlq-source-topic"] != sourceTopic || dead[0].Headers["x-dlq-cause"] == "" {
		t.Fatalf("dead letter headers = %v, want the source topic and the cause", dead[0].Headers)
	}
	if n := kafkatest.EndOffset(t, dlq); n != 1 {
		t.Fatalf("dead letters = %d, want exactly the poison message", n)
	}
}

// TestYardConsumer_RealKafkaAndPostgres: the whole yard contract on a real
// broker and a real database. In order: the gate fact, a replay of its id with
// ANOTHER payload (deduped), an unknown type, a flat legacy message, a poison
// payload (dead-lettered), the docking at another door (the physical fact
// wins) and the door release.
func TestYardConsumer_RealKafkaAndPostgres(t *testing.T) {
	w := newITWorld(t)
	w.seedBooked()
	topic, dlq := w.topics(YardTopic)
	claimsBefore := w.count("processed_events")

	poison := variant(t, "yard_VisitCheckedIn", func(env, _ map[string]any) {
		env["id"], env["data"] = eventIDFor(1), "garbage"
	})
	w.produce(topic,
		fixture(t, "yard_VisitCheckedIn"),
		withData(t, "yard_VisitCheckedIn", "7f3c9a52-1b8d-4e6f-9a07-000000000001", map[string]any{"site_id": "WH9"}),
		typeOf(t, "yard_VisitCheckedIn", "com.warehouse.wms.yard-management.trailer.TrailerParked"),
		flatEnvelope,
		poison,
		withData(t, "yard_TrailerDocked", eventIDFor(2), map[string]any{"door_code": "WH1-DOCK-D02"}),
		fixture(t, "yard_TrailerUndocked"),
	)
	group := w.group("yard")
	c, err := NewYardConsumerForTopic(w.tr, topic, group, w.svc)
	if err != nil {
		t.Fatal(err)
	}
	w.run(c)
	w.committedPast(group, topic, 7)

	s := w.appt(fixtureAppointment)
	if s.State != "COMPLETED" || s.Door != "WH1-DOCK-D02" || s.Arrival != "ON_TIME" || s.Implicit {
		t.Fatalf("appointment = %+v, want COMPLETED at the actual door after an explicit ON_TIME arrival", s)
	}
	// check-in, docking and release claimed; the replay, the unknown type, the
	// flat message and the poison claim nothing
	if got := w.count("processed_events") - claimsBefore; got != 3 {
		t.Fatalf("claims = %d, want 3", got)
	}
	if w.metrics.Ignored("site_mismatch") != 0 {
		t.Fatal("the replayed id with another payload was applied")
	}
	assertDeadLetter(t, dlq, topic, eventIDFor(1))
}

// TestYardConsumer_OutOfOrderThenReplayUnderANewGroup: partitions deliver the
// release first, then the late gate fact, then the docking; and a second group
// reading the topic from the start (a redelivery of everything) changes nothing.
func TestYardConsumer_OutOfOrderThenReplayUnderANewGroup(t *testing.T) {
	w := newITWorld(t)
	w.seedBooked()
	topic, _ := w.topics(YardTopic)
	w.produce(topic,
		fixture(t, "yard_TrailerUndocked"),
		fixture(t, "yard_VisitCheckedIn"),
		withData(t, "yard_TrailerDocked", eventIDFor(3), map[string]any{"door_code": "WH1-DOCK-D02"}),
	)

	first := w.group("yard-a")
	c, err := NewYardConsumerForTopic(w.tr, topic, first, w.svc)
	if err != nil {
		t.Fatal(err)
	}
	w.run(c)
	w.committedPast(first, topic, 3)

	s := w.appt(fixtureAppointment)
	if s.State != "COMPLETED" || !s.Implicit || s.Door != fixtureDoor || w.metrics.Implicit() != 1 {
		t.Fatalf("appointment = %+v implicit=%d, want COMPLETED by an implicit arrival on the booked door", s, w.metrics.Implicit())
	}
	if w.metrics.Ignored("terminal_appointment") != 2 {
		t.Fatalf("terminal facts ignored = %d, want the late check-in and the docking", w.metrics.Ignored("terminal_appointment"))
	}
	claims, outbox, ignored := w.count("processed_events"), w.count("outbox"), w.metrics.Ignored("terminal_appointment")

	second := w.group("yard-b")
	c2, err := NewYardConsumerForTopic(w.tr, topic, second, w.svc)
	if err != nil {
		t.Fatal(err)
	}
	w.run(c2)
	w.committedPast(second, topic, 3)
	if w.count("processed_events") != claims || w.count("outbox") != outbox || w.metrics.Ignored("terminal_appointment") != ignored {
		t.Fatal("the replay under a new group changed rows or counters: the id claims must make it a no-op")
	}
	if got := w.appt(fixtureAppointment); got != s {
		t.Fatalf("appointment after the replay = %+v, want %+v", got, s)
	}
}

// TestYardConsumer_VisitRejectedCancelsTheBookedAppointment: CancelOnVisitRejected.
func TestYardConsumer_VisitRejectedCancelsTheBookedAppointment(t *testing.T) {
	w := newITWorld(t)
	w.seedBooked()
	topic, _ := w.topics(YardTopic)
	w.produce(topic, withData(t, "yard_VisitRejected", eventIDFor(4), map[string]any{"appointment_id": fixtureAppointment, "site_id": fixtureSite}))
	group := w.group("yard")
	c, err := NewYardConsumerForTopic(w.tr, topic, group, w.svc)
	if err != nil {
		t.Fatal(err)
	}
	w.run(c)
	w.committedPast(group, topic, 1)
	if s := w.appt(fixtureAppointment); s.State != "CANCELLED" || s.Reason != "TURNED_AWAY" {
		t.Fatalf("appointment = %+v, want CANCELLED / TURNED_AWAY", s)
	}
}

// flakyYard fails the first ConsumeVisitCheckedIn calls with a transient error.
type flakyYard struct {
	YardUseCases
	failures atomic.Int32
	calls    atomic.Int32
}

func (f *flakyYard) ConsumeVisitCheckedIn(ctx context.Context, eventID, visitID, siteID, appointmentID string, at time.Time) error {
	f.calls.Add(1)
	if f.failures.Add(-1) >= 0 {
		return errors.New("postgres is down")
	}
	return f.YardUseCases.ConsumeVisitCheckedIn(ctx, eventID, visitID, siteID, appointmentID, at)
}

// TestYardConsumer_ATransientFailureRedeliversTheSameMessage: the offset moves
// only after the effect is durable, so the failing message is retried, not lost.
func TestYardConsumer_ATransientFailureRedeliversTheSameMessage(t *testing.T) {
	w := newITWorld(t)
	w.seedBooked()
	topic, dlq := w.topics(YardTopic)
	w.produce(topic, fixture(t, "yard_VisitCheckedIn"))
	flaky := &flakyYard{YardUseCases: w.svc}
	flaky.failures.Store(2)
	group := w.group("yard")
	c, err := NewYardConsumerForTopic(w.tr, topic, group, flaky)
	if err != nil {
		t.Fatal(err)
	}
	w.run(c)
	w.committedPast(group, topic, 1)
	if flaky.calls.Load() != 3 || w.appt(fixtureAppointment).State != "CHECKED_IN" {
		t.Fatalf("calls=%d state=%s, want the third attempt to apply the fact", flaky.calls.Load(), w.appt(fixtureAppointment).State)
	}
	if kafkatest.EndOffset(t, dlq) != 0 {
		t.Fatal("a transient failure was dead-lettered")
	}
}

// TestTheClaimRollsBackWithTheEffect_OnRealPostgres: the fleet dedupe rule on
// a real database: a claim sharing a unit of work with a FAILING effect is
// un-claimed by the rollback, so the redelivery is processed, not skipped.
func TestTheClaimRollsBackWithTheEffect_OnRealPostgres(t *testing.T) {
	w := newITWorld(t)
	boom := errors.New("effect failed")
	err := w.uow.Do(w.ctx, func(ctx context.Context) error {
		if first, err := w.processed.Claim(ctx, "evt-rollback"); err != nil || !first {
			t.Fatalf("claim = %v err=%v", first, err)
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("uow = %v, want the effect error", err)
	}
	first, err := w.processed.Claim(w.ctx, "evt-rollback")
	if err != nil || !first {
		t.Fatalf("claim after the rollback = %v err=%v: the claim survived the rollback", first, err)
	}
}

// TestFacilityConsumer_X5_PendingUntilTheZoneArrives: a dock slot before its
// zone cannot be booked; the zone makes it bookable; a decommission blocks new
// bookings without touching the existing one; a poison payload is dead-lettered.
func TestFacilityConsumer_X5_PendingUntilTheZoneArrives(t *testing.T) {
	w := newITWorld(t)
	topic, dlq := w.topics(FacilityTopic)
	group := w.group("facility")
	c, err := NewFacilityConsumerForTopic(w.tr, topic, group, w.svc)
	if err != nil {
		t.Fatal(err)
	}
	w.run(c)
	if err := w.svc.ConsumeAsnRegistered(w.ctx, "seed-asn", fixtureASN, bookedStart); err != nil {
		t.Fatal(err)
	}

	w.produce(topic, fixture(t, "facility_LocationSlotRegistered"))
	w.committedPast(group, topic, 1)
	if w.count("doors") != 1 {
		t.Fatalf("doors = %d, want the slot held pending", w.count("doors"))
	}
	w.ids.ids = []string{fixtureAppointment}
	if _, err := w.svc.BookAppointment(w.ctx, inboundBooking(fixtureDoor)); !errors.Is(err, dockappointment.ErrUnknownDoor) {
		t.Fatalf("booking a door whose zone has not arrived = %v, want ErrUnknownDoor", err)
	}

	w.produce(topic, fixture(t, "facility_ZoneRegistered"), fixture(t, "facility_ZoneRegistered")) // the replay rides along
	w.committedPast(group, topic, 3)
	w.book(fixtureAppointment, fixtureDoor, fixtureASN)

	poison := withData(t, "facility_LocationSlotRegistered", eventIDFor(5), map[string]any{"locationCode": "WH1-DOCK-D09", "dockFlow": "Sideways"})
	w.produce(topic, flatEnvelope, typeOf(t, "facility_ZoneRegistered", "com.warehouse.wms.facility-layout.aisle.AisleRegistered"),
		poison, fixture(t, "facility_LocationSlotDecommissioned"))
	w.committedPast(group, topic, 7)

	if s := w.appt(fixtureAppointment); s.State != "BOOKED" {
		t.Fatalf("appointment = %+v, want it untouched by the decommission (D-7)", s)
	}
	w.ids.ids = []string{"appt-bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"}
	if _, err := w.svc.BookAppointment(w.ctx, inboundBooking(fixtureDoor)); !errors.Is(err, dockappointment.ErrUnknownDoor) {
		t.Fatalf("booking a decommissioned door = %v, want ErrUnknownDoor", err)
	}
	assertDeadLetter(t, dlq, topic, eventIDFor(5))
}

// TestAsnConsumer_RealKafkaAndPostgres: ASNCancelled cancels the appointment
// once (a redelivered id is a no-op), a poison payload is dead-lettered, a late
// ASNRegistered never revives a cancelled ASN.
func TestAsnConsumer_RealKafkaAndPostgres(t *testing.T) {
	w := newITWorld(t)
	w.seedBooked()
	topic, dlq := w.topics(InboundReceivingTopic)
	poison := withData(t, "inbound_ASNCancelled", eventIDFor(6), map[string]any{"asn_number": ""})
	w.produce(topic,
		fixture(t, "inbound_ASNCancelled"),
		fixture(t, "inbound_ASNCancelled"),
		poison,
		withID(t, "inbound_ASNRegistered", eventIDFor(7)),
	)
	group := w.group("asn")
	c, err := NewAsnConsumerForTopic(w.tr, topic, group, w.svc)
	if err != nil {
		t.Fatal(err)
	}
	w.run(c)
	w.committedPast(group, topic, 4)

	s := w.appt(fixtureAppointment)
	if s.State != "CANCELLED" || s.Reason != "ASN_CANCELLED" || s.Version != 2 {
		t.Fatalf("appointment = %+v, want CANCELLED / ASN_CANCELLED applied exactly once (version 2)", s)
	}
	var state string
	if err := w.pool.QueryRow(w.ctx, `SELECT status FROM asns WHERE asn_number = $1`, fixtureASN).Scan(&state); err != nil || state != "CANCELLED" {
		t.Fatalf("asn state = %q err=%v, want CANCELLED to stay sticky", state, err)
	}
	assertDeadLetter(t, dlq, topic, eventIDFor(6))
}

// TestCutoffConsumer_RealKafkaAndPostgres: the whole schedule of a site is
// replaced by each event; a replayed id and a poison payload change nothing.
func TestCutoffConsumer_RealKafkaAndPostgres(t *testing.T) {
	w := newITWorld(t)
	topic, dlq := w.topics(ProcessPathTopic)
	replace := variant(t, "ppm_CPTScheduleChanged", func(env, data map[string]any) {
		env["id"] = eventIDFor(8)
		data["cutoffs"] = []any{
			map[string]any{"cpt_id": "sp1-1200", "local_time": "12:00", "days_of_week": []any{"Mon", "Tue"}, "ship_method": "ground"},
			map[string]any{"cpt_id": "sp1-1800", "local_time": "18:00", "days_of_week": []any{"Fri"}, "ship_method": "air"},
		}
	})
	poison := withData(t, "ppm_CPTScheduleChanged", eventIDFor(9), map[string]any{"site_id": ""})
	w.produce(topic, fixture(t, "ppm_CPTScheduleChanged"), fixture(t, "ppm_CPTScheduleChanged"), replace, flatEnvelope, poison)
	group := w.group("cpt")
	c, err := NewCutoffConsumerForTopic(w.tr, topic, group, w.svc)
	if err != nil {
		t.Fatal(err)
	}
	w.run(c)
	w.committedPast(group, topic, 5)

	if n := w.count("cutoffs"); n != 2 {
		t.Fatalf("cutoffs = %d, want the two rows of the last schedule", n)
	}
	var old int
	if err := w.pool.QueryRow(w.ctx, `SELECT count(*) FROM cutoffs WHERE cpt_id = 'sp1-1500'`).Scan(&old); err != nil || old != 0 {
		t.Fatalf("stale cutoff rows = %d err=%v", old, err)
	}
	if w.count("dock_appointments") != 0 {
		t.Fatal("a cutoff schedule created an appointment (phase 2)")
	}
	assertDeadLetter(t, dlq, topic, eventIDFor(9))
}

// TestAllConsumersTogether_StartAndStop: the four consumers started as one set
// by Start, each on its own topic, reproduce the end-to-end chain (facility
// door, ASN, booking, gate, docking, release) and stop cleanly.
func TestAllConsumersTogether_StartAndStop(t *testing.T) {
	w := newITWorld(t)
	facilityTopic, _ := w.topics(FacilityTopic)
	asnTopic, _ := w.topics(InboundReceivingTopic)
	cptTopic, _ := w.topics(ProcessPathTopic)
	yardTopic, _ := w.topics(YardTopic)

	build := func(c *Consumer, err error) *Consumer {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	set := []*Consumer{
		build(NewFacilityConsumerForTopic(w.tr, facilityTopic, w.group("f"), w.svc)),
		build(NewAsnConsumerForTopic(w.tr, asnTopic, w.group("a"), w.svc)),
		build(NewCutoffConsumerForTopic(w.tr, cptTopic, w.group("c"), w.svc)),
		build(NewYardConsumerForTopic(w.tr, yardTopic, w.group("y"), w.svc)),
	}
	running := Start(w.ctx, set...)

	w.produce(facilityTopic, fixture(t, "facility_ZoneRegistered"), fixture(t, "facility_LocationSlotRegistered"))
	w.produce(asnTopic, fixture(t, "inbound_ASNRegistered"))
	w.produce(cptTopic, fixture(t, "ppm_CPTScheduleChanged"))
	w.eventually("the door, the ASN and the cutoff copies", func() bool {
		return w.count("doors") == 1 && w.count("asns") == 1 && w.count("cutoffs") == 1
	})
	w.book(fixtureAppointment, fixtureDoor, fixtureASN)

	w.produce(yardTopic, fixture(t, "yard_VisitCheckedIn"), fixture(t, "yard_TrailerDocked"), fixture(t, "yard_TrailerUndocked"))
	w.eventually("the appointment to complete", func() bool {
		return w.appt(fixtureAppointment).State == "COMPLETED"
	})
	if err := running.Stop(); err != nil {
		t.Fatalf("Stop = %v, want a clean shutdown", err)
	}
}
