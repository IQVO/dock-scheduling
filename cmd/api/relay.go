package main

import (
	"context"
	"errors"
	"log/slog"
	"time"

	outboundkafka "github.com/claudioed/dock-scheduling/internal/adapters/outbound/kafka"
	outboxrelay "github.com/claudioed/dock-scheduling/internal/adapters/outbound/outbox"
)

const (
	// sinkCloseTimeout bounds the Kafka writer's close: a Close concurrent with
	// an in-flight write can block until the write resolves, and shutdown must
	// stay bounded (fleet rule: run always returns).
	sinkCloseTimeout = 5 * time.Second
	// relayDrainTimeout bounds the wait for the relay loop to return after stop.
	relayDrainTimeout = 30 * time.Second
)

// worker is a started background loop: cancel stops it, done closes once it
// returned and result holds how.
type worker struct {
	name   string
	cancel context.CancelFunc
	done   chan struct{}
	result error
	sink   *outboundkafka.RelaySink
}

// startRelay starts the transactional-outbox relay in the API process: it
// drains the rows the use cases enqueue in the same transaction as the
// aggregates (events and analytics twins) to Kafka. Several replicas may run
// it at once: the store claims rows with FOR UPDATE SKIP LOCKED. cmd/relay is
// the same loop as a stand-alone binary. Constructing it never dials, so a
// broker outage never blocks boot (/readyz reports it).
func startRelay(cfg apiConfig, ad adapters, logger *slog.Logger) *worker {
	sink := outboundkafka.NewRelaySink(cfg.brokers)
	relay := outboxrelay.NewOutboxRelay(ad.outbox, sink, logger,
		outboxrelay.WithInterval(cfg.relayInterval),
		outboxrelay.WithBatchSize(cfg.batchSize),
		outboxrelay.WithMaxAttempts(cfg.maxAttempts))
	ctx, cancel := context.WithCancel(context.Background())
	w := &worker{name: "outbox relay", cancel: cancel, done: make(chan struct{}), sink: sink}
	go func() {
		defer close(w.done)
		logger.Info("outbox relay running", "interval", cfg.relayInterval.String(), "batch_size", cfg.batchSize, "max_attempts", cfg.maxAttempts,
			"events_topic", outboundkafka.TopicEvents, "analytics_topic", outboundkafka.TopicAnalytics, "dlq_topic", outboundkafka.DeadLetterTopic)
		if err := relay.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			w.result = err
		}
	}()
	return w
}

// stop asks the relay loop to return.
func (w *worker) stop() { w.cancel() }

// err is how the loop ended (nil for a clean stop); read it after done closed.
func (w *worker) err() error {
	if w.result == nil {
		return errors.New("returned without an error")
	}
	return w.result
}

// await waits (bounded) for the relay loop to return, then closes the Kafka
// writer with a hard deadline; a timed-out close leaks the writer to GC, since
// nothing writes after the relay has stopped.
func (w *worker) await(logger *slog.Logger) {
	select {
	case <-w.done:
	case <-time.After(relayDrainTimeout):
		logger.Warn("the outbox relay did not stop before the shutdown deadline")
	}
	closed := make(chan error, 1)
	go func() { closed <- w.sink.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			logger.Error("kafka relay sink close failed", "error", err)
		}
	case <-time.After(sinkCloseTimeout):
		logger.Warn("kafka relay sink close timed out; writer left to GC", "timeout", sinkCloseTimeout.String())
	}
}
