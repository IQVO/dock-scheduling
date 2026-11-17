package kafka

// Event consumer contract tests (E1-E4) for the ANALYTICS edge: this
// repo's own projector (the analytics consumer) subscribes to the topic
// this service itself publishes through its transactional outbox relay —
// warehouse.dock-scheduling.analytics (ADR 0006) — and folds the six
// dockappointment milestone kinds into the schedule-adherence
// projection, idempotent per CloudEvents id. The contract is validated
// against the PINNED copy of this repo's OWN apis/asyncapi.yaml
// (testdata/dock-scheduling.asyncapi.yaml, registered in
// contracts/pinned.json): a self-pin, so the projector's decode contract
// and the publisher's spec cannot drift apart silently.
//
// Golden fixtures come from that spec's OWN examples. The spec declares
// no payload `type` consts — the CloudEvents type each message documents
// is read from its own examples (see documentedTypes). The analytics
// channel also documents twins this projector does NOT fold (door
// reassignments, at-risk marks, closures, operating hours, carriers):
// E4 proves they are ignored, and they double as its unknown-type
// fixtures.
//
// The projection fake below mirrors the REAL projection contract
// (adherence.Projection): Apply is idempotent per EventID — the id and
// the effect land in one transaction, and a second Apply of the same id
// changes nothing (applied = false).

import (
	"context"
	"testing"
	"time"

	"github.com/claudioed/dock-scheduling/internal/adapters/kafka/cloudevents"
	"github.com/claudioed/dock-scheduling/internal/analytics/adherence"
)

// analyticsContractMessages maps the pinned spec's analytics message
// names to the CloudEvents type the projector's handler map routes on.
var analyticsContractMessages = map[string]string{
	"DockAppointmentBookedAnalytics":      cloudevents.Type(entityAppointment, string(adherence.Booked)),
	"DockAppointmentRescheduledAnalytics": cloudevents.Type(entityAppointment, string(adherence.Rescheduled)),
	"DockAppointmentCancelledAnalytics":   cloudevents.Type(entityAppointment, string(adherence.Cancelled)),
	"DockAppointmentNoShowAnalytics":      cloudevents.Type(entityAppointment, string(adherence.NoShow)),
	"DockAppointmentCheckedInAnalytics":   cloudevents.Type(entityAppointment, string(adherence.CheckedIn)),
	"DockAppointmentCompletedAnalytics":   cloudevents.Type(entityAppointment, string(adherence.Completed)),
}

// idempotentProjection is the real Apply contract in miniature: it
// records each FIRST application of an EventID and answers applied=false
// for a redelivery, with the effect and the id committed together.
type idempotentProjection struct {
	applied []adherence.Event
	seen    map[string]bool
}

func newIdempotentProjection() *idempotentProjection {
	return &idempotentProjection{seen: map[string]bool{}}
}

func (p *idempotentProjection) Apply(_ context.Context, e adherence.Event) (bool, error) {
	if p.seen[e.EventID] {
		return false, nil
	}
	p.seen[e.EventID] = true
	p.applied = append(p.applied, e)
	return true, nil
}

// E1: the topic the projector's reader actually subscribes to (the
// AnalyticsTopic constant NewAnalyticsConsumer wires into the reader) is
// documented by this repo's own pinned spec, and the message catalog on
// that channel covers every CloudEvents type the projector folds.
func TestPinnedAsyncAPICoversSubscriptions_Analytics(t *testing.T) {
	documented := loadPinnedAsyncAPI(t, pinnedOwnSpec).documentedTypes(t, AnalyticsTopic)

	for message, want := range analyticsContractMessages {
		if !documented[want] {
			t.Errorf("pinned own spec does not document %s (%s) on the %s channel (documented: %v)", message, want, AnalyticsTopic, documented)
		}
	}
	t.Logf("pinned spec documents %d type(s) on the analytics topic; the projector folds 6 and ignores the rest", len(documented))
}

