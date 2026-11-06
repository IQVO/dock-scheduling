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

// envMCPAddr names the listen address; defaultMCPAddr is the fleet's MCP port.
const (
	envMCPAddr     = "MCP_ADDR"
	defaultMCPAddr = ":8090"
)

// mcpConfig is the MCP server's boot configuration.
type mcpConfig struct {
	addr          string
	databaseURL   string
	migrationsURL string
	brokers       string
	brokerList    []string
	logLevel      string
	drainDelay    time.Duration
}

// loadConfig reads the environment. There is no in-memory mode (fleet decision
// 2026-10-17): a missing DATABASE_URL or KAFKA_BROKERS is a boot error naming
// the variable, and every missing variable is named at once. KAFKA_BROKERS is
// required although this binary starts no relay and no consumer: its writes
// enqueue outbox rows that the relay of cmd/api (or cmd/relay) drains, and
// /readyz reports the broker like the API's, so a deployment without Kafka
// refuses to boot instead of accepting bookings whose events never leave.
func loadConfig(getenv func(string) string, logger *slog.Logger) (mcpConfig, error) {
	cfg := mcpConfig{
		addr:        getenv(envMCPAddr),
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
		errs = append(errs, fmt.Errorf("%s is required: the comma-separated Kafka bootstrap servers the outbox relay publishes the events of these writes to, and /readyz", config.EnvKafkaBrokers))
	}
	if err := errors.Join(errs...); err != nil {
		return mcpConfig{}, err
	}
	if cfg.addr == "" {
		cfg.addr = defaultMCPAddr
	}
	cfg.migrationsURL = postgres.MigrationsURL(getenv)
	cfg.drainDelay = config.ShutdownDrain(getenv(config.EnvShutdownDrain), logger)
	return cfg, nil
}
