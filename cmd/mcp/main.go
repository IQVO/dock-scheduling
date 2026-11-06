// Command mcp is the composition root of dock-scheduling's MCP server
// (docs/adr/0007-mcp-server.md): it wires the environment into the real
// adapters (Postgres through pgx), the adapters into the SAME use cases cmd/api
// serves over REST, and those into the inbound MCP adapter, then serves MCP over
// Streamable HTTP and nothing else (no stdio, no SSE endpoint of its own). It is
// a second, independent deployable beside cmd/api.
//
// The five read tools and the four attributed, idempotent write tools run in
// one unit of work with the outbox rows of their events; this binary starts NO
// outbox relay and NO Kafka consumer (those run in cmd/api / cmd/relay), so
// the events of an agent's booking leave through the relay of the API. There
// is no in-memory mode and no log sink (fleet decision 2026-10-17). There is no
// auth of any kind (fleet-wide revert 2026-09-11): access control is the
// in-cluster ClusterIP boundary.
//
// Environment:
//
//	MCP_ADDR                    listen address (default :8090)
//	DATABASE_URL                Postgres DSN (REQUIRED)
//	MIGRATIONS_DATABASE_URL     direct DSN for the boot migrations (default DATABASE_URL)
//	KAFKA_BROKERS               comma-separated brokers (REQUIRED: /readyz, and the relay that drains this binary's outbox rows)
//	SHUTDOWN_DRAIN_DELAY        wait after flipping /readyz before closing (default 5s)
//	LOG_LEVEL                   debug|info|warn|error (default info)
//
// GET /readyz is 200 only while Postgres answers a ping AND a Kafka broker
// answers a topic-less metadata request; /healthz is liveness only; /metrics is
// the Prometheus scrape. The MCP endpoint is /mcp (and /).
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
	inboundmcp "github.com/claudioed/dock-scheduling/internal/adapters/inbound/mcp"
	"github.com/claudioed/dock-scheduling/internal/adapters/outbound/telemetry"
)

// shutdownTimeout bounds the HTTP server's graceful drain.
const shutdownTimeout = 10 * time.Second

func main() { os.Exit(realMain()) }

// realMain is main with a return code, so deferred cleanups run before the exit.
func realMain() int {
	if err := run(context.Background()); err != nil {
		newLogger("info").Error("dock-scheduling mcp exited with error", "error", err)
		return 1
	}
	return 0
}

// run boots the server from the process environment and serves until
// SIGINT/SIGTERM or until ctx is cancelled (the integration test stops the real
// entry point that way). A clean stop returns nil.
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
	// HTTP drain.
	ad, closeAdapters, err := buildAdapters(ctx, cfg, metrics, logger)
	if err != nil {
		return err
	}
	defer closeAdapters()

	readiness := newReadiness(ad.pool.Ping, cfg.brokerList)
	httpServer := &http.Server{
		Handler:           newRouter(inboundmcp.Handler(inboundmcp.NewServer(ad.deps)), readiness, metrics),
		ReadHeaderTimeout: 5 * time.Second,
	}
	ln, err := net.Listen("tcp", cfg.addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.addr, err)
	}
	if onListen != nil {
		onListen(ln.Addr().String())
	}
	return serveUntilDone(ctx, logger, httpServer, ln, readiness, cfg.drainDelay)
}

// serveUntilDone serves httpServer on ln until ctx is cancelled, SIGINT or
// SIGTERM arrives, or the listener fails, then shuts down in the fleet order:
// (1) /readyz flips to 503, (2) wait the drain delay, (3) drain HTTP; the
// caller's deferred pool.Close runs after all of it.
func serveUntilDone(ctx context.Context, logger *slog.Logger, httpServer *http.Server, ln net.Listener,
	readiness *inboundhttp.Readiness, drainDelay time.Duration,
) error {
	errCh := make(chan error, 1)
	go func() {
		logger.Info("mcp server listening (Streamable HTTP)", "addr", ln.Addr().String())
		if err := httpServer.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	sigCtx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	select {
	case err := <-errCh:
		return err
	case <-sigCtx.Done():
	}

	readiness.SetNotReady()
	if drainDelay > 0 {
		logger.Info("shutdown: readiness flipped to not-ready; waiting for traffic to drain", "drain_delay", drainDelay.String())
		time.Sleep(drainDelay)
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	return httpServer.Shutdown(shutdownCtx)
}

// newLogger builds the JSON logger; LOG_LEVEL debug|info|warn|warning|error
// (case-insensitive).
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