// E2: every example the pinned spec documents for the six folded
// messages decodes through the REAL cloudevents.Decode and routes through
// the REAL analytics consumer → toAdherenceEvent → Apply: exactly one
// applied fact per golden, carrying the wire's own appointment, site,
// version and the kind's own milestone fields, with appointment_id
// matching the CloudEvents subject (the projector's own cross-check).
// A redelivered id applies nothing more.
func TestGoldenEventsDecode_Analytics(t *testing.T) {
	doc := loadPinnedAsyncAPI(t, pinnedOwnSpec)
	ctx := context.Background()
	for message := range analyticsContractMessages {
		for _, ex := range doc.goldenExamples(t, message) {
			t.Run(message+"/"+ex.Name, func(t *testing.T) {
				proj := newIdempotentProjection()
				w := analyticsConsumer(t, proj)
				if err := w.HandleMessage(ctx, goldenJSON(t, ex.Payload)); err != nil {
					t.Fatalf("HandleMessage: %v", err)
				}
				assertOneAppliedFact(t, ex, proj)
				// Dedupe: the CloudEvents id is Apply's idempotency key.
				if err := w.HandleMessage(ctx, goldenJSON(t, ex.Payload)); err != nil {
					t.Fatalf("replayed %s: %v", ex.Name, err)
				}
				if len(proj.applied) != 1 {
					t.Fatalf("replayed CloudEvents id %q applied a second fact (%d total)", goldenID(ex), len(proj.applied))
				}
			})
		}
	}
}

// assertOneAppliedFact asserts the golden produced exactly one applied
// fact of the shape its own payload describes.
func assertOneAppliedFact(t *testing.T, ex pinnedExample, proj *idempotentProjection) {
	t.Helper()
	if len(proj.applied) != 1 {
		t.Fatalf("golden example applied %d facts, want 1", len(proj.applied))
	}
	got := proj.applied[0]
	data := goldenData(ex)
	if got.EventID != goldenID(ex) {
		t.Errorf("applied event id %q, wire CloudEvents id is %q", got.EventID, goldenID(ex))
	}
	if got.AppointmentID != data["appointment_id"] {
		t.Errorf("applied appointment %q, wire says %q", got.AppointmentID, data["appointment_id"])
	}
	if got.SiteID != data["site_id"] {
		t.Errorf("applied site %q, wire says %q", got.SiteID, data["site_id"])
	}
	if want, ok := data["version"].(int); ok && got.Version != int64(want) {
		t.Errorf("applied version %d, wire says %d", got.Version, want)
	}
	if got.ArrivalStatus != adherence.ArrivalStatus(stringOrEmpty(data["arrival_status"])) {
		t.Errorf("applied arrival status %q, wire says %q", got.ArrivalStatus, data["arrival_status"])
	}
	assertAppliedMilestones(t, ex, got, data)
}

// assertAppliedMilestones asserts the kind-specific fields the wire
// carries made it into the applied fact.
func assertAppliedMilestones(t *testing.T, ex pinnedExample, got adherence.Event, data map[string]any) {
	t.Helper()
	switch got.Kind {
	case adherence.Booked, adherence.Rescheduled:
		assertWireTime(t, ex.Name, "window_start", got.WindowStart, data)
		assertWireTime(t, ex.Name, "window_end", got.WindowEnd, data)
	case adherence.CheckedIn:
		assertWireTime(t, ex.Name, "checked_in_at", got.CheckedInAt, data)
	case adherence.Completed:
		assertWireTime(t, ex.Name, "completed_at", got.CompletedAt, data)
	case adherence.Cancelled, adherence.NoShow:
	}
}

func stringOrEmpty(v any) string {
	s, _ := v.(string)
	return s
}

// assertWireTime asserts an applied time field equals the wire's own text.
func assertWireTime(t *testing.T, name, field string, got time.Time, data map[string]any) {
	t.Helper()
	wantStr, _ := data[field].(string)
	want, err := time.Parse(time.RFC3339, wantStr)
	if err != nil {
		t.Fatalf("%s: golden %s is not RFC3339: %v", name, field, err)
	}
	if !got.Equal(want) {
		t.Errorf("%s: applied %s %v, wire says %v", name, field, got, want)
	}
}

