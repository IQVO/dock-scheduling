package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	inboundhttp "github.com/claudioed/dock-scheduling/internal/adapters/inbound/http"
	"github.com/claudioed/dock-scheduling/internal/adapters/outbound/telemetry"
)

// newRouter wraps the MCP handler in a small chi router so the binary is
// deployable behind Kubernetes probes:
//
//   - GET /healthz -> 200 {"status":"ok"} (liveness, never flipped by shutdown).
//   - GET /readyz  -> 200 {"status":"ready"}; 503 {"status":"not_ready"} while
//     draining; 503 {"status":"unavailable","failing":[...]} while Postgres or
//     Kafka is unreachable.
//   - GET /metrics -> the Prometheus scrape (when metrics is non-nil).
//   - /mcp and /   -> the MCP Streamable HTTP handler. Both paths are served so
//     warehouse-ops-agent's *_MCP_ENDPOINT convention (".../mcp") and a
//     root-mounted endpoint both work.
//
// No auth middleware is ever added here (TestNoAuthMiddlewareReintroduced).
func newRouter(mcpHandler http.Handler, readiness *inboundhttp.Readiness, metrics *telemetry.Metrics) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	if metrics != nil {
		r.Use(observe(metrics.ObserveHTTP))
	}
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) { writeStatus(w, http.StatusOK, statusBody{Status: "ok"}) })
	r.Get("/readyz", func(w http.ResponseWriter, req *http.Request) {
		if !readiness.Ready() {
			writeStatus(w, http.StatusServiceUnavailable, statusBody{Status: "not_ready"})
			return
		}
		if failing := readiness.Failing(req.Context()); len(failing) > 0 {
			writeStatus(w, http.StatusServiceUnavailable, statusBody{Status: "unavailable", Failing: failing})
			return
		}
		writeStatus(w, http.StatusOK, statusBody{Status: "ready"})
	})
	if metrics != nil {
		r.Method(http.MethodGet, "/metrics", metrics.Handler())
	}
	r.Mount("/mcp", mcpHandler)
	r.Mount("/", mcpHandler)
	return r
}

type statusBody struct {
	Status  string   `json:"status"`
	Failing []string `json:"failing,omitempty"`
}

func writeStatus(w http.ResponseWriter, code int, body statusBody) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}

// observe tells the metrics about every finished request by route pattern,
// never the raw path, so the labels stay bounded. The wrapped writer keeps
// http.Flusher, which the Streamable HTTP server needs for event streams.
func observe(record func(method, route string, status int, took time.Duration)) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(ww, r)
			status := ww.Status()
			if status == 0 {
				status = http.StatusOK
			}
			record(r.Method, routePattern(r), status, time.Since(start))
		})
	}
}

// routePattern is the matched chi pattern, "unmatched" when none matched.
func routePattern(r *http.Request) string {
	if rc := chi.RouteContext(r.Context()); rc != nil {
		if p := strings.TrimSuffix(rc.RoutePattern(), "/*"); p != "" {
			return p
		}
		if rc.RoutePattern() != "" {
			return "/"
		}
	}
	return "unmatched"
}
