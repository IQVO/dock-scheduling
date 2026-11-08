// Command reports is the read-only HTTP reader of dock-scheduling's analytics
// read side (ADR 0006): GET /reports/schedule-adherence, GET /reports/freshness,
// /healthz, /readyz and /metrics, answered from the ANALYTICAL database only
// through a read-only pool (and, in a deployment, a read-only role). It writes
// nothing and never opens the OLTP database or Kafka. There is no auth layer:
// access control is the in-cluster boundary (fleet rule).
//
// Environment:
//
//	ANALYTICS_DATABASE_URL  analytical Postgres DSN, ideally a read-only role (REQUIRED)
//	HTTP_ADDR               listen address (default :8092)
//	SHUTDOWN_DRAIN_DELAY    wait after flipping /readyz before closing (default 5s)
//	LOG_LEVEL               debug|info|warn|error (default info)
//
// GET /readyz is 200 only while the analytical database answers a ping
// (cached 3s, bounded 2s); /healthz is liveness only. Shutdown order: /readyz
// flips, drain delay, HTTP drain, the pool closes last.
package main

import (
	"context"
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

	inboundhttp "github.com/claudioed/dock-scheduling/internal/adapters/inbound/http"
	"github.com/claudioed/dock-scheduling/internal/adapters/outbound/analyticsstore"
	"github.com/claudioed/dock-scheduling/internal/adapters/outbound/telemetry"
	"github.com/claudioed/dock-scheduling/internal/bootretry"
	"github.com/claudioed/dock-scheduling/internal/config"
)

const (
	// defaultAddr is the reports listener (the OLTP API owns :8080).
	defaultAddr = ":8092"
	// shutdownTimeout bounds the HTTP server's graceful drain.
	shutdownTimeout = 10 * time.Second
)

// reportsConfig is the reports binary's boot configuration.
type reportsConfig struct {
	httpAddr     string
	analyticsURL string
	logLevel     string
	drainDelay   time.Duration
}

// loadConfig reads the environment. A missing ANALYTICS_DATABASE_URL is a boot
// error naming the variable.
func loadConfig(getenv func(string) string, logger *slog.Logger) (reportsConfig, error) {
	cfg := reportsConfig{
		httpAddr:     getenv(config.EnvHTTPAddr),
		analyticsURL: strings.TrimSpace(getenv(analyticsstore.EnvDatabaseURL)),
		logLevel:     getenv(config.EnvLogLevel),
	}
	if cfg.analyticsURL == "" {
		return reportsConfig{}, fmt.Errorf("%s is required: the reports are served from the analytical database", analyticsstore.EnvDatabaseURL)
	}
	if cfg.httpAddr == "" {
		cfg.httpAddr = defaultAddr
	}
	cfg.drainDelay = config.ShutdownDrain(getenv(config.EnvShutdownDrain), logger)
	return cfg, nil
}

func main() { os.Exit(realMain()) }

func realMain() int {
	if err := run(context.Background(), os.Getenv, nil); err != nil {
		newLogger("info").Error("dock-scheduling reports exited with error", "error", err)
		return 1
	}
	return 0
}

// run serves until ctx is cancelled, SIGINT or SIGTERM arrives, or the listener
// fails. onListen, when set, receives the bound address once the listener is
// up (a test binds ":0"). A clean stop returns nil.
func run(ctx context.Context, getenv func(string) string, onListen func(addr string)) error {
	cfg, err := loadConfig(getenv, newLogger("info"))
	if err != nil {
		return err
	}
	logger := newLogger(cfg.logLevel)
	slog.SetDefault(logger)

	metrics, err := telemetry.New()
	if err != nil {
		return fmt.Errorf("metrics: %w", err)
	}
	pool, err := analyticsstore.NewReadOnlyPool(ctx, cfg.analyticsURL)
	if err != nil {
		return fmt.Errorf("open %s: %w", analyticsstore.EnvDatabaseURL, err)
	}
	defer pool.Close() // runs last: after the HTTP drain
	// Retried: the first outbound dial of an injected pod can be reset.
	if err := bootretry.Retry(ctx, logger, "ping analytics database", func() error { return pool.Ping(ctx) }); err != nil {
		return fmt.Errorf("reach the database named by %s: %w", analyticsstore.EnvDatabaseURL, err)
	}

	reader := analyticsstore.NewReader(pool)
	readiness := inboundhttp.NewReadiness(inboundhttp.DefaultReadinessTTL, inboundhttp.DefaultReadinessTimeout,
		inboundhttp.Dependency{Name: "analytics-postgres", Check: pool.Ping})
	srv := &http.Server{
		Handler: inboundhttp.NewReportsRouter(&inboundhttp.ReportsServer{
			Adherence: reader, Freshness: reader, Readiness: readiness, Metrics: metrics.Handler(), Observe: metrics.ObserveHTTP,
		}),
		ReadHeaderTimeout: 5 * time.Second,
	}
	ln, err := net.Listen("tcp", cfg.httpAddr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.httpAddr, err)
	}
	if onListen != nil {
		onListen(ln.Addr().String())
	}

	serveErr := make(chan error, 1)
	go func() {
		logger.Info("reports server listening", "addr", ln.Addr().String())
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
		}
	}()

	sig, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	select {
	case err := <-serveErr:
		return fmt.Errorf("reports server failed: %w", err)
	case <-sig.Done():
	}

	readiness.SetNotReady()
	select {
	case <-time.After(cfg.drainDelay):
	case err := <-serveErr:
		return fmt.Errorf("reports server failed: %w", err)
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
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
