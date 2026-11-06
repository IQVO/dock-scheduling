package main

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	inboundkafka "github.com/claudioed/dock-scheduling/internal/adapters/inbound/kafka"
	"github.com/claudioed/dock-scheduling/internal/adapters/outbound/postgres"
	"github.com/claudioed/dock-scheduling/internal/config"
)

// apiConfig is the API's boot configuration.
type apiConfig struct {
	httpAddr      string
	databaseURL   string
	migrationsURL string
	brokers       string
	brokerList    []string
	groups        inboundkafka.Groups
	logLevel      string
	drainDelay    time.Duration
	relayInterval time.Duration
	batchSize     int
	maxAttempts   int
}

// loadConfig reads the environment. There is no in-memory mode and no log
// sink (fleet decision 2026-10-17): a missing DATABASE_URL, KAFKA_BROKERS or
// consumer group (YARD_, FACILITY_, ASN_ and CPT_CONSUMER_GROUP) is a boot
// error naming the variable, and every missing variable is named at once.
// DOCK_LEGACY_IMPORT_CONSUMER_GROUP is the one optional group: unset keeps the
// strangler stage A importer off. The tuning knobs fall back to their defaults
// with a warning, so a typo never silently disables the attempt cap.
func loadConfig(getenv func(string) string, logger *slog.Logger) (apiConfig, error) {
	cfg := apiConfig{
		httpAddr:    getenv(config.EnvHTTPAddr),
		databaseURL: getenv(postgres.EnvDatabaseURL),
		brokers:     strings.TrimSpace(getenv(config.EnvKafkaBrokers)),
		logLevel:    getenv(config.EnvLogLevel),
	}
	var errs []error
	if cfg.databaseURL == "" {
		errs = append(errs, fmt.Errorf("%s is required: the Postgres URL (there is no in-memory mode)", postgres.EnvDatabaseURL))
	}
	cfg.brokerList = config.Brokers(cfg.brokers)
	if len(cfg.brokerList) == 0 {
		errs = append(errs, fmt.Errorf("%s is required: the comma-separated Kafka bootstrap servers of the outbox relay, the consumers and /readyz", config.EnvKafkaBrokers))
	}
	groups, err := inboundkafka.GroupsFromEnv(getenv)
	if err != nil {
		errs = append(errs, err)
	}
	cfg.groups = groups
	if err := errors.Join(errs...); err != nil {
		return apiConfig{}, err
	}
	if cfg.httpAddr == "" {
		cfg.httpAddr = config.DefaultHTTPAddr
	}
	cfg.migrationsURL = postgres.MigrationsURL(getenv)
	cfg.drainDelay = config.ShutdownDrain(getenv(config.EnvShutdownDrain), logger)
	cfg.relayInterval = config.RelayInterval(getenv(config.EnvRelayInterval), logger)
	cfg.batchSize = config.BatchSize(getenv(config.EnvBatchSize), logger)
	cfg.maxAttempts = config.MaxAttempts(getenv(config.EnvMaxAttempts), logger)
	return cfg, nil
}
