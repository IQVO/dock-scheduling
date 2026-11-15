package kafka

// Event consumer contract tests (E1-E4) for the ASN edge: this repo's ASN
// consumer subscribes to inbound-receiving's integration topic
// warehouse.inbound-receiving.events and acts only on ASNRegistered and
// ASNCancelled (the InboundReceivingTopic / TypeASN* constants in
// asn_consumer.go). The contract is validated against the PINNED copy of
// inbound-receiving's own apis/asyncapi.yaml
// (testdata/inbound-receiving.asyncapi.yaml, registered in
// contracts/pinned.json; the pinned-contracts CI job fails when the copy
// drifts from IQVO/inbound-receiving@develop).
//
// Golden fixtures come from the producer spec's OWN examples. The pinned
// spec declares no payload `type` consts for its messages — the CloudEvents
// type each message documents is read from its own examples (see
// documentedTypes), which is why E1 leans on the example payloads here.
//
// The golden ASNs are ASN-1001 — the very number this repo's fixture world
// books its seeded appointment on, so the cancel-when-all-cancelled policy
// is exercised by the producer's own wire documents end to end.

import (
	"context"
	"testing"

	"github.com/claudioed/dock-scheduling/internal/adapters/kafka/cloudevents"
	"github.com/claudioed/dock-scheduling/internal/domain/dockappointment"
)

// E1: the topic this repo's reader actually subscribes to (the
// InboundReceivingTopic constant NewAsnConsumer wires into the reader) is
// documented by the producer's pinned spec, and the message catalog on
// that channel documents both CloudEvents types this repo's decoder
// routes on. Every OTHER documented type (receipts, advices, ...) is one
// this consumer must ignore.
func TestPinnedAsyncAPICoversSubscriptions_InboundReceivingEvents(t *testing.T) {
	documented := loadPinnedAsyncAPI(t, pinnedInboundSpec).documentedTypes(t, InboundReceivingTopic)

	for _, ty := range []string{TypeASNRegistered, TypeASNCancelled} {
		if !documented[ty] {
			t.Errorf("pinned inbound-receiving spec does not document %s on the %s channel (documented: %v)", ty, InboundReceivingTopic, documented)
		}
	}
	t.Logf("pinned spec documents %d type(s) on the shared topic; this consumer routes on 2 and ignores the rest", len(documented))
}

// E2: the producer's own ASNRegistered and ASNCancelled examples decode
// through the REAL cloudevents.Decode and route through the REAL ASN
// consumer → the local ASN copy and CancelWhenAllAsnsCancelled: a
// registration records the ASN as REGISTERED, a cancellation makes the
// copy CANCELLED (sticky) and cancels the appointment holding that ASN;
// a replayed CloudEvents id applies nothing more.
func TestGoldenEventsDecode_AsnLifecycle(t *testing.T) {
	doc := loadPinnedAsyncAPI(t, pinnedInboundSpec)

	t.Run("registered", func(t *testing.T) {
		ex := doc.goldenExamples(t, "ASNRegistered")[0]
		w := newWorld(t)
		w.handle(w.asn(), goldenJSON(t, ex.Payload))
		assertAsnStatus(t, w, goldenString(t, ex, "asn_number"), dockappointment.AsnRegistered)
		// Dedupe: the same id again claims nothing and records nothing more.
		w.handle(w.asn(), goldenJSON(t, ex.Payload))
		assertAsnStatus(t, w, goldenString(t, ex, "asn_number"), dockappointment.AsnRegistered)
	})

	t.Run("cancelled", func(t *testing.T) {
		w := newWorld(t)
		w.seedBooked() // books the fixture appointment on ASN-1001, the golden's own number
		ex := doc.goldenExamples(t, "ASNCancelled")[0]
		w.handle(w.asn(), goldenJSON(t, ex.Payload))
		if s := w.wantState(fixtureAppointment, "CANCELLED"); s.Reason != "ASN_CANCELLED" {
			t.Fatalf("cancellation reason = %q, want ASN_CANCELLED", s.Reason)
		}
		assertAsnStatus(t, w, fixtureASN, dockappointment.AsnCancelledStatus)
		// Dedupe: a redelivery of the same id cancels nothing further.
		w.handle(w.asn(), goldenJSON(t, ex.Payload))
		w.wantState(fixtureAppointment, "CANCELLED")
	})
}

// goldenString returns a string field of a golden example's data map.
func goldenString(t *testing.T, ex pinnedExample, field string) string {
	t.Helper()
	v, _ := goldenData(ex)[field].(string)
	if v == "" {
		t.Fatalf("golden example %s carries no %s", ex.Name, field)
	}
	return v
}

