package kafka

// Event consumer contract tests (E1-E4) for the facility edge: this
// repo's facility consumer subscribes to facility-layout's integration
// topic warehouse.facility.events and acts only on ZoneRegistered,
// LocationSlotRegistered and LocationSlotDecommissioned (the FacilityTopic
// / Type* constants in facility_consumer.go, wired into the reader by
// NewFacilityConsumer). The contract is validated against the PINNED copy
// of facility-layout's own apis/asyncapi.yaml
// (testdata/facility-layout.asyncapi.yaml, registered in
// contracts/pinned.json; the pinned-contracts CI job fails when the copy
// drifts from IQVO/facility-layout@develop).
//
// Golden fixtures come from the producer spec's OWN examples — every
// fixture here is a real wire document facility-layout documents for this
// channel. facility-layout documents LocationSlotDecommissioned WITHOUT
// examples: that type is E1-only here (its decode path is covered by the
// repo fixtures and the poison tests) and is called out in the PR body.

import (
	"context"
	"testing"

	ce "github.com/cloudevents/sdk-go/v2/event"

	"github.com/claudioed/dock-scheduling/internal/adapters/kafka/cloudevents"
	"github.com/claudioed/dock-scheduling/internal/domain/dockappointment"
)

// facilityDocumentedTypes loads the pinned spec's documented type catalog
// for the subscribed topic.
func facilityDocumentedTypes(t *testing.T) map[string]bool {
	t.Helper()
	return loadPinnedAsyncAPI(t, pinnedFacilitySpec).documentedTypes(t, FacilityTopic)
}

// E1: the topic this repo's reader actually subscribes to (the
// FacilityTopic constant NewFacilityConsumer wires into the reader) is
// documented by the producer's pinned spec as a channel, and the message
// catalog on that channel documents the exact CloudEvents types this
// repo's decoder routes on. Every OTHER type on the topic is Published
// Language this consumer must ignore (E4 proves it does).
func TestPinnedAsyncAPICoversSubscriptions_FacilityEvents(t *testing.T) {
	documented := facilityDocumentedTypes(t)

	for _, ty := range []string{TypeZoneRegistered, TypeLocationSlotRegistered, TypeLocationSlotDecommissioned} {
		if !documented[ty] {
			t.Errorf("pinned facility-layout spec does not document %s on the %s channel (documented: %v)", ty, FacilityTopic, documented)
		}
	}
	t.Logf("pinned spec documents %d type(s) on the shared topic; this consumer routes on 3 and ignores the rest", len(documented))
}

// E2: every ZoneRegistered and LocationSlotRegistered example the
// producer's pinned spec documents decodes through the REAL
// cloudevents.Decode and routes through the REAL facility consumer →
// DoorView use cases: a zone joins to its site, a Dock slot becomes a
// bookable door carrying the wire's GLN, a non-Dock slot never does.
// A replayed CloudEvents id applies nothing more (the ProcessedEvents
// claim is part of the effect's transaction).
func TestGoldenEventsDecode_FacilityFacts(t *testing.T) {
	doc := loadPinnedAsyncAPI(t, pinnedFacilitySpec)

	for _, ex := range doc.goldenExamples(t, "zoneRegistered") {
		t.Run("zoneRegistered/"+ex.Name, func(t *testing.T) {
			w := newWorld(t)
			w.handle(w.facility(), goldenJSON(t, ex.Payload))
			assertZoneJoined(t, w, ex)
			// Dedupe: the same id again claims nothing and changes nothing.
			w.handle(w.facility(), goldenJSON(t, ex.Payload))
			assertZoneJoined(t, w, ex)
		})
	}

	for _, ex := range doc.goldenExamples(t, "locationSlotRegistered") {
		t.Run("locationSlotRegistered/"+ex.Name, func(t *testing.T) {
			w := newWorld(t)
			handleFacilityGolden(t, w, ex)
			assertSlotRouted(t, w, ex)
			w.handle(w.facility(), goldenJSON(t, ex.Payload))
			assertSlotRouted(t, w, ex)
		})
	}

	// LocationSlotDecommissioned: the producer documents no examples —
	// E1 covers its subscription; the PR body names it.
	if exs := doc.message(t, "locationSlotDecommissioned").Examples; len(exs) == 0 {
		t.Logf("note: pinned facility-layout spec documents LocationSlotDecommissioned without examples; E1-only here")
	}
}

// handleFacilityGolden seeds the world with the golden's zone when the
// slot names one (a dock door's site is resolved by joining its zone, so
// the zone fact must arrive first — the X5 rule), then applies the slot
// golden itself.
func handleFacilityGolden(t *testing.T, w *world, ex pinnedExample) {
	t.Helper()
	data := goldenData(ex)
	zone, _ := data["zoneId"].(string)
	site := zoneSiteOf(t, ex)
	if zone != "" && site != "" {
		w.handle(w.facility(), facilityZoneVariant(t, zone, site))
	}
	w.handle(w.facility(), goldenJSON(t, ex.Payload))
}

