package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	inboundhttp "github.com/claudioed/dock-scheduling/internal/adapters/inbound/http"
	"github.com/claudioed/dock-scheduling/internal/adapters/outbound/telemetry"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func envOf(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestLoadConfigNamesEveryMissingVariable(t *testing.T) {
	_, err := loadConfig(envOf(nil), quiet())
	if err == nil {
		t.Fatal("an empty environment must be a boot error")
	}
	for _, name := range []string{"DATABASE_URL", "KAFKA_BROKERS"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error %q does not name %s", err, name)
		}
	}
	if !strings.Contains(err.Error(), "no in-memory mode") {
		t.Errorf("error %q does not say there is no in-memory mode", err)
	}

	_, err = loadConfig(envOf(map[string]string{"DATABASE_URL": "postgres://x"}), quiet())
	if err == nil || !strings.Contains(err.Error(), "KAFKA_BROKERS") || strings.Contains(err.Error(), "DATABASE_URL") {
		t.Errorf("only KAFKA_BROKERS is missing, got %v", err)
	}
	_, err = loadConfig(envOf(map[string]string{"KAFKA_BROKERS": " , "}), quiet())
	if err == nil || !strings.Contains(err.Error(), "KAFKA_BROKERS") || !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Errorf("blank brokers are missing brokers, and DATABASE_URL is missing too, got %v", err)
	}
}

func TestLoadConfigDefaultsAndOverrides(t *testing.T) {
	cfg, err := loadConfig(envOf(map[string]string{"DATABASE_URL": "postgres://app", "KAFKA_BROKERS": "k1:9092, k2:9092"}), quiet())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.addr != ":8090" || cfg.databaseURL != "postgres://app" || cfg.migrationsURL != "postgres://app" {
		t.Errorf("defaults = %+v", cfg)
	}
	if len(cfg.brokerList) != 2 || cfg.brokerList[1] != "k2:9092" {
		t.Errorf("brokers = %v", cfg.brokerList)
	}
	if cfg.drainDelay != 5*time.Second {
		t.Errorf("drain = %v", cfg.drainDelay)
	}

	cfg, err = loadConfig(envOf(map[string]string{
		"DATABASE_URL": "postgres://pgbouncer", "MIGRATIONS_DATABASE_URL": "postgres://direct", "KAFKA_BROKERS": "k:9092",
		"MCP_ADDR": ":9999", "SHUTDOWN_DRAIN_DELAY": "0", "LOG_LEVEL": "debug",
	}), quiet())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.addr != ":9999" || cfg.migrationsURL != "postgres://direct" || cfg.databaseURL != "postgres://pgbouncer" || cfg.drainDelay != 0 || cfg.logLevel != "debug" {
		t.Errorf("overrides = %+v", cfg)
	}
}

// runWith refuses to boot on a bad environment before anything is opened.
func TestRunWithRefusesToBootWithoutItsInfrastructure(t *testing.T) {
	err := runWith(context.Background(), envOf(nil), func(string) { t.Error("must not listen") })
	if err == nil || !strings.Contains(err.Error(), "DATABASE_URL") || !strings.Contains(err.Error(), "KAFKA_BROKERS") {
		t.Fatalf("err = %v, want a boot error naming DATABASE_URL and KAFKA_BROKERS", err)
	}
}

func get(h http.Handler, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func marker(body string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) })
}

func okReadiness() *inboundhttp.Readiness {
	return inboundhttp.NewReadiness(time.Millisecond, time.Second, inboundhttp.Dependency{Name: "postgres", Check: func(context.Context) error { return nil }})
}

func TestRouterServesProbesAndTheMCPEndpointOnBothPaths(t *testing.T) {
	r := newRouter(marker("mcp"), okReadiness(), nil)
	if rec := get(r, "/healthz"); rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != `{"status":"ok"}` {
		t.Errorf("healthz = %d %s", rec.Code, rec.Body)
	}
	if rec := get(r, "/readyz"); rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != `{"status":"ready"}` {
		t.Errorf("readyz = %d %s", rec.Code, rec.Body)
	}
	for _, path := range []string{"/mcp", "/", "/mcp/anything"} {
		if rec := get(r, path); rec.Code != 200 || rec.Body.String() != "mcp" {
			t.Errorf("GET %s = %d %q, want the MCP handler", path, rec.Code, rec.Body)
		}
	}
	if rec := get(r, "/metrics"); rec.Body.String() != "mcp" {
		t.Errorf("without metrics /metrics falls to the MCP handler, got %q", rec.Body)
	}
}

