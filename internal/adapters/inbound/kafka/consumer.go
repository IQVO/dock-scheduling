package kafka

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	ce "github.com/cloudevents/sdk-go/v2/event"
	kafkago "github.com/segmentio/kafka-go"

	"github.com/claudioed/dock-scheduling/internal/adapters/kafka/cloudevents"
	"github.com/claudioed/dock-scheduling/internal/analytics/adherence"
	"github.com/claudioed/dock-scheduling/internal/domain/dockappointment"
	"github.com/claudioed/dock-scheduling/internal/domain/shared"
)

// ErrNoConsumerGroup is a consumer built without a consumer group: a boot
// error naming the environment variable that must carry it (ADR 0003).
var ErrNoConsumerGroup = errors.New("kafka: consumer group is empty")

// errInvalidPayload marks a payload of a HANDLED type that can never be
// applied (a required field missing, a time that does not parse). It is
// deterministic: the message is dead-lettered, never retried.
var errInvalidPayload = errors.New("kafka: invalid payload")

func invalidf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", errInvalidPayload, fmt.Sprintf(format, args...))
}

// HandlerFunc applies one decoded CloudEvent. nil means done; an error wrapping
// deadLetterError means a poison message of a handled type (dead-lettered and
// committed past); any other non-nil error is transient and retried.
type HandlerFunc func(ctx context.Context, e ce.Event) error

// Consumer is one fact consumer: it reads Topic under a stable consumer group
// and dispatches each message on the FULL CloudEvents `type` to its handler.
// Types it has no handler for are ignored.
type Consumer struct {
	Name     string
	Topic    string
	GroupID  string
	Reader   Reader
	Handlers map[string]HandlerFunc
	Logger   *slog.Logger
	Retry    RetryPolicy

	// DLQ is the dead-letter writer (topic Topic+DLQSuffix): a message of a
	// handled type that can never be applied is moved there at once and
	// committed past instead of blocking the partition. Not-a-CloudEvent and
	// unknown types never reach it.
	DLQ DeadLetterWriter

	sleep sleepFunc // test hook; nil => real, ctx-cancellable sleep
}

// newConsumer builds a Consumer reading topic under groupID. envName is the
// environment variable the group comes from, named in the boot error. It
// builds the reader and the dead-letter writer through the Transport; neither
// dials.
func newConsumer(name, envName string, tr Transport, topic, groupID string, handlers map[string]HandlerFunc) (*Consumer, error) {
	if strings.TrimSpace(groupID) == "" {
		return nil, fmt.Errorf("%w: the %s needs %s", ErrNoConsumerGroup, name, envName)
	}
	if tr.Readers == nil {
		return nil, fmt.Errorf("kafka: the %s needs a reader factory", name)
	}
	c := &Consumer{
		Name: name, Topic: topic, GroupID: groupID, Reader: tr.Readers(topic, groupID),
		Handlers: handlers, Logger: defaultLogger(tr.Logger), Retry: tr.Retry,
	}
	if tr.DeadLetters != nil {
		c.DLQ = tr.DeadLetters(topic)
	}
	return c, nil
}

// Run consumes until ctx is cancelled (shutdown drain) or the reader fails. A
// message's offset is committed only after handling returned nil; a transient
// failure retries the SAME message with capped exponential backoff, forever; a
// poison message of a handled type is dead-lettered and committed past.
func (c *Consumer) Run(ctx context.Context) error {
	loop := consumeLoop{
		reader: c.Reader,
		handle: func(ctx context.Context, msg kafkago.Message) error { return c.HandleMessage(ctx, msg.Value) },
		logger: defaultLogger(c.Logger),
		name:   c.Name,
		retry:  c.Retry,
		sleep:  c.sleep,
	}
	if c.DLQ != nil {
		loop.deadLetter = func(ctx context.Context, msg kafkago.Message, cause error) error {
			return c.DLQ.WriteDeadLetter(ctx, c.Topic, msg, cause)
		}
	}
	return loop.run(ctx)
}

// Close releases the reader and the dead-letter writer.
func (c *Consumer) Close() error {
	err := c.Reader.Close()
	if c.DLQ != nil {
		err = errors.Join(err, c.DLQ.Close())
	}
	return err
}

