// Command projector is the WRITER of dock-scheduling's analytics read side
// (ADR 0006). It consumes the service's own analytics topic
// (warehouse.dock-scheduling.analytics) under a fixed, env-supplied consumer
// group, projects each event into the ANALYTICAL database (a separate database
// with its own embedded migrations and its own role) and serves only /healthz
// and /readyz on an admin port. It is the only writer of that database; the
// read-only reader is cmd/reports. It never opens the OLTP database.
//
// Kafka is dialled lazily (the reader connects inside its first fetch), so a
// broker outage at boot does not crash the process; the analytical database
// dial and migrations are retried through internal/bootretry because the first
// outbound connection of an injected pod can be reset shortly after start.
//
// Environment:
//
//	ANALYTICS_DATABASE_URL    analytical Postgres DSN, DIRECT, the projector's own role (REQUIRED)
//	KAFKA_BROKERS             comma-separated brokers (REQUIRED)
//	ANALYTICS_CONSUMER_GROUP  fixed consumer group of the projector (REQUIRED, set by the chart)
//	ADMIN_ADDR                /healthz and /readyz listen address (default :8091)
//	SHUTDOWN_DRAIN_DELAY      wait after flipping /readyz before closing (default 5s)
//	LOG_LEVEL                 debug|info|warn|error (default info)
//
// /readyz is 200 only while the analytical database answers a ping AND a Kafka
// broker answers a topic-less metadata request. The process exits non-zero when
// the consumer returns on its own, so the orchestrator restarts it.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	inboundhttp "github.com/claudioed/dock-scheduling/internal/adapters/inbound/http"
	inboundkafka "github.com/claudioed/dock-scheduling/internal/adapters/inbound/kafka"
	"github.com/claudioed/dock-scheduling/internal/adapters/outbound/analyticsstore"
	outboundkafka "github.com/claudioed/dock-scheduling/internal/adapters/outbound/kafka"
	"github.com/claudioed/dock-scheduling/internal/bootretry"
	"github.com/claudioed/dock-scheduling/internal/config"
)

const (
	// defaultAdminAddr is the projector's probe listener.
	defaultAdminAddr = ":8091"
	// envAdminAddr names it.
	envAdminAddr = "ADMIN_ADDR"
	// shutdownTimeout bounds the admin drain and the wait for the consumer to
	// finish the message in flight.
	shutdownTimeout = 10 * time.Second
)

// projectorConfig is the projector's boot configuration.
type projectorConfig struct {
	analyticsURL string
	brokers      []string
	group        string
	adminAddr    string
	logLevel     string
	drainDelay   time.Duration
}

// loadConfig reads the environment; every missing variable is named at once.
func loadConfig(getenv func(string) string, logger *slog.Logger) (projectorConfig, error) {
	cfg := projectorConfig{
		analyticsURL: strings.TrimSpace(getenv(analyticsstore.EnvDatabaseURL)),
		brokers:      config.Brokers(getenv(config.EnvKafkaBrokers)),
		adminAddr:    getenv(envAdminAddr),
		logLevel:     getenv(config.EnvLogLevel),
	}
	var errs []error
	if cfg.analyticsURL == "" {
		errs = append(errs, fmt.Errorf("%s is required: the projector writes only to the analytical database", analyticsstore.EnvDatabaseURL))
	}
	if len(cfg.brokers) == 0 {
		errs = append(errs, fmt.Errorf("%s is required: the comma-separated Kafka bootstrap servers of the analytics consumer", config.EnvKafkaBrokers))
	}
	group, err := inboundkafka.AnalyticsGroupFromEnv(getenv)
	if err != nil {
		errs = append(errs, err)
	}
	cfg.group = group
	if err := errors.Join(errs...); err != nil {
		return projectorConfig{}, err
	}
	if cfg.adminAddr == "" {
		cfg.adminAddr = defaultAdminAddr
	}
	cfg.drainDelay = config.ShutdownDrain(getenv(config.EnvShutdownDrain), logger)
	return cfg, nil
}

func main() { os.Exit(realMain()) }

func realMain() int {
	if err := run(context.Background(), os.Getenv, nil); err != nil {
		newLogger("info").Error("dock-scheduling projector exited with error", "error", err)
		return 1
	}
	return 0
}