func TestReadyzReportsDrainingAndFailingDependencies(t *testing.T) {
	down := errors.New("broker unreachable")
	readiness := inboundhttp.NewReadiness(time.Millisecond, time.Second,
		inboundhttp.Dependency{Name: "postgres", Check: func(context.Context) error { return nil }},
		inboundhttp.Dependency{Name: "kafka", Check: func(context.Context) error { return down }},
	)
	r := newRouter(marker("mcp"), readiness, nil)
	rec := get(r, "/readyz")
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), `"failing":["kafka"]`) || !strings.Contains(rec.Body.String(), "unavailable") {
		t.Errorf("failing dependency = %d %s", rec.Code, rec.Body)
	}
	if h := rec.Header().Get("Content-Type"); h != "application/json" {
		t.Errorf("content type = %q", h)
	}

	ready := okReadiness()
	drain := newRouter(marker("mcp"), ready, nil)
	ready.SetNotReady()
	if rec := get(drain, "/readyz"); rec.Code != http.StatusServiceUnavailable || strings.TrimSpace(rec.Body.String()) != `{"status":"not_ready"}` {
		t.Errorf("draining = %d %s", rec.Code, rec.Body)
	}
	if rec := get(drain, "/healthz"); rec.Code != 200 {
		t.Errorf("liveness must not flip on drain, got %d", rec.Code)
	}
}

func TestRouterKeepsFlusherForEventStreamsAndObservesByRoutePattern(t *testing.T) {
	metrics, err := telemetry.New()
	if err != nil {
		t.Fatal(err)
	}
	streaming := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		f, ok := w.(http.Flusher)
		if !ok {
			t.Error("the response writer lost http.Flusher: SSE answers would buffer")
			return
		}
		_, _ = w.Write([]byte("data: x\n\n"))
		f.Flush()
	})
	r := newRouter(streaming, okReadiness(), metrics)
	get(r, "/mcp/tools")
	get(r, "/other-path-with-an-id-123")
	get(r, "/healthz")

	scrape := get(r, "/metrics").Body.String()
	for _, want := range []string{
		`dock_scheduling_http_requests_total{method="GET",route="/mcp",status="200"} 1`,
		`dock_scheduling_http_requests_total{method="GET",route="/",status="200"} 1`,
		`dock_scheduling_http_requests_total{method="GET",route="/healthz",status="200"} 1`,
	} {
		if !strings.Contains(scrape, want) {
			t.Errorf("metrics lack %q:\n%s", want, grep(scrape, "dock_scheduling_http_requests_total"))
		}
	}
	if strings.Contains(scrape, "other-path-with-an-id") {
		t.Error("a raw path leaked into a metric label")
	}
}

func grep(text, needle string) string {
	var out []string
	for _, l := range strings.Split(text, "\n") {
		if strings.Contains(l, needle) {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}

func TestRouterSurvivesAPanickingHandler(t *testing.T) {
	r := newRouter(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") }), okReadiness(), nil)
	if rec := get(r, "/mcp"); rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	if rec := get(r, "/healthz"); rec.Code != 200 {
		t.Errorf("the router is dead after a panic: %d", rec.Code)
	}
}

func TestNewLoggerLevels(t *testing.T) {
	for level, want := range map[string]slog.Level{"debug": slog.LevelDebug, "WARN": slog.LevelWarn, "warning": slog.LevelWarn, "error": slog.LevelError, "": slog.LevelInfo, "nonsense": slog.LevelInfo} {
		if !newLogger(level).Enabled(context.Background(), want) || (want > slog.LevelDebug && newLogger(level).Enabled(context.Background(), want-1)) {
			t.Errorf("level %q: not exactly %v", level, want)
		}
	}
}
