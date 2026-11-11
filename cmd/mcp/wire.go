package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	inboundhttp "github.com/claudioed/dock-scheduling/internal/adapters/inbound/http"
	inboundmcp "github.com/claudioed/dock-scheduling/internal/adapters/inbound/mcp"
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
	deps inboundmcp.Deps
}

// buildAdapters runs the embedded migrations, opens the pool and wires the SAME
// use cases cmd/api serves over REST, plus the agent idempotency over the same
// idempotency_keys table. Like every sibling's cmd/mcp it runs the idempotent
// migrations on start, so it can boot against a fresh database before cmd/api
// has (golang-migrate's advisory lock makes concurrent starts safe). The
// migrations and the first ping retry with backoff (the first outbound dial of
// an injected pod can be reset); on exhaustion the LAST error is returned and
// the process refuses to boot. There is no in-memory runtime.
func buildAdapters(ctx context.Context, cfg mcpConfig, metrics *telemetry.Metrics, logger *slog.Logger) (adapters, func(), error) {
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
	uow := postgres.NewUnitOfWork(pool)
	clock := system.Clock{}
	service := usecases.New(usecases.Deps{
		Appointments: postgres.NewAppointmentRepo(pool), Calendars: postgres.NewCalendarRepo(pool), Carriers: postgres.NewCarrierRepo(pool),
		Doors: postgres.NewDoorStore(pool), Asns: postgres.NewAsnStore(pool), Cutoffs: postgres.NewCutoffStore(pool),
		Processed: postgres.NewProcessedEventRepo(pool), Events: postgres.NewOutboxPublisher(pool), Metrics: metrics,
		IDs: system.IDs{}, Clock: clock, UoW: uow,
	})
	return adapters{
		pool: pool,
		deps: inboundmcp.Deps{
			Service:     service,
			Idempotency: usecases.Idempotency{Store: postgres.NewIdempotencyStore(pool), UoW: uow, Now: clock.Now},
		},
	}, pool.Close, nil
}

// newReadiness builds the /readyz gate: the drain gate plus a Postgres ping and
// a Kafka broker answering a topic-less metadata request (never topics: the
// broker auto-creates them on first write), cached 3s, bounded 2s. The ping is
// a parameter so the gate is testable without a pool.
func newReadiness(ping func(context.Context) error, brokers []string) *inboundhttp.Readiness {
	return inboundhttp.NewReadiness(inboundhttp.DefaultReadinessTTL, inboundhttp.DefaultReadinessTimeout,
		inboundhttp.Dependency{Name: "postgres", Check: ping},
		inboundhttp.Dependency{Name: "kafka", Check: outboundkafka.NewBrokerCheck(brokers).Check},
	)
}
