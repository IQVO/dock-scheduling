// Package kafka is the outbound Kafka adapter. writer.go holds the one place
// a kafka-go Writer is constructed; this file holds the CloudEvents encoders:
// the mapping of a persisted outbox fact (ADR 0004's catalogue) to the exact
// wire message — full `type` string, `source` /warehouse/dock-scheduling,
// `subject` = aggregate id (the Kafka key), `dataschema`
// urn:warehouse:dock-scheduling:{events,analytics}:<Event>:v1, `time` = the
// fact's occurred-at in UTC, and the structured-mode JSON value plus the
// content-type header. Envelopes are built ONLY through
// internal/adapters/kafka/cloudevents.
package kafka

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/claudioed/dock-scheduling/internal/adapters/kafka/cloudevents"
	"github.com/claudioed/dock-scheduling/internal/application/outbox"
)

// The topics this service publishes to. The outbox relay sets Message.Topic
// per message; the Kafka writer carries no Topic (the topic-not-set-twice
// rule).
const (
	// TopicEvents is the integration topic: the facts other contexts consume.
	TopicEvents = "warehouse.dock-scheduling.events"
	// TopicAnalytics is this service's read side: EVERY published fact goes
	// there (byte-identical to its integration twin except for the dataschema
	// stream segment), and the analytics-only facts go ONLY there (ADR 0004).
	// Only the projector consumes it; it is not an integration contract.
	TopicAnalytics = "warehouse.dock-scheduling.analytics"
	// DLQSuffix extends a topic name into its dead-letter topic.
	DLQSuffix = ".dlq"
	// DeadLetterTopic is where the relay parks rows it can never publish
	// (the fleet pattern: the integration topic + DLQSuffix).
	DeadLetterTopic = TopicEvents + DLQSuffix
)

// Topics lists every topic this service writes to — the integration, the
// analytics and the dead-letter topic — for the topic provisioning of the
// deployment (the broker creates business topics on first write, but the
// fleet's ensure-topics step names them up front).
func Topics() []string { return []string{TopicEvents, TopicAnalytics, DeadLetterTopic} }

// schemaVersion is the dataschema version of every payload below
// (urn:warehouse:dock-scheduling:<stream>:<EventName>:v1).
const schemaVersion = 1

// Entities are the aggregate segment of `type` (the outbox row's aggreg_type).
const (
	entityAppointment = "dockappointment"
	entityCalendar    = "dockcalendar"
)

// route says where a published fact goes.
type route int

const (
	// unpublished: the catalogue does not know the fact.
	unpublished route = iota
	// integrationAndAnalytics: the events topic and its analytics twin.
	integrationAndAnalytics
	// analyticsOnly: the analytics topic only.
	analyticsOnly
)

// routeOf is ADR 0004's catalogue. An unknown combination is NOT an error —
// it yields no message (the relay dead-letters such a row instead of
// retrying it forever).
func routeOf(entity, event string) route {
	switch entity {
	case entityAppointment:
		switch event {
		case "DockAppointmentBooked", "DockAppointmentRescheduled", "DockAppointmentCancelled", "DockAppointmentNoShow":
			return integrationAndAnalytics
		case "DockAppointmentCheckedIn", "DockAppointmentDoorReassigned", "DockAppointmentCompleted", "DockAppointmentAtRisk":
			return analyticsOnly
		}
	case entityCalendar:
		switch event {
		case "DoorClosureScheduled", "DoorClosureLifted":
			return integrationAndAnalytics
		case "DoorOperatingHoursDeclared":
			return analyticsOnly
		}
	}
	return unpublished
}

// decodeData parses raw JSON into a generic value the CloudEvents helper can
// re-marshal into the envelope's `data` object. json.Number keeps every
// number exactly as written (no float64 rounding of int64 fields).
func decodeData(raw json.RawMessage) (any, error) {
	if len(raw) == 0 {
		return struct{}{}, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("kafka: decode %s data: %w", "payload", err)
	}
	return v, nil
}

// Encode maps one persisted outbox fact to its CloudEvents messages: the
// integration message on TopicEvents (integration facts only) and the
// analytics message on TopicAnalytics (every published fact) — one CloudEvents
// id, byte-identical payloads, dataschemas
// `urn:warehouse:dock-scheduling:{events,analytics}:<Event>:v1`. The
// integration message, when there is one, comes first. A fact the service
// does not publish yields NO message and NO error — the caller (the relay)
// dead-letters the row. A known fact that cannot be built into a valid
// envelope (empty aggregate id, invalid data JSON, SDK validation) is an
// error: deterministic poison, never retried by re-encoding.
//
// The CloudEvents id is the fact's EventID — minted once at enqueue and
// persisted, so a relay retry republishes the same id and consumers dedupe.
func Encode(f outbox.Fact) ([]outbox.Message, error) {
	r := routeOf(f.Entity, f.Event)
	if r == unpublished {
		return nil, nil
	}
	data, err := decodeData(f.Data)
	if err != nil {
		return nil, fmt.Errorf("encode %s.%s: %w", f.Entity, f.Event, err)
	}
	streams := []struct {
		stream string
		topic  string
	}{{cloudevents.StreamAnalytics, TopicAnalytics}}
	if r == integrationAndAnalytics {
		streams = append([]struct {
			stream string
			topic  string
		}{{cloudevents.StreamEvents, TopicEvents}}, streams...)
	}
	out := make([]outbox.Message, 0, len(streams))
	for _, st := range streams {
		msg, err := message(f, data, st.stream, st.topic)
		if err != nil {
			return nil, err
		}
		out = append(out, msg)
	}
	return out, nil
}

// message builds the CloudEvent of one stream: the shared envelope (id, type,
// subject, time, payload) with the stream's dataschema segment and topic.
func message(f outbox.Fact, data any, stream, topic string) (outbox.Message, error) {
	value, err := cloudevents.New(cloudevents.Spec{
		ID:        f.EventID,
		Entity:    f.Entity,
		EventName: f.Event,
		Subject:   f.AggregateID,
		Time:      f.OccurredAt,
		Stream:    stream,
		Version:   schemaVersion,
		Data:      data,
	})
	if err != nil {
		return outbox.Message{}, fmt.Errorf("encode %s.%s: %w", f.Entity, f.Event, err)
	}
	ct := cloudevents.ContentTypeHeader()
	return outbox.Message{
		EventID:    f.EventID,
		Topic:      topic,
		EventType:  cloudevents.Type(f.Entity, f.Event),
		Subject:    f.AggregateID,
		DataSchema: cloudevents.DataSchema(stream, f.Event, schemaVersion),
		Key:        []byte(f.AggregateID),
		Value:      value,
		Headers:    []outbox.Header{{Key: ct.Key, Value: string(ct.Value)}},
	}, nil
}
