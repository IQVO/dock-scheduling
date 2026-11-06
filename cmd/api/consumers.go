package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	inboundkafka "github.com/claudioed/dock-scheduling/internal/adapters/inbound/kafka"
	"github.com/claudioed/dock-scheduling/internal/application/usecases"
)

// consumersDrainTimeout bounds the wait for the consumers to return after
// stop: a handler in flight finishes or fails on the cancelled context, and
// shutdown must stay bounded (fleet rule: run always returns).
const consumersDrainTimeout = 30 * time.Second

// consumerSet is the built set of Kafka consumers (ADR 0009) running inside
// the API process, next to the outbox relay. Several replicas may run it at
// once: each consumer is a member of its group, and every effect is idempotent
// under the claimed CloudEvents id.
type consumerSet struct {
	consumers []*inboundkafka.Consumer
	names     []string
	cancel    context.CancelFunc
	running   *inboundkafka.Running
	// done closes once every consumer returned; read how with result.
	done chan struct{}
}

// buildConsumers constructs the yard, facility, ASN and CPT consumers, plus the
// legacy importer when DOCK_LEGACY_IMPORT_CONSUMER_GROUP is set. Constructing
// them never dials, so a broker outage never blocks boot (/readyz reports it).
// A blank mandatory group is an error naming its *_CONSUMER_GROUP variable.
func buildConsumers(cfg apiConfig, svc *usecases.Service, logger *slog.Logger) (*consumerSet, error) {
	cs, err := inboundkafka.Build(inboundkafka.NewTransport(cfg.brokerList, logger), cfg.groups, svc)
	if err != nil {
		return nil, fmt.Errorf("build the Kafka consumers: %w", err)
	}
	set := &consumerSet{consumers: cs, done: make(chan struct{})}
	for _, c := range cs {
		set.names = append(set.names, c.Name)
	}
	return set, nil
}

// start runs every consumer in its own goroutine. They run on a context of
// their own, NOT the caller's: the shutdown order (readiness, HTTP drain,
// consumers, relay, pool) decides when they stop, never a cancelled parent.
func (s *consumerSet) start(logger *slog.Logger) {
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.running = inboundkafka.Start(ctx, s.consumers...)
	go func() {
		defer close(s.done)
		_ = s.running.Wait()
	}()
	logger.Info("kafka consumers running", "consumers", strings.Join(s.names, ", "))
}

// stop asks every consumer to stop fetching; await waits (bounded) for them.
func (s *consumerSet) stop() { s.cancel() }

// await waits for the consumers to return, bounded.
func (s *consumerSet) await(logger *slog.Logger) {
	select {
	case <-s.done:
	case <-time.After(consumersDrainTimeout):
		logger.Warn("the kafka consumers did not stop before the shutdown deadline")
	}
}

// err is how the consumers ended (nil for a clean stop); read it after done closed.
func (s *consumerSet) err() error {
	if err := s.running.Wait(); err != nil {
		return err
	}
	return fmt.Errorf("returned without an error")
}
