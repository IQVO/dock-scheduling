package kafka_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/claudioed/dock-scheduling/internal/adapters/kafka/cloudevents"
	"github.com/claudioed/dock-scheduling/internal/adapters/outbound/kafka"
	"github.com/claudioed/dock-scheduling/internal/application/outbox"
)

// Golden exact-JSON tests, one per published type and stream (fleet Event
// Standard §6): every CloudEvents attribute, the whole data object, the Kafka
// key, the topic and exactly the content-type header are pinned by
// testdata/<type>.golden.json (integration stream) and
// testdata/<type>_analytics.golden.json (analytics stream). The goldens are
// written independently of the encoder (from the AsyncAPI examples), and the
// postgres package's facts test checks that the wire data its outbox publisher
// writes for the domain events is the very `data` object of the same goldens.

const (
	apptID = "appt-123e4567-e89b-12d3-a456-426614174000"
	siteID = "WH1"
)

func at(day, h, m, s int) time.Time { return time.Date(2026, 11, day, h, m, s, 0, time.UTC) }

// goldenCase is one published type: the fact the relay encodes (its data is
// the golden's own `data` object) and where it must go.
type goldenCase struct {
	name        string
	entity      string
	aggregateID string
	eventID     string
	occurredAt  time.Time
	integration bool // false: analytics only
}

func goldenCases() []goldenCase {
	return []goldenCase{
		{"DockAppointmentBooked", "dockappointment", apptID, "6f1d2c3b-4a5e-4b7c-8d9e-0a1b2c3d4e5f", at(5, 12, 0, 0), true},
		{"DockAppointmentRescheduled", "dockappointment", apptID, "0b9a8c7d-6e5f-4a3b-9c2d-1e0f9a8b7c6d", at(5, 13, 10, 0), true},
		{"DockAppointmentCancelled", "dockappointment", apptID, "2b1a0f9e-8d7c-4b6a-a594-837261504f3e", at(5, 14, 20, 0), true},
		{"DockAppointmentNoShow", "dockappointment", apptID, "4d3c2b1a-0f9e-4d8c-8b7a-6f5e4d3c2b1a", at(6, 11, 5, 0), true},
		{"DockAppointmentCheckedIn", "dockappointment", apptID, "8c7d6e5f-4a3b-4c2d-9e0f-9a8b7c6d5e4f", at(6, 7, 55, 30), false},
		{"DockAppointmentDoorReassigned", "dockappointment", apptID, "a5948372-6150-4f3e-8d7c-4b6a2b1a0f9e", at(6, 8, 20, 0), false},
		{"DockAppointmentCompleted", "dockappointment", apptID, "d4e5f6a7-b8c9-4d0e-8f2a-3b4c5d6e7f80", at(6, 9, 40, 10), false},
		{"DockAppointmentAtRisk", "dockappointment", apptID, "f3e2d1c0-9b8a-4c5d-8e7f-8a9b0c1d2e3f", at(6, 7, 30, 0), false},
		{"DoorClosureScheduled", "dockcalendar", siteID, "1a2b3c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5d", at(5, 15, 0, 0), true},
		{"DoorClosureLifted", "dockcalendar", siteID, "5d4c3b2a-1f0e-4d9c-ab8a-7f6e5d4c3b2a", at(7, 9, 0, 0), true},
		{"DoorOperatingHoursDeclared", "dockcalendar", siteID, "9e8d7c6b-5a4f-4e3d-8c2b-1a0f9e8d7c6b", at(5, 11, 0, 0), false},
	}
}

// goldenName maps an event name to its golden file: DockAppointmentBooked ->
// dock_appointment_booked.golden.json.
func goldenName(name string) string {
	out := make([]rune, 0, len(name)+8)
	for i, r := range name {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				out = append(out, '_')
			}
			r = r - 'A' + 'a'
		}
		out = append(out, r)
	}
	return string(out) + ".golden.json"
}

func goldenFile(name, stream string) string {
	if stream == cloudevents.StreamAnalytics {
		return strings.TrimSuffix(goldenName(name), ".golden.json") + "_analytics.golden.json"
	}
	return goldenName(name)
}

func readGolden(t *testing.T, file string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", file))
	if err != nil {
		t.Fatalf("read golden %s: %v", file, err)
	}
	return raw
}

