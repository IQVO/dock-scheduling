package kafka

// Event consumer contract tests (E1-E4) for the CPT edge: this repo's
// cutoff consumer subscribes to process-path-management's integration
// topic warehouse.process-path-management.events and acts only on
// CPTScheduleChanged (the ProcessPathTopic / TypeCPTScheduleChanged
// constants in cutoff_consumer.go). The contract is validated against the
// PINNED copy of process-path-management's own apis/asyncapi.yaml
// (testdata/process-path-management.asyncapi.yaml, registered in
// contracts/pinned.json; the pinned-contracts CI job fails when the copy
// drifts from IQVO/process-path-management@develop).
//
// The pinned spec documents its own topic under 'publish:' (app
// perspective) rather than the fleet's usual channel-perspective
// 'subscribe:' — E1 therefore matches on the channel and its message
// catalog, not the operation key.
//
// Golden fixtures come from the producer spec's OWN examples.

import (
	"context"
	"testing"

	"github.com/claudioed/dock-scheduling/internal/adapters/kafka/cloudevents"
	"github.com/claudioed/dock-scheduling/internal/domain/shared"
)

// E1: the topic this repo's reader actually subscribes to (the
// ProcessPathTopic constant NewCutoffConsumer wires into the reader) is
// documented by the producer's pinned spec, and the message catalog on
// that channel documents the one CloudEvents type this repo's decoder
// routes on. The channel's other three ProcessPath* types are ones this
// consumer must ignore.
func TestPinnedAsyncAPICoversSubscriptions_ProcessPathEvents(t *testing.T) {
	documented := loadPinnedAsyncAPI(t, pinnedProcessPathSpec).documentedTypes(t, ProcessPathTopic)

	if !documented[TypeCPTScheduleChanged] {
		t.Errorf("pinned process-path-management spec does not document %s on the %s channel (documented: %v)",
			TypeCPTScheduleChanged, ProcessPathTopic, documented)
	}
	t.Logf("pinned spec documents %d type(s) on the shared topic; this consumer routes on 1 and ignores the rest", len(documented))
}

// E2: the producer's own CPTScheduleChanged example decodes through the
// REAL cloudevents.Decode and routes through the REAL cutoff consumer →
// the CutoffView: the event IS the whole schedule of the site, so the
// golden's sp1-1500 cutoff becomes Known and a cutoff the golden does not
// name stays unknown. A replayed CloudEvents id claims nothing and
// changes nothing.
func TestGoldenEventsDecode_CptSchedule(t *testing.T) {
	doc := loadPinnedAsyncAPI(t, pinnedProcessPathSpec)
	ex := doc.goldenExamples(t, "CPTScheduleChanged")[0]

	w := newWorld(t)
	w.handle(w.cutoff(), goldenJSON(t, ex.Payload))
	assertCutoffsKnown(t, w, goldenString(t, ex, "site_id"), true, "sp1-1500")
	assertCutoffsKnown(t, w, goldenString(t, ex, "site_id"), false, "sp1-1800")

	// Dedupe: the same id again claims nothing and stores nothing more.
	w.handle(w.cutoff(), goldenJSON(t, ex.Payload))
	assertCutoffsKnown(t, w, goldenString(t, ex, "site_id"), true, "sp1-1500")
	assertCutoffsKnown(t, w, goldenString(t, ex, "site_id"), false, "sp1-1800")
}

// assertCutoffsKnown asserts the CutoffView's Known verdict for site on ids.
func assertCutoffsKnown(t *testing.T, w *world, site string, want bool, ids ...string) {
	t.Helper()
	known, err := w.store.Cutoffs().Known(context.Background(), shared.SiteID(site), ids)
	if err != nil {
		t.Fatalf("cutoffs known: %v", err)
	}
	for _, id := range ids {
		if known[id] != want {
			t.Fatalf("cutoff %s known = %v, want %v", id, known[id], want)
		}
	}
}

