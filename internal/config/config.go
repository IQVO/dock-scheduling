// Package config holds the environment parsing the composition roots (cmd/api
// and cmd/relay) share, so the two binaries read the outbox knobs the same way.
package config

import (
	"log/slog"
	"strconv"
	"strings"
	"time"

	outboxrelay "github.com/claudioed/dock-scheduling/internal/adapters/outbound/outbox"
)

// Environment variable names.
const (
	EnvKafkaBrokers    = "KAFKA_BROKERS"
	EnvRelayInterval   = "OUTBOX_RELAY_INTERVAL"
	EnvBatchSize       = "OUTBOX_BATCH_SIZE"
	EnvMaxAttempts     = "OUTBOX_MAX_ATTEMPTS"
	EnvShutdownDrain   = "SHUTDOWN_DRAIN_DELAY"
	EnvHTTPAddr        = "HTTP_ADDR"
	EnvLogLevel        = "LOG_LEVEL"
	DefaultHTTPAddr    = ":8080"
	DefaultShutdownDur = 5 * time.Second
)

// RelayInterval reads OUTBOX_RELAY_INTERVAL (a Go duration or plain seconds),
// defaulting when unset, malformed or non-positive.
func RelayInterval(raw string, logger *slog.Logger) time.Duration {
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

// BatchSize reads OUTBOX_BATCH_SIZE, defaulting when unset, malformed or
// non-positive.
func BatchSize(raw string, logger *slog.Logger) int {
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

// MaxAttempts reads OUTBOX_MAX_ATTEMPTS: how many times an outbox row may fail
// before the relay excludes it. Unset = the fleet default (20); 0 = unlimited;
// negative or unparsable values log a warning and use the default, so a typo
// never silently removes the cap.
func MaxAttempts(raw string, logger *slog.Logger) int {
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

// Brokers splits the comma-separated KAFKA_BROKERS into trimmed, non-empty addresses.
func Brokers(raw string) []string {
	var out []string
	for _, b := range strings.Split(raw, ",") {
		if b = strings.TrimSpace(b); b != "" {
			out = append(out, b)
		}
	}
	return out
}

// ShutdownDrain reads SHUTDOWN_DRAIN_DELAY (a Go duration or plain seconds):
// how long the process keeps serving after /readyz flipped to 503. "0" is
// valid (no wait); unset or malformed is DefaultShutdownDur.
func ShutdownDrain(raw string, logger *slog.Logger) time.Duration {
	if raw == "" {
		return DefaultShutdownDur
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		if secs, serr := strconv.ParseFloat(raw, 64); serr == nil {
			d, err = time.Duration(secs*float64(time.Second)), nil
		}
	}
	if err != nil || d < 0 {
		logger.Warn("invalid SHUTDOWN_DRAIN_DELAY; using the default", "value", raw, "default", DefaultShutdownDur)
		return DefaultShutdownDur
	}
	return d
}