func decodeJSON(t *testing.T, what string, raw []byte) any {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("%s is not JSON: %v\n%s", what, err, raw)
	}
	return v
}

// factOf builds the outbox fact of a case; its data is the golden's data.
func factOf(t *testing.T, c goldenCase) outbox.Fact {
	t.Helper()
	file := goldenFile(c.name, cloudevents.StreamAnalytics) // every case has an analytics golden
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(readGolden(t, file), &env); err != nil {
		t.Fatalf("golden %s: %v", file, err)
	}
	return outbox.Fact{EventID: c.eventID, Entity: c.entity, Event: c.name, AggregateID: c.aggregateID, OccurredAt: c.occurredAt, Data: env.Data}
}

// assertMessage checks one encoded message against its golden file: topic,
// key, the full CloudEvents value (every attribute and the whole data
// object), and exactly the content-type header.
func assertMessage(t *testing.T, c goldenCase, stream string, got outbox.Message) {
	t.Helper()
	topic := kafka.TopicEvents
	if stream == cloudevents.StreamAnalytics {
		topic = kafka.TopicAnalytics
	}
	if got.Topic != topic {
		t.Errorf("%s/%s: topic = %q, want %q", c.name, stream, got.Topic, topic)
	}
	if got.EventType != cloudevents.Type(c.entity, c.name) {
		t.Errorf("%s: type = %q, want %q", c.name, got.EventType, cloudevents.Type(c.entity, c.name))
	}
	if got.Subject != c.aggregateID || string(got.Key) != c.aggregateID {
		t.Errorf("%s: subject %q key %q, want the aggregate id %q", c.name, got.Subject, got.Key, c.aggregateID)
	}
	if got.EventID != c.eventID {
		t.Errorf("%s: event id = %q, want the persisted %q", c.name, got.EventID, c.eventID)
	}
	if got.DataSchema != cloudevents.DataSchema(stream, c.name, 1) {
		t.Errorf("%s: dataschema = %q", c.name, got.DataSchema)
	}
	wantHeaders := []outbox.Header{{Key: "content-type", Value: cloudevents.MediaType}}
	if !reflect.DeepEqual(got.Headers, wantHeaders) {
		t.Errorf("%s: headers = %+v, want %+v", c.name, got.Headers, wantHeaders)
	}
	file := goldenFile(c.name, stream)
	raw := readGolden(t, file)
	if g, w := decodeJSON(t, "value", got.Value), decodeJSON(t, file, raw); !reflect.DeepEqual(g, w) {
		t.Errorf("%s/%s: value differs from the golden\n got: %s\nwant: %s", c.name, stream, got.Value, raw)
	}
}

func TestEncode_GoldenJSONPerTypeAndStream(t *testing.T) {
	for _, c := range goldenCases() {
		t.Run(c.name, func(t *testing.T) {
			msgs, err := kafka.Encode(factOf(t, c))
			if err != nil {
				t.Fatalf("Encode: %v", err)
			}
			if c.integration {
				if len(msgs) != 2 {
					t.Fatalf("%d messages, want 2 (the integration message and its analytics twin)", len(msgs))
				}
				assertMessage(t, c, cloudevents.StreamEvents, msgs[0])
				assertMessage(t, c, cloudevents.StreamAnalytics, msgs[1])
				return
			}
			if len(msgs) != 1 {
				t.Fatalf("%d messages, want 1: an analytics-only fact goes ONLY to the analytics topic", len(msgs))
			}
			assertMessage(t, c, cloudevents.StreamAnalytics, msgs[0])
		})
	}
}

// The goldens on disk are exactly the ones the cases name: no stray file, no
// missing one (a new published type needs its golden pair).
func TestGoldenFilesAreExactlyTheCataloguedOnes(t *testing.T) {
	var want []string
	for _, c := range goldenCases() {
		want = append(want, goldenFile(c.name, cloudevents.StreamAnalytics))
		if c.integration {
			want = append(want, goldenFile(c.name, cloudevents.StreamEvents))
		}
	}
	entries, err := os.ReadDir("testdata")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range entries {
		got = append(got, e.Name())
	}
	sort.Strings(want)
	sort.Strings(got)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("testdata = %v\nwant       %v", got, want)
	}
}

