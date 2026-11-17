package kafka

// Event consumer contract tests (E1-E4) for the yard edge: this repo's
// yard consumer subscribes to yard-management's integration topic
// warehouse.yard-management.events and acts only on VisitCheckedIn,
// VisitRejected, TrailerDocked and TrailerUndocked (the YardTopic /
// TypeVisit*/TypeTrailer* constants in yard_consumer.go). The contract is
// validated against the PINNED copy of yard-management's own
// apis/asyncapi.yaml (testdata/yard-management.asyncapi.yaml, registered
// in contracts/pinned.json; the pinned-contracts CI job fails when the
// copy drifts from IQVO/yard-management@develop).
//
// yard-management pins an AsyncAPI 3.0.0 document: channels are keyed by
// a logical name with the topic in `address`, and the messages hang off
// the channel itself (see consumer_contract_spec_test.go's parser).
//
// Golden fixtures come from the producer spec's OWN examples — the yard
// example set (appt-0a1b2c3d…, WH1, door WH1-DOCK-D01, the Nov 5
// 14:00-16:00 window) is the same world this repo's own tests seed, so
// the producer's wire documents drive the ArrivalFromGate /
// CancelOnVisitRejected / ActualDoorWins / CompleteOnDoorRelease policies
// end to end.

import (
	"context"
	"testing"

	"github.com/claudioed/dock-scheduling/internal/adapters/kafka/cloudevents"
)

// E1: the topic this repo's reader actually subscribes to (the YardTopic
// constant NewYardConsumer wires into the reader) is documented by the
// producer's pinned spec, and the message catalog on that channel
// documents all four CloudEvents types this repo's decoder routes on.
// The channel's other nine types (seals, dwell, inspections, ...) are
// ones this consumer must ignore.
func TestPinnedAsyncAPICoversSubscriptions_YardEvents(t *testing.T) {
	documented := loadPinnedAsyncAPI(t, pinnedYardSpec).documentedTypes(t, YardTopic)

	for _, ty := range []string{TypeVisitCheckedIn, TypeVisitRejected, TypeTrailerDocked, TypeTrailerUndocked} {
		if !documented[ty] {
			t.Errorf("pinned yard-management spec does not document %s on the %s channel (documented: %v)", ty, YardTopic, documented)
		}
	}
	t.Logf("pinned spec documents %d type(s) on the shared topic; this consumer routes on 4 and ignores the rest", len(documented))
}

// E2: the producer's own examples decode through the REAL
// cloudevents.Decode and route through the REAL yard consumer → the
// appointment policies. Each golden subtest drives the booked fixture
// appointment through the policy the golden names; a replayed CloudEvents
// id applies nothing more.
func TestGoldenEventsDecode_YardFacts(t *testing.T) {
	doc := loadPinnedAsyncAPI(t, pinnedYardSpec)

	for _, tc := range []struct {
		message string
		verify  func(t *testing.T, w *world, ex pinnedExample)
	}{
		{"VisitCheckedIn", verifyGoldenCheckedIn},
		{"VisitRejected", verifyGoldenRejected},
		{"TrailerDocked", verifyGoldenDocked},
		{"TrailerUndocked", verifyGoldenUndocked},
	} {
		for _, ex := range doc.goldenExamples(t, tc.message) {
			t.Run(tc.message+"/"+ex.Name, func(t *testing.T) {
				w := newWorld(t)
				w.seedBooked()
				tc.verify(t, w, ex)
				// Dedupe: the same id again claims nothing — the whole
				// appointment state (version included) is unchanged.
				before := w.appt(fixtureAppointment)
				w.handle(w.yard(), goldenJSON(t, ex.Payload))
				if after := w.appt(fixtureAppointment); after != before {
					t.Fatalf("replayed CloudEvents id %q changed the state: %+v -> %+v", goldenID(ex), before, after)
				}
			})
		}
	}
}