// zoneSiteOf returns the site the golden's own zone registers under
// (from the pinned spec's zoneRegistered examples; "" when the golden's
// zone has no example — then no join is possible and the door stays nil).
func zoneSiteOf(t *testing.T, ex pinnedExample) string {
	t.Helper()
	data := goldenData(ex)
	zone, _ := data["zoneId"].(string)
	if zone == "" {
		return ""
	}
	doc := loadPinnedAsyncAPI(t, pinnedFacilitySpec)
	for _, zex := range doc.goldenExamples(t, "zoneRegistered") {
		zdata := goldenData(zex)
		if z, _ := zdata["zoneId"].(string); z == zone {
			s, _ := zdata["siteCode"].(string)
			return s
		}
	}
	return ""
}

// facilityZoneVariant mints a zone registration for zone@site with a FRESH
// CloudEvents id (a derived fact, not a golden: the golden under test is
// the slot event itself).
func facilityZoneVariant(t *testing.T, zone, site string) []byte {
	t.Helper()
	return goldenJSON(t, map[string]any{
		"specversion": "1.0", "id": "00000000-0000-4000-8000-00000000fac0",
		"source": "/warehouse/facility-layout", "type": TypeZoneRegistered,
		"subject": zone, "time": "2026-02-03T04:05:06Z",
		"datacontenttype": "application/json",
		"dataschema":      "urn:warehouse:facility-layout:events:ZoneRegistered:v1",
		"data":            map[string]any{"zoneId": zone, "siteCode": site},
	})
}

// assertZoneJoined asserts the golden zone routed into the DoorView: a
// dock door registered in that zone resolves to the wire's own site.
func assertZoneJoined(t *testing.T, w *world, ex pinnedExample) {
	t.Helper()
	data := goldenData(ex)
	zone, _ := data["zoneId"].(string)
	site, _ := data["siteCode"].(string)
	w.handle(w.facility(), facilityDockSlotVariant(t, zone+"-DOCK-01", zone))
	door := doorOf(t, w, zone+"-DOCK-01")
	if door == nil || string(door.Site) != site {
		t.Fatalf("a door in zone %s resolved to %+v, want site %s from the golden", zone, door, site)
	}
}

// facilityDockSlotVariant mints a Dock slot in zone with a FRESH id.
func facilityDockSlotVariant(t *testing.T, code, zone string) []byte {
	t.Helper()
	return goldenJSON(t, map[string]any{
		"specversion": "1.0", "id": "00000000-0000-4000-8000-00000000fac1",
		"source": "/warehouse/facility-layout", "type": TypeLocationSlotRegistered,
		"subject": code, "time": "2026-02-03T04:05:06Z",
		"datacontenttype": "application/json",
		"dataschema":      "urn:warehouse:facility-layout:events:LocationSlotRegistered:v1",
		"data":            map[string]any{"locationCode": code, "zoneId": zone, "role": "Dock", "dockFlow": "Inbound"},
	})
}

// assertSlotRouted asserts the golden slot produced exactly the DoorView
// state its own payload describes: role=Dock (with the zone joined)
// becomes a door carrying the wire's GLN; any other role stays no-door.
func assertSlotRouted(t *testing.T, w *world, ex pinnedExample) {
	t.Helper()
	data := goldenData(ex)
	code, _ := data["locationCode"].(string)
	role, _ := data["role"].(string)
	door := doorOf(t, w, code)
	if role != "Dock" {
		if door != nil {
			t.Fatalf("%s (role %q) became a bookable door (%+v), want no door", ex.Name, role, door)
		}
		return
	}
	if door == nil {
		t.Fatalf("%s (role Dock) produced no door: its zone must be registered first (X5)", ex.Name)
	}
	if gln, _ := data["gln"].(string); string(door.GLN) != gln {
		t.Errorf("%s: door GLN %q, wire says %q", ex.Name, door.GLN, gln)
	}
}

// doorOf reads the DoorView for code (nil when not bookable).
func doorOf(t *testing.T, w *world, code string) *dockappointment.Door {
	t.Helper()
	d, _ := w.store.Doors().Door(context.Background(), mustDoor(t, code))
	return d
}

