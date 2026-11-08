// Command relay is the transactional-outbox relay of dock-scheduling: it drains
// the outbox table the API (and the consumers) fill in the same transaction as
// the aggregates, encodes every fact to its CloudEvents of ADR 0004 and writes
// it to Kafka — the events topic for integration facts, the analytics topic for
// every fact, the dead-letter topic for rows that can never publish. Delivery
// is at-least-once (the CloudEvents id is persisted at enqueue time).
//
// It serves only GET /healthz (ADMIN_ADDR, default :8094) and does not run migrations: the schema belongs to the
// API's boot. It is real wiring only (pgx + kafka-go); in-memory doubles are
// test-only.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	outboundkafka "github.com/claudioed/dock-scheduling/internal/adapters/outbound/kafka"
	outboxrelay "github.com/claudioed/dock-scheduling/internal/adapters/outbound/outbox"
	"github.com/claudioed/dock-scheduling/internal/adapters/outbound/postgres"
)

const (
	// sinkCloseTimeout bounds the Kafka writer's close: a Close concurrent with
	// an in-flight write can block until the write resolves, and shutdown must
	// stay bounded (fleet rule: run always returns).
	sinkCloseTimeout = 5 * time.Second
	// relayDrainTimeout bounds the wait for the relay loop to return after the
	// shutdown signal.
	relayDrainTimeout = 30 * time.Second
)

func main() { os.Exit(realMain()) }

// realMain is main with a return code, so the deferred signal stop runs before the exit.
func realMain() int {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Getenv, logger); err != nil {
		logger.Error("dock-scheduling relay stopped", "error", err)
		return 1
	}
	return 0
}

// run wires the relay from the environment and runs it until ctx is cancelled.
// A cancelled context is the clean exit; every other return is a failure.
func run(ctx context.Context, getenv func(string) string, logger *slog.Logger) error {
	cfg, err := loadConfig(getenv, logger)
	if err != nil {
		return err
	}
	pool, err := postgres.NewPool(ctx, cfg.databaseURL)
	if err != nil {
		return fmt.Errorf("open %s: %w", postgres.EnvDatabaseURL, err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		return fmt.Errorf("reach the database named by %s: %w", postgres.EnvDatabaseURL, err)
	}

	probe, err := startAdmin(cfg.adminAddr, logger)
	if err != nil {
		return err
	}
	defer func() {
		if err := probe.close(); err != nil {
			logger.Warn("relay admin server did not drain", "error", err)
		}
	}()

	sink := outboundkafka.NewRelaySink(cfg.brokers)
	relay := outboxrelay.NewOutboxRelay(postgres.NewOutboxStore(pool), sink, logger,
		outboxrelay.WithInterval(cfg.interval),
		outboxrelay.WithBatchSize(cfg.batchSize),
		outboxrelay.WithMaxAttempts(cfg.maxAttempts))
	logger.Info("dock-scheduling relay running",
		"interval", cfg.interval.String(), "batch_size", cfg.batchSize, "max_attempts", cfg.maxAttempts,
		"events_topic", outboundkafka.TopicEvents, "analytics_topic", outboundkafka.TopicAnalytics, "dlq_topic", outboundkafka.DeadLetterTopic)

	done := make(chan error, 1)
	go func() { done <- relay.Run(ctx) }()

	// Shutdown order: the relay loop stops first (an in-flight pass finishes or
	// rolls back, so every row is either published-and-removed or still in the
	// outbox), then the Kafka writer closes, then the deferred pool.Close runs.
	var runErr error
	select {
	case runErr = <-done:
	case <-ctx.Done():
		select {
		case runErr = <-done:
		case <-time.After(relayDrainTimeout):
			logger.Warn("the outbox relay did not stop before the shutdown deadline")
		}
	}
	closeSinkBounded(logger, sink)
	if runErr != nil && !errors.Is(runErr, context.Canceled) {
		return runErr
	}
	return nil
}

// closeSinkBounded closes the Kafka writer with a hard deadline; a timed-out
// close leaks the writer to GC — nothing writes after the relay has stopped.
func closeSinkBounded(logger *slog.Logger, sink *outboundkafka.RelaySink) {
	done := make(chan error, 1)
	go func() { done <- sink.Close() }()
	select {
	case err := <-done:
		if err != nil {
			logger.Error("kafka relay sink close failed", "error", err)
		}
	case <-time.After(sinkCloseTimeout):
		logger.Warn("kafka relay sink close timed out; writer left to GC", "timeout", sinkCloseTimeout.String())
	}
}