// Every encoded value is a valid CloudEvents 1.0 event the repo's own decoder
// accepts, with the service source and the persisted id.
func TestEncode_ValuesDecodeAsCloudEvents(t *testing.T) {
	for _, c := range goldenCases() {
		msgs, err := kafka.Encode(factOf(t, c))
		if err != nil {
			t.Fatalf("%s: Encode: %v", c.name, err)
		}
		for _, m := range msgs {
			e, err := cloudevents.Decode(m.Value)
			if err != nil {
				t.Fatalf("%s: decode: %v", c.name, err)
			}
			if e.Source() != cloudevents.Source || e.DataContentType() != "application/json" || e.ID() != c.eventID || e.Subject() != c.aggregateID {
				t.Errorf("%s: source %q datacontenttype %q id %q subject %q", c.name, e.Source(), e.DataContentType(), e.ID(), e.Subject())
			}
			if len(e.Extensions()) != 0 {
				t.Errorf("%s: unexpected extension attributes: %v", c.name, e.Extensions())
			}
		}
	}
}

// The id is the one minted at enqueue and persisted with the outbox row, not a
// fresh one per encode: a relay retry republishes the same id.
func TestEncode_IDStableAcrossCalls(t *testing.T) {
	f := factOf(t, goldenCases()[0])
	first, err := kafka.Encode(f)
	if err != nil {
		t.Fatal(err)
	}
	second, err := kafka.Encode(f)
	if err != nil {
		t.Fatal(err)
	}
	for i := range first {
		if first[i].EventID != second[i].EventID || !bytes.Equal(first[i].Value, second[i].Value) {
			t.Fatal("two encodes of one fact differ: the id/value must be a function of the persisted row")
		}
	}
}

// The analytics twin is byte-identical to the integration message except for
// the dataschema stream segment: nothing else may differ.
func TestEncode_AnalyticsTwinDiffersOnlyInDataSchema(t *testing.T) {
	for _, c := range goldenCases() {
		if !c.integration {
			continue
		}
		msgs, err := kafka.Encode(factOf(t, c))
		if err != nil {
			t.Fatalf("%s: Encode: %v", c.name, err)
		}
		want := strings.Replace(string(msgs[0].Value), ":dock-scheduling:"+cloudevents.StreamEvents+":", ":dock-scheduling:"+cloudevents.StreamAnalytics+":", 1)
		if string(msgs[1].Value) != want {
			t.Errorf("%s: analytics twin differs from the integration value outside the dataschema", c.name)
		}
		if msgs[1].EventID != msgs[0].EventID || msgs[1].EventType != msgs[0].EventType ||
			msgs[1].Subject != msgs[0].Subject || !bytes.Equal(msgs[1].Key, msgs[0].Key) || !reflect.DeepEqual(msgs[1].Headers, msgs[0].Headers) {
			t.Errorf("%s: the twins' shared columns differ", c.name)
		}
	}
}

// Unknown facts yield NOTHING, never an error: the relay dead-letters the row
// instead of failing or retrying forever. The route is by entity AND event, so
// an event under the wrong aggregate is unknown too.
func TestEncode_UnknownYieldsNothing(t *testing.T) {
	for _, f := range []outbox.Fact{
		{Entity: "dockappointment", Event: "DockAppointmentReopened", AggregateID: apptID, OccurredAt: at(5, 9, 0, 0), Data: json.RawMessage(`{}`)},
		{Entity: "dockcalendar", Event: "DockAppointmentBooked", AggregateID: siteID, OccurredAt: at(5, 9, 0, 0), Data: json.RawMessage(`{}`)},
		{Entity: "dockappointment", Event: "DoorClosureScheduled", AggregateID: apptID, OccurredAt: at(5, 9, 0, 0), Data: json.RawMessage(`{}`)},
		{Entity: "returnreceipt", Event: "ReturnLineInspected", AggregateID: "rcpt-1", OccurredAt: at(5, 9, 0, 0), Data: json.RawMessage(`{}`)},
		{Entity: "", Event: "", AggregateID: "x", OccurredAt: at(5, 9, 0, 0), Data: json.RawMessage(`{}`)}, // undecodable payload row
	} {
		msgs, err := kafka.Encode(f)
		if err != nil || len(msgs) != 0 {
			t.Errorf("Encode(%s.%s) = %d msgs, err %v; want nothing, no error", f.Entity, f.Event, len(msgs), err)
		}
	}
}

