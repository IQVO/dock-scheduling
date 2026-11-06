package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	inboundhttp "github.com/claudioed/dock-scheduling/internal/adapters/inbound/http"
	outboundkafka "github.com/claudioed/dock-scheduling/internal/adapters/outbound/kafka"
	"github.com/claudioed/dock-scheduling/internal/adapters/outbound/postgres"
	"github.com/claudioed/dock-scheduling/internal/adapters/outbound/system"
	"github.com/claudioed/dock-scheduling/internal/adapters/outbound/telemetry"
	"github.com/claudioed/dock-scheduling/internal/application/usecases"
	"github.com/claudioed/dock-scheduling/internal/bootretry"
)

// adapters is the set of real outbound adapters the composition root wires.
type adapters struct {
	pool *pgxpool.Pool
	deps usecases.Deps
	// store and uow back the Idempotency-Key middleware.
	idempotency *postgres.IdempotencyStore
	outbox      *postgres.OutboxStore
}

// buildAdapters runs the embedded migrations, opens the pool and wires every
// Postgres adapter. The migrations and the first ping retry with backoff (the
// first outbound dial of an injected pod can be reset), and on exhaustion the
// LAST error is returned: the process refuses to boot. There is no in-memory
// runtime: an unusable database is a boot error, never a degraded mode.
func buildAdapters(ctx context.Context, cfg apiConfig, metrics *telemetry.Metrics, logger *slog.Logger) (adapters, func(), error) {
	noop := func() {}
	if err := bootretry.Retry(ctx, logger, "run migrations", func() error { return postgres.RunMigrations(cfg.migrationsURL) }); err != nil {
		return adapters{}, noop, fmt.Errorf("migrate the database named by %s: %w", postgres.EnvDatabaseURL, err)
	}
	pool, err := postgres.NewPool(ctx, cfg.databaseURL)
	if err != nil {
		return adapters{}, noop, fmt.Errorf("open %s: %w", postgres.EnvDatabaseURL, err)
	}
	if err := bootretry.Retry(ctx, logger, "ping postgres", func() error { return pool.Ping(ctx) }); err != nil {
		pool.Close()
		return adapters{}, noop, fmt.Errorf("reach the database named by %s: %w", postgres.EnvDatabaseURL, err)
	}
	logger.Info("postgres adapters configured")
	return adapters{
		pool: pool,
		deps: usecases.Deps{
			Appointments: postgres.NewAppointmentRepo(pool), Calendars: postgres.NewCalendarRepo(pool),
			Doors: postgres.NewDoorStore(pool), Asns: postgres.NewAsnStore(pool), Cutoffs: postgres.NewCutoffStore(pool),
			Processed: postgres.NewProcessedEventRepo(pool), Events: postgres.NewOutboxPublisher(pool), Metrics: metrics,
			IDs: system.IDs{}, Clock: system.Clock{}, UoW: postgres.NewUnitOfWork(pool),
		},
		idempotency: postgres.NewIdempotencyStore(pool),
		outbox:      postgres.NewOutboxStore(pool),
	}, pool.Close, nil
}

// newReadiness builds the /readyz gate: the drain gate plus a Postgres ping
// and a Kafka broker answering a topic-less metadata request (never topics:
// the broker auto-creates them on first write), cached 3s, bounded 2s. The
// ping is a parameter so the gate is testable without a pool.
func newReadiness(ping func(context.Context) error, brokers []string) *inboundhttp.Readiness {
	return inboundhttp.NewReadiness(inboundhttp.DefaultReadinessTTL, inboundhttp.DefaultReadinessTimeout,
		inboundhttp.Dependency{Name: "postgres", Check: ping},
		inboundhttp.Dependency{Name: "kafka", Check: outboundkafka.NewBrokerCheck(brokers).Check},
	)
}

// buildServer wires the use cases into the REST adapter.
func buildServer(ad adapters, readiness *inboundhttp.Readiness, metrics *telemetry.Metrics) *inboundhttp.Server {
	return &inboundhttp.Server{
		Service:     usecases.New(ad.deps),
		Idempotency: ad.idempotency,
		UoW:         ad.deps.UoW,
		Now:         ad.deps.Clock.Now,
		// Adherence stays nil until the analytics read side lands (D-13): the
		// report route answers the contract shape with no rows.
		Readiness: readiness,
		Metrics:   metrics.Handler(),
		Observe:   metrics.ObserveHTTP,
	}
}