// HandleMessage decodes one CloudEvents 1.0 message and dispatches it:
//   - not a valid CloudEvent: WARN, commit past (never dead-lettered: the type
//     is unknowable, so there is nothing to replay from the DLQ);
//   - a type this consumer does not act on: ignored;
//   - deadLetterError (poison of a handled type): WARN, the run loop
//     dead-letters it and commits past (nil here when no DLQ is wired);
//   - anything else: transient, returned for the capped retry.
func (c *Consumer) HandleMessage(ctx context.Context, value []byte) error {
	logger := defaultLogger(c.Logger)
	e, err := cloudevents.Decode(value)
	if err != nil {
		logger.WarnContext(ctx, "skipping a message that is not a valid CloudEvent", "consumer", c.Name, "topic", c.Topic, "error", err)
		return nil
	}
	handle, ok := c.Handlers[e.Type()]
	if !ok {
		return nil
	}
	err = handle(ctx, e)
	var poison *deadLetterError
	if errors.As(err, &poison) {
		logger.WarnContext(ctx, "an event that can never be applied; dead-lettering it",
			"consumer", c.Name, "type", e.Type(), "event_id", e.ID(), "error", deadLetterCause(err))
		if c.DLQ == nil {
			return nil
		}
	}
	return err
}

// typed is the generic apply-event helper every handler goes through: it
// decodes the event's JSON `data` into a T (tolerant of unknown fields; a
// payload that does not decode is poison), runs fn, classifies fn's error
// (deterministic refusal => poison, anything else transient) and logs the
// outcome. A handler is therefore only a mapping from T to a use case call.
func typed[T any](logger *slog.Logger, fn func(ctx context.Context, e ce.Event, d T) error) HandlerFunc {
	logger = defaultLogger(logger)
	return func(ctx context.Context, e ce.Event) error {
		var d T
		if err := e.DataAs(&d); err != nil {
			return &deadLetterError{cause: invalidf("payload of %s does not decode: %v", e.Type(), err)}
		}
		if err := fn(ctx, e, d); err != nil {
			if isDeterministic(err) {
				return &deadLetterError{cause: err}
			}
			return err
		}
		logger.InfoContext(ctx, "fact consumed", "type", e.Type(), "event_id", e.ID(), "subject", e.Subject())
		return nil
	}
}

// deterministicErrors are the refusals no retry can turn into success: the
// payload or the value objects it builds are invalid. State conflicts
// (ErrConcurrentUpdate) and infrastructure errors are NOT among them.
var deterministicErrors = []error{
	errInvalidPayload,
	shared.ErrInvalidDoorCode, shared.ErrInvalidSiteID, shared.ErrInvalidCarrier, shared.ErrInvalidDirection, shared.ErrInvalidDockFlow,
	dockappointment.ErrInvalidAppointmentID, dockappointment.ErrInvalidWindow, dockappointment.ErrDuplicateAsn,
	dockappointment.ErrInvalidRefs, dockappointment.ErrInvalidReason, dockappointment.ErrDoorDoubleBooked,
	dockappointment.ErrAsnAlreadyScheduled,
	// The analytics projection's refusals (ADR 0006).
	adherence.ErrInvalidEvent, adherence.ErrRejected,
}

// isDeterministic reports whether err is a refusal that retrying cannot fix.
func isDeterministic(err error) bool {
	for _, target := range deterministicErrors {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}

// require fails with a poison error naming every blank field.
func require(eventType string, fields map[string]string) error {
	var blank []string
	for name, v := range fields {
		if strings.TrimSpace(v) == "" {
			blank = append(blank, name)
		}
	}
	if len(blank) == 0 {
		return nil
	}
	slices.Sort(blank)
	return invalidf("%s carries no %s", eventType, strings.Join(blank, ", "))
}

// when returns the instant a payload field names; an absent field falls back
// to the CloudEvents `time` attribute (the domain occurred-at), and a value
// that does not parse as RFC3339 is poison.
func when(e ce.Event, field, value string) (time.Time, error) {
	if strings.TrimSpace(value) == "" {
		if t := e.Time(); !t.IsZero() {
			return t.UTC(), nil
		}
		return time.Time{}, invalidf("%s of %s is absent and the event carries no time", field, e.Type())
	}
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, invalidf("%s of %s is not an RFC3339 time: %v", field, e.Type(), err)
	}
	return t.UTC(), nil
}

// whenRequired is when without the fallback: a window bound has no sensible
// default.
func whenRequired(e ce.Event, field, value string) (time.Time, error) {
	if strings.TrimSpace(value) == "" {
		return time.Time{}, invalidf("%s of %s is absent", field, e.Type())
	}
	return when(e, field, value)
}
