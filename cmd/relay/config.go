package main

import (
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	outboxrelay "github.com/claudioed/dock-scheduling/internal/adapters/outbound/outbox"
	"github.com/claudioed/dock-scheduling/internal/adapters/outbound/postgres"
)

// envKafkaBrokers names the comma-separated Kafka bootstrap servers.
const envKafkaBrokers = "KAFKA_BROKERS"

// config is the relay's boot configuration.
type config struct {
	databaseURL string
	brokers     string
	interval    time.Duration
	batchSize   int
	maxAttempts int
}

// loadConfig reads the environment. A missing DATABASE_URL or KAFKA_BROKERS is
// a boot error naming the variable; the tuning knobs fall back to their
// defaults with a warning, so a typo never silently disables the cap.
func loadConfig(getenv func(string) string, logger *slog.Logger) (config, error) {
	var errs []error
	cfg := config{databaseURL: getenv(postgres.EnvDatabaseURL), brokers: strings.TrimSpace(getenv(envKafkaBrokers))}
	if cfg.databaseURL == "" {
		errs = append(errs, fmt.Errorf("%s is required: the Postgres URL of the outbox", postgres.EnvDatabaseURL))
	}
	if cfg.brokers == "" {
		errs = append(errs, fmt.Errorf("%s is required: the comma-separated Kafka bootstrap servers", envKafkaBrokers))
	}
	if err := errors.Join(errs...); err != nil {
		return config{}, err
	}
	cfg.interval = parseRelayInterval(getenv("OUTBOX_RELAY_INTERVAL"), logger)
	cfg.batchSize = parseBatchSize(getenv("OUTBOX_BATCH_SIZE"), logger)
	cfg.maxAttempts = outboxMaxAttempts(getenv("OUTBOX_MAX_ATTEMPTS"), logger)
	return cfg, nil
}

// parseRelayInterval reads OUTBOX_RELAY_INTERVAL (a Go duration or plain
// seconds), defaulting when unset, malformed or non-positive.
func parseRelayInterval(raw string, logger *slog.Logger) time.Duration {
	if raw == "" {
		return outboxrelay.DefaultInterval
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		if secs, serr := strconv.ParseFloat(raw, 64); serr == nil {
			d, err = time.Duration(secs*float64(time.Second)), nil
		}
	}
	if err != nil || d <= 0 {
		logger.Warn("invalid OUTBOX_RELAY_INTERVAL; using the default", "value", raw, "default", outboxrelay.DefaultInterval)
		return outboxrelay.DefaultInterval
	}
	return d
}

// parseBatchSize reads OUTBOX_BATCH_SIZE, defaulting when unset, malformed or
// non-positive.
func parseBatchSize(raw string, logger *slog.Logger) int {
	if raw == "" {
		return outboxrelay.DefaultBatchSize
	}
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n <= 0 {
		logger.Warn("invalid OUTBOX_BATCH_SIZE; using the default", "value", raw, "default", outboxrelay.DefaultBatchSize)
		return outboxrelay.DefaultBatchSize
	}
	return n
}

// outboxMaxAttempts reads OUTBOX_MAX_ATTEMPTS: how many times an outbox row may
// fail before the relay excludes it. Unset = the fleet default (20); 0 =
// unlimited; negative or unparsable values log a warning and use the default,
// so a typo never silently removes the cap. Every relay a cmd builds passes it
// (fitness test TestOutboxRelaysPassAttemptCap).
func outboxMaxAttempts(raw string, logger *slog.Logger) int {
	if raw == "" {
		return outboxrelay.DefaultMaxAttempts
	}
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n < 0 {
		logger.Warn("invalid OUTBOX_MAX_ATTEMPTS; using the default", "value", raw, "default", outboxrelay.DefaultMaxAttempts)
		return outboxrelay.DefaultMaxAttempts
	}
	return n
}
