package main

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/claudioed/dock-scheduling/internal/adapters/outbound/postgres"
	"github.com/claudioed/dock-scheduling/internal/config"
)

// envKafkaBrokers names the comma-separated Kafka bootstrap servers.
const envKafkaBrokers = config.EnvKafkaBrokers

// relayConfig is the relay's boot configuration.
type relayConfig struct {
	databaseURL string
	brokers     string
	interval    time.Duration
	batchSize   int
	maxAttempts int
	// adminAddr is the /healthz listener (ADMIN_ADDR, default :8094).
	adminAddr string
}

// loadConfig reads the environment. A missing DATABASE_URL or KAFKA_BROKERS is
// a boot error naming the variable; the tuning knobs fall back to their
// defaults with a warning, so a typo never silently disables the cap.
func loadConfig(getenv func(string) string, logger *slog.Logger) (relayConfig, error) {
	var errs []error
	cfg := relayConfig{databaseURL: getenv(postgres.EnvDatabaseURL), brokers: strings.TrimSpace(getenv(envKafkaBrokers))}
	if cfg.databaseURL == "" {
		errs = append(errs, fmt.Errorf("%s is required: the Postgres URL of the outbox", postgres.EnvDatabaseURL))
	}
	if cfg.brokers == "" {
		errs = append(errs, fmt.Errorf("%s is required: the comma-separated Kafka bootstrap servers", envKafkaBrokers))
	}
	if err := errors.Join(errs...); err != nil {
		return relayConfig{}, err
	}
	cfg.interval = parseRelayInterval(getenv(config.EnvRelayInterval), logger)
	cfg.batchSize = parseBatchSize(getenv(config.EnvBatchSize), logger)
	cfg.maxAttempts = outboxMaxAttempts(getenv(config.EnvMaxAttempts), logger)
	if cfg.adminAddr = strings.TrimSpace(getenv(envAdminAddr)); cfg.adminAddr == "" {
		cfg.adminAddr = defaultAdminAddr
	}
	return cfg, nil
}

// The knob parsers are shared with cmd/api (internal/config).
func parseRelayInterval(raw string, logger *slog.Logger) time.Duration {
	return config.RelayInterval(raw, logger)
}

func parseBatchSize(raw string, logger *slog.Logger) int { return config.BatchSize(raw, logger) }

// outboxMaxAttempts reads OUTBOX_MAX_ATTEMPTS: unset = the fleet default (20);
// 0 = unlimited; negative or unparsable values log a warning and use the
// default. Every relay a cmd builds passes it (fitness test TestOutboxRelaysPassAttemptCap).
func outboxMaxAttempts(raw string, logger *slog.Logger) int { return config.MaxAttempts(raw, logger) }