// assertAsnStatus asserts the local ASN copy's status.
func assertAsnStatus(t *testing.T, w *world, number string, want dockappointment.AsnStatus) {
	t.Helper()
	st, err := w.store.Asns().Statuses(context.Background(), []string{number})
	if err != nil {
		t.Fatalf("asn statuses: %v", err)
	}
	if st[number] != want {
		t.Fatalf("asn %s status = %v, want %v", number, st[number], want)
	}
}

// E3: every golden fixture carries the fleet-mandatory CloudEvents 1.0
// envelope — specversion 1.0, id, source /warehouse/inbound-receiving,
// type, subject, time, datacontenttype application/json, dataschema
// urn:warehouse:inbound-receiving:events:<EventName>:v1 — and the pinned
// spec's envelope schema requires exactly those attributes (the messages
// pin their dataschemas in the examples; the schema side requires the
// attribute itself).
func TestCloudEventsEnvelopeInvariants_Asn(t *testing.T) {
	doc := loadPinnedAsyncAPI(t, pinnedInboundSpec)
	for _, message := range []string{"ASNRegistered", "ASNCancelled"} {
		for _, ex := range doc.goldenExamples(t, message) {
			evt, err := cloudevents.Decode(goldenJSON(t, ex.Payload))
			if err != nil {
				t.Fatalf("decode golden example %s/%s: %v", message, ex.Name, err)
			}
			assertFleetEnvelope(t, ex.Name, evt, "/warehouse/inbound-receiving")
			if want, _ := ex.Payload["dataschema"].(string); evt.DataSchema() != want {
				t.Errorf("%s/%s: dataschema %q, the example documents %q", message, ex.Name, evt.DataSchema(), want)
			}
		}
		assertEnvelopeRequired(t, doc, message)
	}
	if doc.DefaultContentType != "application/cloudevents+json" {
		t.Errorf("pinned spec defaultContentType %q, want application/cloudevents+json", doc.DefaultContentType)
	}
}

// E4: this consumer is a Conformist on a shared topic — unknown ADDITIVE
// attributes in data (the producer's own lines/supplier_ref/reason are
// the live example: this repo ignores them) must not fail the decode or
// the handling; a documented type this repo does not act on
// (ReceiptOpened) is silently skipped and claims nothing; a message that
// is not a CloudEvent at all is WARNed and committed past (nil error,
// this repo's documented policy — never a DLQ); a payload of a HANDLED
// type that can never apply is the poison this repo dead-letters once.
func TestUnknownAttributesTolerated_Asn(t *testing.T) {
	doc := loadPinnedAsyncAPI(t, pinnedInboundSpec)
	golden := doc.goldenExamples(t, "ASNRegistered")[0]
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
	if err := w.asn().HandleMessage(ctx, goldenJSON(t, tolerant)); err != nil {
		t.Fatalf("handle with unknown data attributes: %v", err)
	}
	assertAsnStatus(t, w, goldenString(t, golden, "asn_number"), dockappointment.AsnRegistered)

	// A documented type this consumer ignores: no error, no claim.
	other := clonePayload(golden.Payload)
	other["type"] = "com.warehouse.wms.inbound-receiving.receipt.ReceiptOpened"
	other["id"] = "00000000-0000-4000-8000-00000000asn2"
	w2 := newWorld(t)
	if err := w2.asn().HandleMessage(ctx, goldenJSON(t, other)); err != nil {
		t.Fatalf("an undocumented-for-this-repo type must be skipped, not an error: %v", err)
	}
	if first, _ := w2.store.Claim(ctx, "00000000-0000-4000-8000-00000000asn2"); !first {
		t.Error("an ignored type claimed its CloudEvents id, blocking a future replay")
	}

	// Not a CloudEvent (a flat legacy payload): WARN, commit past, nil.
	w3 := newWorld(t)
	flat := []byte(`{"asn_number":"ASN-9","supplier_ref":"ACME"}`)
	if err := w3.asn().HandleMessage(ctx, flat); err != nil {
		t.Fatalf("a non-CloudEvent must be skipped with nil, not %v", err)
	}
	if first, _ := w3.store.Claim(ctx, goldenID(golden)); !first {
		t.Error("a skipped non-CloudEvent claimed the golden's id")
	}

	// A handled type that can never apply: poison (dead-lettered once).
	poison := clonePayload(golden.Payload)
	poison["id"] = "00000000-0000-4000-8000-00000000asn3"
	poison["data"] = map[string]any{"asn_number": ""}
	assertPoison(t, newWorld(t).asn().HandleMessage(ctx, goldenJSON(t, poison)))
}