// run boots the projector from the environment and serves until ctx is
// cancelled, SIGINT or SIGTERM arrives, or the consumer or the admin listener
// fails. onListen, when set, receives the bound admin address (a test binds
// ":0"). A clean stop returns nil.
func run(ctx context.Context, getenv func(string) string, onListen func(addr string)) error {
	cfg, err := loadConfig(getenv, newLogger("info"))
	if err != nil {
		return err
	}
	logger := newLogger(cfg.logLevel)
	slog.SetDefault(logger)

	pool, err := openAnalyticsPool(ctx, logger, cfg.analyticsURL)
	if err != nil {
		return err
	}
	defer pool.Close() // runs last: after the consumer stopped touching it

	consumer, err := inboundkafka.NewAnalyticsConsumer(inboundkafka.NewTransport(cfg.brokers, logger), cfg.group, analyticsstore.NewProjection(pool))
	if err != nil {
		return fmt.Errorf("build the analytics consumer: %w", err)
	}

	readiness := newReadiness(pool.Ping, cfg.brokers)
	admin := &http.Server{Handler: newAdminMux(readiness), ReadHeaderTimeout: 5 * time.Second}
	ln, err := net.Listen("tcp", cfg.adminAddr)
	if err != nil {
		_ = consumer.Close()
		return fmt.Errorf("listen on %s: %w", cfg.adminAddr, err)
	}
	if onListen != nil {
		onListen(ln.Addr().String())
	}
	adminErr := make(chan error, 1)
	go func() {
		logger.Info("projector admin server listening", "addr", ln.Addr().String())
		if err := admin.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			adminErr <- err
		}
	}()

	// The consumer runs on a context of its own, NOT the caller's: the shutdown
	// order (readiness, admin drain, consumer, pool) decides when it stops.
	running := inboundkafka.Start(context.Background(), consumer)
	consumerDone := make(chan struct{})
	go func() { defer close(consumerDone); _ = running.Wait() }()
	logger.Info("analytics consumer running", "topic", inboundkafka.AnalyticsTopic, "group_id", cfg.group, "brokers", cfg.brokers)

	sig, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	select {
	case <-consumerDone:
		// The consumer only returns on cancellation or an unrecoverable reader
		// failure: exit non-zero so the pod restarts.
		_ = admin.Close()
		return fmt.Errorf("the analytics consumer stopped unexpectedly: %w", consumerErr(running))
	case err := <-adminErr:
		stopConsumer(logger, running, consumerDone)
		return fmt.Errorf("admin server failed: %w", err)
	case <-sig.Done():
	}

	readiness.SetNotReady()
	if cfg.drainDelay > 0 {
		logger.Info("shutdown: readiness flipped to not-ready; waiting for traffic to drain", "drain_delay", cfg.drainDelay.String())
		time.Sleep(cfg.drainDelay)
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	shutdownErr := admin.Shutdown(shutdownCtx)
	stopConsumer(logger, running, consumerDone)
	return shutdownErr
}

// consumerErr is how the consumer ended (read after it returned).
func consumerErr(r *inboundkafka.Running) error {
	if err := r.Wait(); err != nil {
		return err
	}
	return errors.New("returned without an error")
}

// stopConsumer asks the consumer to stop and waits (bounded) for it to commit
// or dead-letter the message in flight.
func stopConsumer(logger *slog.Logger, running *inboundkafka.Running, done <-chan struct{}) {
	go func() { _ = running.Stop() }()
	select {
	case <-done:
	case <-time.After(shutdownTimeout):
		logger.Warn("the analytics consumer did not stop before the shutdown deadline")
	}
}

// openAnalyticsPool applies the embedded analytical migrations over the direct
// DSN, opens the writer pool and pings it, each under boot retry.
func openAnalyticsPool(ctx context.Context, logger *slog.Logger, url string) (*pgxpool.Pool, error) {
	if err := bootretry.Retry(ctx, logger, "run analytics migrations", func() error { return analyticsstore.RunMigrations(url) }); err != nil {
		return nil, fmt.Errorf("migrate the database named by %s: %w", analyticsstore.EnvDatabaseURL, err)
	}
	pool, err := analyticsstore.NewPool(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", analyticsstore.EnvDatabaseURL, err)
	}
	if err := bootretry.Retry(ctx, logger, "ping analytics database", func() error { return pool.Ping(ctx) }); err != nil {
		pool.Close()
		return nil, fmt.Errorf("reach the database named by %s: %w", analyticsstore.EnvDatabaseURL, err)
	}
	return pool, nil
}

// newReadiness builds the /readyz gate: the drain gate plus a ping of the
// analytical database and a Kafka broker answering a topic-less metadata request
// (never topics: the broker auto-creates them), cached 3s, bounded 2s.
func newReadiness(ping func(context.Context) error, brokers []string) *inboundhttp.Readiness {
	return inboundhttp.NewReadiness(inboundhttp.DefaultReadinessTTL, inboundhttp.DefaultReadinessTimeout,
		inboundhttp.Dependency{Name: "analytics-postgres", Check: ping},
		inboundhttp.Dependency{Name: "kafka", Check: outboundkafka.NewBrokerCheck(brokers).Check},
	)
}

// newAdminMux serves /healthz (liveness, never flipped) and /readyz: 200 only
// while the drain gate is open AND the analytical database and Kafka answer;
// 503 as the first step of shutdown, or while a dependency is down (the body
// names it).
func newAdminMux(gate *inboundhttp.Readiness) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if !gate.Ready() {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"status":"not_ready"}`))
			return
		}
		if failing := gate.Failing(r.Context()); len(failing) > 0 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "not_ready", "failing": failing})
			return
		}
		_, _ = w.Write([]byte(`{"status":"ready"}`))
	})
	return mux
}

func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn", "warning":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl}))
}
