package kafka

import (
	"context"
	"errors"
	"fmt"
	"time"

	kafkago "github.com/segmentio/kafka-go"
)

// DLQSuffix is appended to the consumed topic for its dead letter topic
// (warehouse.<ctx>.events -> warehouse.<ctx>.events.dlq), the fleet
// convention (the outbound relay does the same for its own topic).
const DLQSuffix = ".dlq"

// DeadLetterTopic is the dead letter topic of a consumed topic.
func DeadLetterTopic(topic string) string { return topic + DLQSuffix }

// The fleet's dead-letter headers: where the message came from and why it was
// moved.
const (
	dlqHeaderSourceTopic = "x-dlq-source-topic"
	dlqHeaderCause       = "x-dlq-cause"
)

// DeadLetterWriter publishes a message that can never be applied to the dead
// letter topic of the consumed topic. An interface so the consumer is testable
// without a broker.
type DeadLetterWriter interface {
	WriteDeadLetter(ctx context.Context, sourceTopic string, msg kafkago.Message, cause error) error
	Close() error
}

// dlqWriter is the kafka-go-backed DeadLetterWriter: one writer over the dead
// letter topic, acks=all like every writer of this service.
type dlqWriter struct {
	topic  string
	writer *kafkago.Writer
}

// newDLQWriter builds the dead-letter writer for topic+DLQSuffix. It does not
// dial; AllowAutoTopicCreation follows the fleet convention (the broker
// creates topics on first write).
func newDLQWriter(brokers []string, topic string) *dlqWriter {
	return &dlqWriter{
		topic: DeadLetterTopic(topic),
		writer: &kafkago.Writer{
			Addr:                   kafkago.TCP(brokers...),
			Topic:                  DeadLetterTopic(topic),
			Balancer:               &kafkago.Hash{},
			RequiredAcks:           kafkago.RequireAll,
			BatchTimeout:           10 * time.Millisecond,
			AllowAutoTopicCreation: true,
		},
	}
}

// WriteDeadLetter republishes the untouched message with its source topic and
// cause in the headers. The key rides along, so the message is replayable.
func (w *dlqWriter) WriteDeadLetter(ctx context.Context, sourceTopic string, msg kafkago.Message, cause error) error {
	out := kafkago.Message{Key: msg.Key, Value: msg.Value, Headers: append([]kafkago.Header{}, msg.Headers...)}
	out.Headers = append(out.Headers,
		kafkago.Header{Key: dlqHeaderSourceTopic, Value: []byte(sourceTopic)},
		kafkago.Header{Key: dlqHeaderCause, Value: []byte(cause.Error())},
	)
	if err := w.writer.WriteMessages(ctx, out); err != nil {
		return fmt.Errorf("write dead letter to %s: %w", w.topic, err)
	}
	return nil
}

// Close releases the underlying writer.
func (w *dlqWriter) Close() error {
	return w.writer.Close()
}

// deadLetterError asks the run loop to dead-letter a message and commit past
// it: the deterministic refusals of a HANDLED type (a payload that never
// decodes, a blank natural id, a value the domain refuses). It wraps the cause
// for the dead letter's header.
type deadLetterError struct{ cause error }

func (e *deadLetterError) Error() string { return "kafka: dead-letter the message: " + e.cause.Error() }
func (e *deadLetterError) Unwrap() error { return e.cause }

// deadLetterCause strips the marker for the header text.
func deadLetterCause(err error) error {
	var marker *deadLetterError
	if errors.As(err, &marker) {
		return marker.cause
	}
	return err
}
