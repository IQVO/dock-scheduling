// Package kafka holds this service's INBOUND Kafka consumers (ADR 0003, ADR
// 0009): yard-management facts, facility-layout doors and zones,
// inbound-receiving ASNs, process-path-management cutoffs and the optional
// legacy appointment importer. Every consumer decodes CloudEvents 1.0 only,
// via internal/adapters/kafka/cloudevents, and dispatches on the FULL `type`
// string. The consumers only decode and map: every effect is a Consume* use
// case of internal/application/usecases, which claims the CloudEvents id in
// processed_events inside the SAME UnitOfWork as the effect.
//
// Delivery guarantee: AT-LEAST-ONCE with an ATOMIC effect. A message's offset
// is committed (FetchMessage + CommitMessages, never the auto-committing
// ReadMessage) only AFTER handling returned nil.
//
// Deterministic problems never block a partition:
//   - a message that is not a valid CloudEvent (the retired flat envelope
//     included): WARN, commit past (the type is unknowable, nothing to replay);
//   - a type the consumer has no handler for: ignored;
//   - a message of a HANDLED type that can never be applied (a payload that
//     does not decode, a missing id, a value the domain refuses as invalid):
//     DEAD-LETTERED to "<topic>.dlq" (headers x-dlq-source-topic and
//     x-dlq-cause) and committed past.
//
// A TRANSIENT failure (Postgres down, a lost commit, a concurrent update) is
// retried on the SAME message with capped exponential backoff, forever: every
// effect is idempotent under the claimed event id, and dropping a transiently
// unhandleable fact would silently lose it. A topic that does not exist yet is
// kafka-go's internal retry, surfaced through the reader's ErrorLogger: retry
// logs, never a crash.
package kafka

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	kafkago "github.com/segmentio/kafka-go"
)

// Reader is the subset of *kafkago.Reader a consumer needs, so unit tests never
// need a live broker. FetchMessage/CommitMessages (not ReadMessage) because
// ReadMessage with a GroupID auto-commits the offset the moment it returns,
// i.e. before the message has been handled.
type Reader interface {
	FetchMessage(ctx context.Context) (kafkago.Message, error)
	CommitMessages(ctx context.Context, msgs ...kafkago.Message) error
	Close() error
}

// ReaderFactory builds the reader of one topic under one consumer group. The
// production factory (NewTransport) is kafka-go's; tests inject fakes.
type ReaderFactory func(topic, groupID string) Reader

// DeadLetterFactory builds the dead-letter writer of a consumed topic.
type DeadLetterFactory func(topic string) DeadLetterWriter

// Transport is everything the consumers need from the broker side: how to
// read a topic and how to dead-letter it, plus logging and the retry backoff.
// NewTransport builds the kafka-go one; the composition root passes it to the
// New*Consumer constructors.
type Transport struct {
	Readers     ReaderFactory
	DeadLetters DeadLetterFactory
	Logger      *slog.Logger
	Retry       RetryPolicy
}

// NewTransport is the kafka-go Transport over brokers. Constructing readers
// and writers never dials: the connection is opened by the first FetchMessage
// inside the consumer's own goroutine, so an unreachable broker never blocks
// process startup.
func NewTransport(brokers []string, logger *slog.Logger) Transport {
	logger = defaultLogger(logger)
	return Transport{
		Readers: func(topic, groupID string) Reader {
			cfg := readerConfig(brokers, topic, groupID)
			cfg.ErrorLogger = readerErrorLogger(logger, topic)
			return kafkago.NewReader(cfg)
		},
		DeadLetters: func(topic string) DeadLetterWriter { return newDLQWriter(brokers, topic) },
		Logger:      logger,
	}
}

// readerConfig builds the kafka-go reader configuration shared by the
// consumers. GroupID always comes from the caller (an environment variable at
// the composition root, never a literal here), satisfying
// internal/architecture's TestKafkaConsumerGroupNeverHardcodedInline.
//
// CommitInterval is deliberately left UNSET: with a GroupID kafka-go then
// commits synchronously inside CommitMessages, which the run loop calls only
// after a message was handled. A group without a committed offset starts at
// the first offset, so a fresh deployment replays the topic and the local
// copies converge.
func readerConfig(brokers []string, topic, groupID string) kafkago.ReaderConfig {
	return kafkago.ReaderConfig{Brokers: brokers, Topic: topic, GroupID: groupID}
}

func defaultLogger(logger *slog.Logger) *slog.Logger {
	if logger == nil {
		return slog.Default()
	}
	return logger
}

// readerErrorLogger adapts logger to kafka-go's ErrorLogger: without it
// kafka-go silently retries a failing group join, a missing topic or an
// unreachable broker.
func readerErrorLogger(logger *slog.Logger, topic string) kafkago.LoggerFunc {
	return func(format string, args ...any) {
		logger.Error("kafka reader error; retrying", "topic", topic, "detail", fmt.Sprintf(format, args...))
	}
}