// A fact the service publishes but cannot build into a valid envelope is a
// deterministic error (dead-lettered by the relay, never retried).
func TestEncode_InvalidFactIsAnError(t *testing.T) {
	f := factOf(t, goldenCases()[0])
	f.AggregateID = "" // empty subject: no CloudEvent can be built
	if _, err := kafka.Encode(f); err == nil {
		t.Fatal("Encode with empty aggregate id: want error, got nil")
	}
	f = factOf(t, goldenCases()[0])
	f.Data = json.RawMessage(`{"appointment_id":`) // data JSON that does not parse
	if _, err := kafka.Encode(f); err == nil {
		t.Fatal("Encode with broken data JSON: want error, got nil")
	}
	f = factOf(t, goldenCases()[0])
	f.Data = nil // no data at all is an empty object, not an error
	if msgs, err := kafka.Encode(f); err != nil || len(msgs) != 2 {
		t.Fatalf("Encode with no data = %d msgs, %v", len(msgs), err)
	}
}

// Numbers keep their exact lexical form through the envelope (json.Number): an
// int64 field is not rounded by a float64 round-trip.
func TestEncode_Int64DataSurvivesExactly(t *testing.T) {
	f := factOf(t, goldenCases()[0])
	f.Data = json.RawMessage(`{"appointment_id":"` + apptID + `","big":9007199254740993}`)
	msgs, err := kafka.Encode(f)
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Data struct {
			Big json.Number `json:"big"`
		} `json:"data"`
	}
	if err := json.Unmarshal(msgs[0].Value, &v); err != nil {
		t.Fatal(err)
	}
	if v.Data.Big.String() != "9007199254740993" {
		t.Fatalf("big = %s, want 9007199254740993 exactly", v.Data.Big)
	}
}

var nullableFields = []string{"carrier_scac", "cpt_id", "expected_seal_number", "visit_id", "eta_at"}

// nulled returns the fact of the case with every nullable field it carries set to null.
func nulled(t *testing.T, c goldenCase) outbox.Fact {
	t.Helper()
	f := factOf(t, c)
	var data map[string]any
	if err := json.Unmarshal(f.Data, &data); err != nil {
		t.Fatal(err)
	}
	for _, k := range nullableFields {
		if _, ok := data[k]; ok {
			data[k] = nil
		}
	}
	var err error
	if f.Data, err = json.Marshal(data); err != nil {
		t.Fatal(err)
	}
	return f
}

// The nullable fields are pinned as PRESENT and null (never omitted, never ""):
// carrier_scac, cpt_id, expected_seal_number, visit_id and eta_at.
func TestEncode_NullableFieldsAreNullNotOmitted(t *testing.T) {
	for _, c := range goldenCases() {
		if c.name != "DockAppointmentBooked" && c.name != "DockAppointmentCheckedIn" && c.name != "DockAppointmentAtRisk" {
			continue
		}
		msgs, err := kafka.Encode(nulled(t, c))
		if err != nil {
			t.Fatal(err)
		}
		var v struct {
			Data map[string]any `json:"data"`
		}
		if err := json.Unmarshal(msgs[len(msgs)-1].Value, &v); err != nil {
			t.Fatal(err)
		}
		for _, k := range nullableFields {
			val, present := v.Data[k]
			if present && val != nil {
				t.Errorf("%s.%s = %v, want null", c.name, k, val)
			}
		}
		if c.name == "DockAppointmentBooked" {
			if _, present := v.Data["cpt_id"]; !present {
				t.Errorf("%s: cpt_id must be present (as null), never omitted", c.name)
			}
		}
	}
}

func TestTopicsListEventsAnalyticsAndDeadLetter(t *testing.T) {
	want := []string{"warehouse.dock-scheduling.events", "warehouse.dock-scheduling.analytics", "warehouse.dock-scheduling.events.dlq"}
	if got := kafka.Topics(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Topics() = %v, want %v", got, want)
	}
}
