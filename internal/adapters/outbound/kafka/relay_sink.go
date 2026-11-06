package kafka

import (
	"context"
	"fmt"
	"strconv"
	"time"

	kafkago "github.com/segmentio/kafka-go"

	"github.com/claudioed/dock-scheduling/internal/application/outbox"
)

// Writer is the subset of *kafkago.Writer the relay sink depends on, so unit
// tests can substitute a fake.
type Writer interface {
	WriteMessages(ctx context.Context, msgs ...kafkago.Message) error
}

// RelaySink is the production Handler (and Sink) of the outbox relay. Its
// Writer has NO fixed Topic — kafka-go requires exactly one of Writer.Topic
// and Message.Topic, and each message names its own (the
// topic-not-set-twice rule, TestKafkaWriterTopicNotSetTwice). Settings are
// the ones of NewWriter: acks=all, the Hash balancer on the key and a short
// BatchTimeout. Constructing it never dials.
//
// EventsTopic and DeadLetterTopic default to TopicEvents and DeadLetterTopic;
// AnalyticsTopic defaults to TopicAnalytics (the twin of every encoded fact,
// ADR 0006). The fields exist so the integration proof can point the same
// code at isolated per-test topics.
type RelaySink struct {
	writer Writer
	// EventsTopic is where encoded facts go (TopicEvents when empty).
	EventsTopic string
	// AnalyticsTopic is where the analytics twins go (TopicAnalytics when
	// empty).
	AnalyticsTopic string
	// DeadLetterTopic is where unencodable rows are parked raw
	// (DeadLetterTopic when empty).
	DeadLetterTopic string
}

// NewRelaySink constructs a RelaySink over the comma-separated brokers (KAFKA_BROKERS). It does not dial.
func NewRelaySink(brokers string) *RelaySink { return NewRelaySinkWithWriter(NewWriter(brokers)) }

// NewRelaySinkWithWriter constructs a RelaySink over an explicit Writer (a
// fake in tests). The writer must NOT have a Topic set.
func NewRelaySinkWithWriter(w Writer) *RelaySink { return &RelaySink{writer: w} }

func (s *RelaySink) eventsTopic() string {
	if s.EventsTopic == "" {
		return TopicEvents
	}
	return s.EventsTopic
}

func (s *RelaySink) analyticsTopic() string {
	if s.AnalyticsTopic == "" {
		return TopicAnalytics
	}
	return s.AnalyticsTopic
}

func (s *RelaySink) deadLetterTopic() string {
	if s.DeadLetterTopic == "" {
		return DeadLetterTopic
	}
	return s.DeadLetterTopic
}

// Send writes already-encoded messages in one WriteMessages call, each on the
// topic it names. A message without a topic is rejected before anything is
// written.
func (s *RelaySink) Send(ctx context.Context, msgs ...outbox.Message) error {
	if len(msgs) == 0 {
		return nil
	}
	out, err := toKafka(msgs)
	if err != nil {
		return err
	}
	if err := s.writer.WriteMessages(ctx, out...); err != nil {
		return fmt.Errorf("kafka relay sink: write %d message(s): %w", len(out), err)
	}
	return nil
}

// toKafka maps outbox messages to kafka-go messages, rejecting a message
// that names no topic (the writer carries none, so the message must).
func toKafka(msgs []outbox.Message) ([]kafkago.Message, error) {
	out := make([]kafkago.Message, len(msgs))
	for i, m := range msgs {
		if m.Topic == "" {
			return nil, fmt.Errorf("kafka relay sink: message %d (%s) has no topic", i, m.EventType)
		}
		headers := make([]kafkago.Header, len(m.Headers))
		for j, h := range m.Headers {
			headers[j] = kafkago.Header{Key: h.Key, Value: []byte(h.Value)}
		}
		out[i] = kafkago.Message{Topic: m.Topic, Key: m.Key, Value: m.Value, Headers: headers}
	}
	return out, nil
}

// Handle implements the relay's Handler for ONE claimed outbox row: encode
// the fact to its CloudEvents of ADR 0004 and write it to the events topic.
// A row with no wire shape — an event the service does not publish, or a
// payload that is not the publisher's envelope — is poison that no retry can
// fix: it is dead-lettered RAW (the row's payload bytes, keyed by the
// aggregate id, with x-dlq-* headers) so the relay can dispose of the row
// and keep flowing (ADR 0005). A dead-letter write that itself fails is
// returned as an error, so the store records an attempt and the row is
// retried — the poison is never dropped on a broker hiccup.
//
// A cancelled context never dead-letters: shutdown mid-send must leave the
// row claimed-but-unpublished, not parked on the DLQ.
func (s *RelaySink) Handle(ctx context.Context, row outbox.Row) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	msgs, err := Encode(row.Fact)
	if err != nil {
		return s.deadLetter(ctx, row, err)
	}
	if len(msgs) == 0 {
		// Unknown domain events yield nothing: no retry can ever produce a
		// message for this row.
		return s.deadLetter(ctx, row, fmt.Errorf("no published wire shape for %s.%s", row.Fact.Entity, row.Fact.Event))
	}
	for i := range msgs {
		switch msgs[i].Topic {
		case TopicEvents:
			msgs[i].Topic = s.eventsTopic()
		case TopicAnalytics:
			msgs[i].Topic = s.analyticsTopic()
		}
	}
	out, err := toKafka(msgs)
	if err != nil {
		return err
	}
	if err := s.writer.WriteMessages(ctx, out...); err != nil {
		return fmt.Errorf("kafka relay sink: write row %d (%s): %w", row.ID, row.Fact.Event, err)
	}
	return nil
}

// deadLetter writes the row's raw payload to the DLQ topic with the fleet's
// x-dlq-* headers naming the cause (the value is the outbox payload, not a
// CloudEvent, so its content-type is application/json).
//
//dlq:deterministic an outbox row whose fact has no wire shape can never gain one by retrying
func (s *RelaySink) deadLetter(ctx context.Context, row outbox.Row, cause error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	msg := outbox.Message{
		Topic:      s.deadLetterTopic(),
		Key:        []byte(row.Fact.AggregateID),
		Value:      row.RawPayload,
		EventType:  "outbox-poison",
		Subject:    row.Fact.AggregateID,
		EventID:    row.Fact.EventID,
		DataSchema: "",
		Headers: []outbox.Header{
			{Key: "content-type", Value: "application/json"},
			{Key: "x-dlq-error", Value: cause.Error()},
			{Key: "x-dlq-outbox-row", Value: strconv.FormatInt(row.ID, 10)},
			{Key: "x-dlq-aggregate", Value: row.Fact.AggregateID},
			{Key: "x-dlq-failed-at", Value: time.Now().UTC().Format(time.RFC3339)},
		},
	}
	out, err := toKafka([]outbox.Message{msg})
	if err != nil {
		return err
	}
	if err := s.writer.WriteMessages(ctx, out...); err != nil {
		return fmt.Errorf("kafka relay sink: dead-letter row %d: %w", row.ID, err)
	}
	return nil
}

// Close releases the underlying Kafka writer.
func (s *RelaySink) Close() error {
	if w, ok := s.writer.(*kafkago.Writer); ok {
		return w.Close()
	}
	return nil
}