// E3: every golden fixture carries the fleet-mandatory CloudEvents 1.0
// envelope — specversion 1.0, id, source /warehouse/dock-scheduling (this
// repo's own constant), type, subject, time, datacontenttype
// application/json, dataschema
// urn:warehouse:dock-scheduling:analytics:<EventName>:v1 (built by this
// repo's own cloudevents.DataSchema) — and the pinned spec's envelope
// schema requires the full attribute set.
func TestCloudEventsEnvelopeInvariants_Analytics(t *testing.T) {
	doc := loadPinnedAsyncAPI(t, pinnedOwnSpec)
	for message, wantType := range analyticsContractMessages {
		msg := doc.message(t, message)
		for _, ex := range msg.Examples {
			evt, err := cloudevents.Decode(goldenJSON(t, ex.Payload))
			if err != nil {
				t.Fatalf("decode %s/%s: %v", message, ex.Name, err)
			}
			assertFleetEnvelope(t, ex.Name, evt, cloudevents.Source)
			if evt.Type() != wantType {
				t.Errorf("%s: type %q, want %q", ex.Name, evt.Type(), wantType)
			}
			if want := cloudevents.DataSchema(cloudevents.StreamAnalytics, msg.Name, 1); evt.DataSchema() != want {
				t.Errorf("%s: dataschema %q, want %q", ex.Name, evt.DataSchema(), want)
			}
		}
		assertEnvelopeRequired(t, doc, message)
	}
	if doc.DefaultContentType != "application/cloudevents+json" {
		t.Errorf("pinned spec defaultContentType %q, want application/cloudevents+json", doc.DefaultContentType)
	}
}

// E4: the projector is a Conformist on its own stream — unknown ADDITIVE
// attributes in data must not fail the decode or the apply; a documented
// type the projector does not fold (the spec's own DoorReassigned twin)
// is ignored and applies nothing; a message that is not a CloudEvent is
// WARNed and committed past (nil error, no DLQ per this repo's policy).
func TestUnknownAttributesTolerated_Analytics(t *testing.T) {
	doc := loadPinnedAsyncAPI(t, pinnedOwnSpec)
	golden := doc.goldenExamples(t, "DockAppointmentBookedAnalytics")[0]
	ctx := context.Background()

	// Unknown additive attributes in data — scalar and nested.
	tolerant := clonePayload(golden.Payload)
	data := tolerant["data"].(map[string]any)
	data["future_additive_field"] = "unknown-to-this-projector"
	data["nested_extension"] = map[string]any{"deep": []any{1, "two"}}
	proj := newIdempotentProjection()
	w := analyticsConsumer(t, proj)
	if err := w.HandleMessage(ctx, goldenJSON(t, tolerant)); err != nil {
		t.Fatalf("HandleMessage with unknown data attributes: %v", err)
	}
	if len(proj.applied) != 1 || proj.applied[0].AppointmentID != data["appointment_id"] {
		t.Fatalf("unknown data attributes changed the outcome (%d facts)", len(proj.applied))
	}

	// A documented type this projector does not fold: ignored, nothing applied.
	unfolded := doc.goldenExamples(t, "DockAppointmentDoorReassignedAnalytics")[0]
	proj2 := newIdempotentProjection()
	w2 := analyticsConsumer(t, proj2)
	if err := w2.HandleMessage(ctx, goldenJSON(t, unfolded.Payload)); err != nil {
		t.Fatalf("an unfolded type must be ignored, not an error: %v", err)
	}
	if len(proj2.applied) != 0 {
		t.Errorf("an unfolded type was applied (%d facts)", len(proj2.applied))
	}

	// Not a CloudEvent (a flat legacy payload): WARN, commit past, nil.
	proj3 := newIdempotentProjection()
	w3 := analyticsConsumer(t, proj3)
	flat := []byte(`{"appointment_id":"appt-x","site_id":"WH1"}`)
	if err := w3.HandleMessage(ctx, flat); err != nil {
		t.Fatalf("a non-CloudEvent must be skipped with nil, not %v", err)
	}
	if len(proj3.applied) != 0 {
		t.Errorf("a non-CloudEvent was applied (%d facts)", len(proj3.applied))
	}
}