// verifyGoldenCheckedIn asserts the golden check-in recorded an explicit
// ON_TIME arrival (14:02 in the 14:00 window) and moved the appointment
// to CHECKED_IN.
func verifyGoldenCheckedIn(t *testing.T, w *world, ex pinnedExample) {
	t.Helper()
	w.handle(w.yard(), goldenJSON(t, ex.Payload))
	s := w.wantState(fixtureAppointment, "CHECKED_IN")
	if s.Arrival != "ON_TIME" || s.Implicit {
		t.Fatalf("arrival = %+v, want an explicit ON_TIME arrival from the golden's own arrived_at", s)
	}
}

// verifyGoldenRejected asserts the golden rejection (a walk-in: the
// producer's own example carries appointment_id null) is consumed without
// error and leaves the booked appointment untouched — the counted-ignore
// path — while a rejection naming the appointment (a derived variant with
// a FRESH id) cancels it.
func verifyGoldenRejected(t *testing.T, w *world, ex pinnedExample) {
	t.Helper()
	w.handle(w.yard(), goldenJSON(t, ex.Payload))
	w.wantState(fixtureAppointment, "BOOKED")

	named := clonePayload(ex.Payload)
	named["id"] = "00000000-0000-4000-8000-00000000yrd9"
	data := named["data"].(map[string]any)
	data["appointment_id"] = fixtureAppointment
	data["site_id"] = fixtureSite
	w.handle(w.yard(), goldenJSON(t, named))
	if s := w.wantState(fixtureAppointment, "CANCELLED"); s.Reason != "TURNED_AWAY" {
		t.Fatalf("cancellation reason = %q, want TURNED_AWAY", s.Reason)
	}
}

// verifyGoldenDocked asserts the golden docking (at the booked door) kept
// the appointment CHECKED_IN on that door after a golden check-in.
func verifyGoldenDocked(t *testing.T, w *world, ex pinnedExample) {
	t.Helper()
	checkedIn := loadPinnedAsyncAPI(t, pinnedYardSpec).goldenExamples(t, "VisitCheckedIn")[0]
	w.handle(w.yard(), goldenJSON(t, checkedIn.Payload))
	w.handle(w.yard(), goldenJSON(t, ex.Payload))
	s := w.wantState(fixtureAppointment, "CHECKED_IN")
	if s.Door != goldenString(t, ex, "door_code") {
		t.Fatalf("door = %s, want the golden's own %s", s.Door, goldenString(t, ex, "door_code"))
	}
}

// verifyGoldenUndocked asserts the golden undocking completed the
// appointment after a golden check-in and docking.
func verifyGoldenUndocked(t *testing.T, w *world, ex pinnedExample) {
	t.Helper()
	doc := loadPinnedAsyncAPI(t, pinnedYardSpec)
	w.handle(w.yard(), goldenJSON(t, doc.goldenExamples(t, "VisitCheckedIn")[0].Payload))
	w.handle(w.yard(), goldenJSON(t, doc.goldenExamples(t, "TrailerDocked")[0].Payload))
	w.handle(w.yard(), goldenJSON(t, ex.Payload))
	w.wantState(fixtureAppointment, "COMPLETED")
}

// E3: every golden fixture carries the fleet-mandatory CloudEvents 1.0
// envelope — specversion 1.0, id, source /warehouse/yard-management,
// type, subject, time, datacontenttype application/json, dataschema
// urn:warehouse:yard-management:events:<EventName>:v1 (the 3.0 document
// pins type and dataschema as consts on each message) — and the envelope
// schema requires the full attribute set.
func TestCloudEventsEnvelopeInvariants_Yard(t *testing.T) {
	doc := loadPinnedAsyncAPI(t, pinnedYardSpec)
	for _, message := range []string{"VisitCheckedIn", "VisitRejected", "TrailerDocked", "TrailerUndocked"} {
		msg := doc.message(t, message)
		consts := doc.payloadConsts(t, msg.Payload)
		for _, ex := range msg.Examples {
			evt, err := cloudevents.Decode(goldenJSON(t, ex.Payload))
			if err != nil {
				t.Fatalf("decode golden example %s/%s: %v", message, ex.Name, err)
			}
			assertFleetEnvelope(t, ex.Name, evt, "/warehouse/yard-management")
			if evt.Type() != consts["type"] {
				t.Errorf("%s/%s: type %q, the pinned spec pins %q", message, ex.Name, evt.Type(), consts["type"])
			}
			if evt.DataSchema() != consts["dataschema"] {
				t.Errorf("%s/%s: dataschema %q, the pinned spec pins %q", message, ex.Name, evt.DataSchema(), consts["dataschema"])
			}
		}
		assertEnvelopeRequired(t, doc, message)
	}
	if doc.DefaultContentType != "application/cloudevents+json" {
		t.Errorf("pinned spec defaultContentType %q, want application/cloudevents+json", doc.DefaultContentType)
	}
}