// RetryPolicy is the capped exponential backoff the run loop applies while the
// SAME message keeps failing with a transient error. Zero fields fall back to
// DefaultRetryInitial / DefaultRetryMax.
type RetryPolicy struct {
	Initial time.Duration
	Max     time.Duration
}

// Default backoff: 200ms, 400ms, 800ms ... capped at 5s.
const (
	DefaultRetryInitial = 200 * time.Millisecond
	DefaultRetryMax     = 5 * time.Second
)

func (p RetryPolicy) withDefaults() RetryPolicy {
	if p.Initial <= 0 {
		p.Initial = DefaultRetryInitial
	}
	if p.Max <= 0 {
		p.Max = DefaultRetryMax
	}
	return p
}

// next doubles d, capped at p.Max.
func (p RetryPolicy) next(d time.Duration) time.Duration {
	d *= 2
	if d > p.Max {
		return p.Max
	}
	return d
}

// sleepFunc waits d or until ctx is done (returning ctx.Err()). A field so
// tests can record the backoff sequence without really sleeping.
type sleepFunc func(ctx context.Context, d time.Duration) error

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// consumeLoop is the at-least-once run loop shared by the consumers.
type consumeLoop struct {
	reader Reader
	// handle applies one message; non-nil means transient failure => retry
	// (or a poison marker => dead-letter).
	handle func(ctx context.Context, msg kafkago.Message) error
	logger *slog.Logger
	name   string
	retry  RetryPolicy
	sleep  sleepFunc

	// deadLetter publishes a message the consumer can never apply to the
	// topic's dead letter topic (nil: poison is only WARNed and skipped).
	deadLetter func(ctx context.Context, msg kafkago.Message, cause error) error
}

// run fetches one message at a time and does not fetch the next until the
// current one was handled successfully AND its offset committed. It returns
// only when ctx is cancelled (shutdown drain) or FetchMessage itself fails.
func (l *consumeLoop) run(ctx context.Context) error {
	policy := l.retry.withDefaults()
	sleep := l.sleep
	if sleep == nil {
		sleep = sleepCtx
	}
	for {
		msg, err := l.reader.FetchMessage(ctx)
		if err != nil {
			return err
		}
		if err := l.handleWithDeadLetter(ctx, policy, sleep, msg); err != nil {
			return err
		}
		// Commit ONLY now. A commit that fails is retried too: the work is
		// already durable (the processed-events claim), and if the process
		// dies first the redelivery is skipped by that same claim.
		if err := l.retryUntilOK(ctx, policy, sleep, "offset commit", msg, func() error {
			return l.reader.CommitMessages(ctx, msg)
		}); err != nil {
			return err
		}
	}
}

// handleWithDeadLetter applies one message, deciding what a failure means:
//
//   - nil: done, the caller commits;
//   - a POISON marker (a payload of a handled type that can never be applied):
//     dead-lettered at once, without a handling retry, and the offset then
//     moves past it. The dead-letter PUBLISH itself is retried forever: a
//     message that could not even be dead-lettered is never silently
//     committed past;
//   - anything else is TRANSIENT: the SAME message is retried with the capped
//     exponential backoff, forever.
//
// The ctx.Err() check at the top is the fleet's escape-18b guard and it lives
// HERE, in the function that decides: a handler that failed because the
// process is stopping is neither retried past the cancellation nor
// dead-lettered; the offset stays uncommitted and the message is redelivered.
func (l *consumeLoop) handleWithDeadLetter(ctx context.Context, p RetryPolicy, sleep sleepFunc, msg kafkago.Message) error {
	err := l.handle(ctx, msg)
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var poison *deadLetterError
	if l.deadLetter != nil && errors.As(err, &poison) {
		cause := deadLetterCause(err)
		l.logger.ErrorContext(ctx, l.name+" message can never be applied; dead-lettering it without retry",
			"error", cause, "partition", msg.Partition, "offset", msg.Offset)
		return l.retryUntilOK(ctx, p, sleep, "dead-letter publish", msg, func() error {
			return l.deadLetter(ctx, msg, cause)
		})
	}
	return l.retryUntilOK(ctx, p, sleep, "handling", msg, func() error {
		return l.handle(ctx, msg)
	})
}

// retryUntilOK runs op until it returns nil, sleeping with capped exponential
// backoff between attempts. It gives up (returning ctx.Err()) only when ctx is
// cancelled. It NEVER skips: a transiently unhandleable message blocks its
// partition (and is logged at ERROR every attempt) rather than being lost.
func (l *consumeLoop) retryUntilOK(ctx context.Context, p RetryPolicy, sleep sleepFunc, what string, msg kafkago.Message, op func() error) error {
	delay := p.Initial
	for attempt := 1; ; attempt++ {
		err := op()
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		l.logger.ErrorContext(ctx, l.name+" "+what+" failed; retrying the same message",
			"error", err, "partition", msg.Partition, "offset", msg.Offset,
			"attempt", attempt, "retry_in", delay)
		if err := sleep(ctx, delay); err != nil {
			return err
		}
		delay = p.next(delay)
	}
}