// E3: every golden fixture carries the fleet-mandatory CloudEvents 1.0
// envelope — specversion 1.0, id, source /warehouse/facility-layout,
// type, subject, time, datacontenttype application/json, dataschema
// urn:warehouse:facility-layout:events:<EventName>:v1 — and the pinned
// spec's envelope schema requires exactly those attributes, with the
// structured-mode content type this repo's decoder assumes.
func TestCloudEventsEnvelopeInvariants_Facility(t *testing.T) {
	doc := loadPinnedAsyncAPI(t, pinnedFacilitySpec)
	for _, message := range []string{"zoneRegistered", "locationSlotRegistered", "locationSlotDecommissioned"} {
		msg := doc.message(t, message)
		wantSchema := doc.payloadConsts(t, msg.Payload)["dataschema"]
		for _, ex := range msg.Examples { // decommissioned documents none: schema-side only
			evt, err := cloudevents.Decode(goldenJSON(t, ex.Payload))
			if err != nil {
				t.Fatalf("decode golden example %s/%s: %v", message, ex.Name, err)
			}
			assertFleetEnvelope(t, ex.Name, evt, "/warehouse/facility-layout")
			if evt.DataSchema() != wantSchema {
				t.Errorf("%s/%s: dataschema %q, the pinned spec pins %q", message, ex.Name, evt.DataSchema(), wantSchema)
			}
		}
		assertEnvelopeRequired(t, doc, message)
	}
	if doc.DefaultContentType != "application/cloudevents+json" {
		t.Errorf("pinned spec defaultContentType %q, want application/cloudevents+json", doc.DefaultContentType)
	}
}

// E4: this consumer is a Conformist on a shared, fan-out topic — unknown
// ADDITIVE attributes in data must not fail the decode or the handling;
// a documented type this repo does not act on (siteRegistered, the
// channel's first message) is silently skipped and claims nothing; a
// message that is not a CloudEvent at all is WARNed and committed past
// (nil error — this repo's documented policy, never a DLQ); a payload of
// a HANDLED type that can never apply is the poison this repo
// dead-letters once.
func TestUnknownAttributesTolerated_Facility(t *testing.T) {
	doc := loadPinnedAsyncAPI(t, pinnedFacilitySpec)
	golden := doc.goldenExamples(t, "locationSlotRegistered")[0]
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
	if err := w.facility().HandleMessage(ctx, goldenJSON(t, tolerant)); err != nil {
		t.Fatalf("handle with unknown data attributes: %v", err)
	}
	assertSlotRouted(t, w, pinnedExample{Name: golden.Name, Payload: tolerant})

	// A documented type this consumer ignores: no error, no claim.
	other := clonePayload(golden.Payload)
	other["type"] = "com.warehouse.wms.facility-layout.site.SiteRegistered"
	other["id"] = "00000000-0000-4000-8000-00000000fac2"
	w2 := newWorld(t)
	if err := w2.facility().HandleMessage(ctx, goldenJSON(t, other)); err != nil {
		t.Fatalf("an undocumented-for-this-repo type must be skipped, not an error: %v", err)
	}
	if first, _ := w2.store.Claim(ctx, "00000000-0000-4000-8000-00000000fac2"); !first {
		t.Error("an ignored type claimed its CloudEvents id, blocking a future replay")
	}

	// Not a CloudEvent (a flat legacy payload): WARN, commit past, nil.
	w3 := newWorld(t)
	flat := []byte(`{"eventName":"ZoneRegistered","zoneId":"Z","siteCode":"WH1"}`)
	if err := w3.facility().HandleMessage(ctx, flat); err != nil {
		t.Fatalf("a non-CloudEvent must be skipped with nil, not %v", err)
	}
	if first, _ := w3.store.Claim(ctx, goldenID(golden)); !first {
		t.Error("a skipped non-CloudEvent claimed the golden's id")
	}

	// A handled type that can never apply: poison (dead-lettered once).
	poison := clonePayload(golden.Payload)
	poison["id"] = "00000000-0000-4000-8000-00000000fac3"
	poison["data"] = map[string]any{"role": "Dock", "locationCode": "", "zoneId": "Z"}
	assertPoison(t, newWorld(t).facility().HandleMessage(ctx, goldenJSON(t, poison)))
}

// assertFleetEnvelope asserts the CloudEvents 1.0 attributes every fleet
// producer must stamp on a golden example.
func assertFleetEnvelope(t *testing.T, name string, evt ce.Event, source string) {
	t.Helper()
	if evt.SpecVersion() != cloudevents.SpecVersion {
		t.Errorf("%s: specversion %q, want %q", name, evt.SpecVersion(), cloudevents.SpecVersion)
	}
	if evt.ID() == "" || evt.Subject() == "" || evt.Time().IsZero() {
		t.Errorf("%s: id/subject/time must all be present (got %q/%q/%v)", name, evt.ID(), evt.Subject(), evt.Time())
	}
	if evt.Source() != source {
		t.Errorf("%s: source %q, want %q", name, evt.Source(), source)
	}
	if evt.DataMediaType() != cloudevents.DataContentType {
		t.Errorf("%s: datacontenttype %q, want %q", name, evt.DataMediaType(), cloudevents.DataContentType)
	}
}