// E4: this consumer is a Conformist on a shared, fan-out topic — unknown
// ADDITIVE attributes in data (the producer's own trailers/purpose/
// version fields are the live example: this repo's DTOs ignore them) must
// not fail the decode or the handling; a documented type this repo does
// not act on (SealExceptionRaised) is silently skipped and claims
// nothing; a message that is not a CloudEvent at all is WARNed and
// committed past (nil error, this repo's documented policy — never a
// DLQ); a payload of a HANDLED type that can never apply is the poison
// this repo dead-letters once.
func TestUnknownAttributesTolerated_Yard(t *testing.T) {
	doc := loadPinnedAsyncAPI(t, pinnedYardSpec)
	golden := doc.goldenExamples(t, "VisitCheckedIn")[0]
	ctx := context.Background()

	// Unknown additive attributes in data — scalar and nested.
	tolerant := clonePayload(golden.Payload)
	data := tolerant["data"].(map[string]any)
	data["future_additive_field"] = "unknown-to-this-consumer"
	data["nested_extension"] = map[string]any{"deep": []any{1, "two"}}
	if _, err := cloudevents.Decode(goldenJSON(t, tolerant)); err != nil {
		t.Fatalf("decode with unknown data attributes: %v", err)
	}
	w := newWorld(t)
	w.seedBooked()
	if err := w.yard().HandleMessage(ctx, goldenJSON(t, tolerant)); err != nil {
		t.Fatalf("handle with unknown data attributes: %v", err)
	}
	if s := w.wantState(fixtureAppointment, "CHECKED_IN"); s.Arrival != "ON_TIME" {
		t.Fatalf("unknown data attributes changed the outcome: %+v", s)
	}

	// A documented type this consumer ignores: no error, no claim.
	other := clonePayload(golden.Payload)
	other["type"] = "com.warehouse.wms.yard-management.trailer.SealExceptionRaised"
	other["id"] = "00000000-0000-4000-8000-00000000yrd2"
	w2 := newWorld(t)
	if err := w2.yard().HandleMessage(ctx, goldenJSON(t, other)); err != nil {
		t.Fatalf("an undocumented-for-this-repo type must be skipped, not an error: %v", err)
	}
	if first, _ := w2.store.Claim(ctx, "00000000-0000-4000-8000-00000000yrd2"); !first {
		t.Error("an ignored type claimed its CloudEvents id, blocking a future replay")
	}

	// Not a CloudEvent (a flat legacy payload): WARN, commit past, nil.
	w3 := newWorld(t)
	flat := []byte(`{"visit_id":"v1","site_id":"WH1","arrived_at":"2026-11-05T14:02:11Z"}`)
	if err := w3.yard().HandleMessage(ctx, flat); err != nil {
		t.Fatalf("a non-CloudEvent must be skipped with nil, not %v", err)
	}
	if first, _ := w3.store.Claim(ctx, goldenID(golden)); !first {
		t.Error("a skipped non-CloudEvent claimed the golden's id")
	}

	// A handled type that can never apply: poison (dead-lettered once).
	poison := clonePayload(golden.Payload)
	poison["id"] = "00000000-0000-4000-8000-00000000yrd3"
	poison["data"] = map[string]any{"visit_id": "v-1", "site_id": "WH1", "arrived_at": "not-a-time"}
	assertPoison(t, newWorld(t).yard().HandleMessage(ctx, goldenJSON(t, poison)))
}
