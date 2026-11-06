// Package http is the REST inbound adapter: every route of apis/openapi.yaml
// on a chi router, RFC 7807 problem documents for every error, ETag/If-Match on
// the appointment, the Idempotency-Key middleware on every POST and the
// liveness/readiness/metrics endpoints. It has no auth middleware (fleet
// decision 2026-09-11; internal/architecture's TestNoAuthMiddlewareReintroduced).
package http

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/claudioed/dock-scheduling/internal/application/ports"
	"github.com/claudioed/dock-scheduling/internal/application/usecases"
)

// Server holds the use cases and the ports the handlers and middleware call.
type Server struct {
	// Service runs every command and query.
	Service *usecases.Service

	// Idempotency and UoW back the Idempotency-Key middleware of every POST;
	// the shipped binary always sets both. A nil store (handler unit tests)
	// leaves the POST routes unwrapped.
	Idempotency ports.IdempotencyStore
	UoW         ports.UnitOfWork
	// Now stamps the stored idempotency answers (time.Now when nil).
	Now func() time.Time

	// Adherence backs GET /reports/schedule-adherence (D-13). Nil (the
	// analytics read side has not landed) serves the contract shape with no rows.
	Adherence ports.AdherenceReader

	// Readiness backs GET /readyz: the drain gate plus the Postgres and Kafka
	// checks (NewReadiness). Nil (handler unit tests) means always ready.
	Readiness *Readiness
	// Metrics backs GET /metrics (a Prometheus handler built by cmd/); nil
	// leaves the route unregistered.
	Metrics http.Handler
	// Observe is told about every finished request (route pattern, never the
	// raw path, so labels stay bounded); nil observes nothing.
	Observe func(method, route string, status int, took time.Duration)
}

// NewRouter builds the chi router for every route of apis/openapi.yaml.
func NewRouter(s *Server) http.Handler {
	r := chi.NewRouter()
	r.Use(s.observe)
	r.Use(recoverPanic)
	// A NUL (U+0000) in the path, the query or a body is a uniform client
	// error, never a Postgres 22021 -> 500; it runs before the route-scoped
	// idempotency middleware so a rejected request never records its key.
	r.Use(rejectNUL)
	r.Use(rejectBadQuery)
	r.Get("/healthz", handleHealthz)
	r.Get("/readyz", s.handleReadyz)
	if s.Metrics != nil {
		r.Method(http.MethodGet, "/metrics", s.Metrics)
	}

	keyed := s.keyed(false)
	r.With(keyed).Post("/appointments", s.handleBook)
	r.Get("/appointments", s.handleList)
	r.Get("/appointments/{appointmentId}", s.handleGet)
	r.With(keyed).Post("/appointments/{appointmentId}/reschedules", s.handleReschedule)
	r.With(keyed).Post("/appointments/{appointmentId}/cancellations", s.handleCancel)
	r.With(keyed).Post("/appointments/{appointmentId}/eta-reports", s.handleEta)
	r.Get("/doors/{doorCode}/availability", s.handleAvailability)

	r.Get("/sites/{siteId}/dock-calendar", s.handleGetCalendar)
	r.Put("/sites/{siteId}/dock-calendar/operating-hours", s.handlePutHours)
	r.Put("/sites/{siteId}/dock-calendar/settings", s.handlePutSettings)
	r.With(keyed).Post("/sites/{siteId}/dock-calendar/closures", s.handleScheduleClosure)
	r.With(keyed).Post("/sites/{siteId}/dock-calendar/closures/{closureId}/lifts", s.handleLiftClosure)

	r.With(s.keyed(true)).Post("/no-show-sweeps", s.handleNoShowSweep)
	r.Get("/reports/schedule-adherence", s.handleScheduleAdherence)

	r.NotFound(func(w http.ResponseWriter, r *http.Request) { writeProblem(w, r, notFoundResource, "no such path") })
	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		writeProblem(w, r, problem{http.StatusMethodNotAllowed, "method-not-allowed", "Method not allowed"}, "the method is not supported on this path")
	})
	return r
}

// keyed returns the Idempotency-Key middleware (a pass-through without a store).
func (s *Server) keyed(detached bool) func(http.Handler) http.Handler {
	if s.Idempotency == nil || s.UoW == nil {
		return func(next http.Handler) http.Handler { return next }
	}
	now := s.Now
	if now == nil {
		now = time.Now
	}
	m := &idempotency{store: s.Idempotency, uow: s.UoW, now: now}
	return func(next http.Handler) http.Handler { return m.wrap(next, detached) }
}

// statusRecorder remembers the status a handler wrote.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

// observe reports each finished request to Server.Observe by route pattern.
func (s *Server) observe(next http.Handler) http.Handler {
	if s.Observe == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		route := "unmatched"
		if rc := chi.RouteContext(r.Context()); rc != nil && rc.RoutePattern() != "" {
			route = rc.RoutePattern()
		}
		if rec.status == 0 {
			rec.status = http.StatusOK
		}
		s.Observe(r.Method, route, rec.status, time.Since(start))
	})
}

// recoverPanic turns a handler panic into a 500 problem; the panic value is
// logged, never echoed.
func recoverPanic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if p := recover(); p != nil && p != http.ErrAbortHandler {
				slog.ErrorContext(r.Context(), "handler panicked", "panic", p, "method", sanitizeLog(r.Method), "path", sanitizeLog(r.URL.Path))
				writeProblem(w, r, internalProblem, "an unexpected error occurred")
			}
		}()
		next.ServeHTTP(w, r)
	})
}
