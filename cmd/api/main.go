// Command api is dock-scheduling's composition root: it wires the environment
// into the real adapters (Postgres through pgx, Kafka through the outbox
// relay), the adapters into the use cases and the use cases into the REST
// router. There is no in-memory mode, no log sink and no relay that is off
// (fleet decision 2026-10-17); the Kafka consumers are wired by their own
// step.
//
// Environment:
//
//	HTTP_ADDR                   listen address (default :8080)
//	DATABASE_URL                Postgres DSN (REQUIRED)
//	MIGRATIONS_DATABASE_URL     direct DSN for the boot migrations (default DATABASE_URL)
//	KAFKA_BROKERS               comma-separated brokers (REQUIRED: the outbox relay and /readyz)
//	OUTBOX_RELAY_INTERVAL       relay poll interval (Go duration or seconds, default 1s)
//	OUTBOX_BATCH_SIZE           rows per relay pass (default 100)
//	OUTBOX_MAX_ATTEMPTS         failures before the relay excludes a row (default 20, 0 = unlimited)
//	SHUTDOWN_DRAIN_DELAY        wait after flipping /readyz before closing (default 5s)
//	LOG_LEVEL                   debug|info|warn|error (default info)
//
// GET /readyz is 200 only while Postgres answers a ping AND a Kafka broker
// answers a topic-less metadata request (verdict cached 3s, bounded 2s);
// /healthz is liveness only. The process exits non-zero when the relay or the
// listener returns on its own, so the orchestrator restarts it.
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
	"github.com/claudioed/dock-scheduling/internal/adapters/outbound/telemetry"
)

// shutdownTimeout bounds the HTTP server's graceful drain.
const shutdownTimeout = 10 * time.Second

func main() { os.Exit(realMain()) }

// realMain is main with a return code, so deferred cleanups run before the exit.
func realMain() int {
	if err := run(context.Background()); err != nil {
		newLogger("info").Error("dock-scheduling api exited with error", "error", err)
		return 1
	}
	return 0
}

// run boots the service from the process environment and serves until
// SIGINT/SIGTERM or until ctx is cancelled (the integration test stops the
// real entry point that way). A clean stop returns nil.
func run(ctx context.Context) error { return runWith(ctx, os.Getenv, nil) }

// runWith is run with the environment and a listen hook injected: onListen, when
// set, receives the bound address once the listener is up (a test binds ":0").
func runWith(ctx context.Context, getenv func(string) string, onListen func(addr string)) error {
	boot := newLogger("info")
	cfg, err := loadConfig(getenv, boot)
	if err != nil {
		return err
	}
	logger := newLogger(cfg.logLevel)
	slog.SetDefault(logger)

	metrics, err := telemetry.New()
	if err != nil {
		return fmt.Errorf("metrics: %w", err)
	}
	// closeAdapters (pool.Close) is deferred FIRST so it runs LAST, after the
	// HTTP drain and the relay have stopped.
	ad, closeAdapters, err := buildAdapters(ctx, cfg, metrics, logger)
	if err != nil {
		return err
	}
	defer closeAdapters()

	readiness := newReadiness(ad.pool.Ping, cfg.brokerList)
	httpServer := &http.Server{
		Handler:           inboundhttp.NewRouter(buildServer(ad, readiness, metrics)),
		ReadHeaderTimeout: 5 * time.Second,
	}
	ln, err := net.Listen("tcp", cfg.httpAddr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.httpAddr, err)
	}
	if onListen != nil {
		onListen(ln.Addr().String())
	}
	relay := startRelay(cfg, ad, logger)
	return serveUntilDone(ctx, logger, httpServer, ln, readiness, relay, cfg.drainDelay)
}

// serveUntilDone serves httpServer on ln until ctx is cancelled, SIGINT or
// SIGTERM arrives, or the listener or the relay fails, then shuts down in the
// fleet order: (1) /readyz flips to 503, (2) wait the drain delay, (3) drain
// HTTP, (4) stop and await the relay LAST so nothing committed above is
// stranded; the caller's deferred pool.Close runs after all of it.
func serveUntilDone(ctx context.Context, logger *slog.Logger, httpServer *http.Server, ln net.Listener,
	readiness *inboundhttp.Readiness, relay *worker, drainDelay time.Duration,
) error {
	errCh := make(chan error, 1)
	go func() {
		logger.Info("http server listening", "addr", ln.Addr().String())
		if err := httpServer.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	sigCtx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	select {
	case err := <-errCh:
		relay.stop()
		relay.await(logger)
		return err
	case <-relay.done:
		// The relay returned on its own: the service cannot run without it
		// and must not sit "alive" with a dead relay, so exit non-zero and
		// let the orchestrator restart it.
		_ = httpServer.Close()
		relay.await(logger)
		return fmt.Errorf("the outbox relay stopped unexpectedly: %w", relay.err())
	case <-sigCtx.Done():
	}

	readiness.SetNotReady()
	if drainDelay > 0 {
		logger.Info("shutdown: readiness flipped to not-ready; waiting for traffic to drain", "drain_delay", drainDelay.String())
		time.Sleep(drainDelay)
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	err := httpServer.Shutdown(shutdownCtx)
	relay.stop()
	relay.await(logger)
	return err
}

// newLogger builds the JSON logger; LOG_LEVEL debug|info|warn|warning|error
// (case-insensitive; "warning" is accepted like "warn" fleet-wide).
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