// E3: every golden fixture carries the fleet-mandatory CloudEvents 1.0
// envelope — specversion 1.0, id, source
// /warehouse/process-path-management, type, subject, time,
// datacontenttype application/json, dataschema
// urn:warehouse:process-path-management:events:CPTScheduleChanged:v1
// (the spec pins type and dataschema as consts; the assertion reads them
// from the pinned payload itself) — and the envelope schema requires the
// full attribute set.
func TestCloudEventsEnvelopeInvariants_Cpt(t *testing.T) {
	doc := loadPinnedAsyncAPI(t, pinnedProcessPathSpec)
	msg := doc.message(t, "CPTScheduleChanged")
	consts := doc.payloadConsts(t, msg.Payload)
	for _, ex := range msg.Examples {
		evt, err := cloudevents.Decode(goldenJSON(t, ex.Payload))
		if err != nil {
			t.Fatalf("decode golden example %s: %v", ex.Name, err)
		}
		assertFleetEnvelope(t, ex.Name, evt, "/warehouse/process-path-management")
		if evt.Type() != consts["type"] {
			t.Errorf("%s: type %q, the pinned spec pins %q", ex.Name, evt.Type(), consts["type"])
		}
		if evt.DataSchema() != consts["dataschema"] {
			t.Errorf("%s: dataschema %q, the pinned spec pins %q", ex.Name, evt.DataSchema(), consts["dataschema"])
		}
	}
	assertEnvelopeRequired(t, doc, "CPTScheduleChanged")
	if doc.DefaultContentType != "application/cloudevents+json" {
		t.Errorf("pinned spec defaultContentType %q, want application/cloudevents+json", doc.DefaultContentType)
	}
}

// E4: this consumer is a Conformist on a shared topic — unknown ADDITIVE
// attributes in data (the producer's own eligible_path_ids is the live
// example: this repo stores the schedule without it) must not fail the
// decode or the handling; a documented type this repo does not act on
// (ProcessPathCreated) is silently skipped and claims nothing; a message
// that is not a CloudEvent at all is WARNed and committed past (nil
// error, this repo's documented policy — never a DLQ); a payload of the
// HANDLED type that can never apply is the poison this repo dead-letters
// once.
func TestUnknownAttributesTolerated_Cpt(t *testing.T) {
	doc := loadPinnedAsyncAPI(t, pinnedProcessPathSpec)
	golden := doc.goldenExamples(t, "CPTScheduleChanged")[0]
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
	if err := w.cutoff().HandleMessage(ctx, goldenJSON(t, tolerant)); err != nil {
		t.Fatalf("handle with unknown data attributes: %v", err)
	}
	assertCutoffsKnown(t, w, goldenString(t, golden, "site_id"), true, "sp1-1500")

	// A documented type this consumer ignores: no error, no claim.
	other := clonePayload(golden.Payload)
	other["type"] = "com.warehouse.wes.process-path-management.processpath.ProcessPathCreated"
	other["id"] = "00000000-0000-4000-8000-00000000cpt2"
	w2 := newWorld(t)
	if err := w2.cutoff().HandleMessage(ctx, goldenJSON(t, other)); err != nil {
		t.Fatalf("an undocumented-for-this-repo type must be skipped, not an error: %v", err)
	}
	if first, _ := w2.store.Claim(ctx, "00000000-0000-4000-8000-00000000cpt2"); !first {
		t.Error("an ignored type claimed its CloudEvents id, blocking a future replay")
	}

	// Not a CloudEvent (a flat legacy payload): WARN, commit past, nil.
	w3 := newWorld(t)
	flat := []byte(`{"site_id":"sp1","timezone":"UTC","cutoffs":[]}`)
	if err := w3.cutoff().HandleMessage(ctx, flat); err != nil {
		t.Fatalf("a non-CloudEvent must be skipped with nil, not %v", err)
	}
	if first, _ := w3.store.Claim(ctx, goldenID(golden)); !first {
		t.Error("a skipped non-CloudEvent claimed the golden's id")
	}

	// The handled type with a payload that can never apply: poison.
	poison := clonePayload(golden.Payload)
	poison["id"] = "00000000-0000-4000-8000-00000000cpt3"
	poison["data"] = map[string]any{"site_id": "", "cutoffs": []any{}}
	assertPoison(t, newWorld(t).cutoff().HandleMessage(ctx, goldenJSON(t, poison)))
}
