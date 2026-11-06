package http

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/claudioed/dock-scheduling/internal/application/ports"
)

// ReportsServer is what cmd/reports hands the reports router: the read side of
// the analytics store (ADR 0006). The reports are NOT served by the OLTP
// router (NewRouter): they read the analytical database, which only
// cmd/reports opens.
type ReportsServer struct {
	// Adherence backs GET /reports/schedule-adherence (D-13).
	Adherence ports.AdherenceReader
	// Freshness backs GET /reports/freshness.
	Freshness ports.FreshnessReader
	// Now is the clock of the freshness lag (time.Now when nil).
	Now func() time.Time
	// Readiness backs GET /readyz: the drain gate plus the analytical database
	// check. Nil means always ready (handler unit tests).
	Readiness *Readiness
	// Metrics backs GET /metrics; nil leaves the route unregistered.
	Metrics http.Handler
	// Observe is told about every finished request (route pattern); nil
	// observes nothing.
	Observe func(method, route string, status int, took time.Duration)
}

// NewReportsRouter builds the chi router of cmd/reports: the two report routes
// of apis/openapi.yaml (tag Reports) plus /healthz, /readyz and /metrics, with
// the same RFC 7807 errors and request hygiene as the OLTP router.
func NewReportsRouter(rs *ReportsServer) http.Handler {
	base := &Server{Adherence: rs.Adherence, Readiness: rs.Readiness, Metrics: rs.Metrics, Observe: rs.Observe}
	r := chi.NewRouter()
	r.Use(base.observe)
	r.Use(recoverPanic)
	r.Use(rejectNUL)
	r.Use(rejectBadQuery)
	r.Get("/healthz", handleHealthz)
	r.Get("/readyz", base.handleReadyz)
	if rs.Metrics != nil {
		r.Method(http.MethodGet, "/metrics", rs.Metrics)
	}
	r.Get("/reports/schedule-adherence", base.handleScheduleAdherence)
	r.Get("/reports/freshness", rs.handleFreshness)
	r.NotFound(func(w http.ResponseWriter, r *http.Request) { writeProblem(w, r, notFoundResource, "no such path") })
	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		writeProblem(w, r, problem{http.StatusMethodNotAllowed, "method-not-allowed", "Method not allowed"}, "the method is not supported on this path")
	})
	return r
}

type freshnessBody struct {
	// AsOf is the CloudEvents time of the newest event the projection applied;
	// null before the first one.
	AsOf *time.Time `json:"asOf"`
	// LagSeconds is now - asOf, never negative; null before the first event.
	LagSeconds *float64 `json:"lagSeconds"`
}

// handleFreshness is GET /reports/freshness: how far the projection is behind
// (the fleet analytics charter).
func (s *ReportsServer) handleFreshness(w http.ResponseWriter, r *http.Request) {
	if name := unknownParam(r); name != "" {
		writeError(w, r, errUnknownQuery(name))
		return
	}
	asOf, err := s.Freshness.Freshness(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	out := freshnessBody{}
	if asOf != nil {
		now := time.Now
		if s.Now != nil {
			now = s.Now
		}
		lag := now().Sub(*asOf).Seconds()
		if lag < 0 {
			lag = 0
		}
		u := asOf.UTC()
		out.AsOf, out.LagSeconds = &u, &lag
	}
	writeJSON(w, http.StatusOK, out)
}
