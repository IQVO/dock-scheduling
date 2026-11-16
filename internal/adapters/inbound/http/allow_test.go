package http_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	inboundhttp "github.com/claudioed/dock-scheduling/internal/adapters/inbound/http"
)

// A 405 MUST list the methods the resource does support (RFC 9110 §15.5.6).
// chi sets Allow only in its DEFAULT 405 handler; the RFC 7807 handler this
// service registers replaces it, so the list is derived from the router.
type allowCase struct{ method, path, wantAllow string }

func assertAllow(t *testing.T, router http.Handler, c allowCase) {
	t.Helper()
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(c.method, c.path, http.NoBody))
	wantProblem(t, rec, http.StatusMethodNotAllowed, "method-not-allowed")
	if got := rec.Header().Get("Allow"); got != c.wantAllow {
		t.Errorf("%s %s: Allow = %q, want %q", c.method, c.path, got, c.wantAllow)
	}
}

func TestOLTPRouterWrongMethodCarriesAllow(t *testing.T) {
	router := newEnv(t).router
	for _, c := range []allowCase{
		{http.MethodDelete, "/healthz", "GET"},
		{http.MethodPost, "/readyz", "GET"},
		{http.MethodDelete, "/appointments", "GET, POST"},
		{http.MethodPut, "/appointments/appt-1", "GET"},
		{http.MethodGet, "/appointments/appt-1/reschedules", "POST"},
		{http.MethodDelete, "/doors/D-1/availability", "GET"},
		{http.MethodPost, "/sites/S-1/dock-calendar", "GET"},
		{http.MethodGet, "/sites/S-1/dock-calendar/operating-hours", "PUT"},
		{http.MethodGet, "/sites/S-1/dock-calendar/closures", "POST"},
		{http.MethodDelete, "/carriers", "GET"},
		{http.MethodPost, "/carriers/ACME", "GET, PUT, PATCH"},
		{http.MethodGet, "/no-show-sweeps", "POST"},
	} {
		t.Run(c.method+" "+c.path, func(t *testing.T) { assertAllow(t, router, c) })
	}
}

func TestReportsRouterWrongMethodCarriesAllow(t *testing.T) {
	router := inboundhttp.NewReportsRouter(&inboundhttp.ReportsServer{Metrics: http.NotFoundHandler()})
	for _, c := range []allowCase{
		{http.MethodPost, "/healthz", "GET"},
		{http.MethodPost, "/readyz", "GET"},
		{http.MethodPut, "/metrics", "GET"},
		{http.MethodDelete, "/reports/schedule-adherence", "GET"},
		{http.MethodPost, "/reports/freshness", "GET"},
	} {
		t.Run(c.method+" "+c.path, func(t *testing.T) { assertAllow(t, router, c) })
	}
}

// An unknown path is a 404, never a 405, so it carries no Allow.
func TestUnknownPathHasNoAllow(t *testing.T) {
	routers := map[string]http.Handler{
		"oltp":    newEnv(t).router,
		"reports": inboundhttp.NewReportsRouter(&inboundhttp.ReportsServer{}),
	}
	for name, router := range routers {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/nope", http.NoBody))
			wantProblem(t, rec, http.StatusNotFound, "not-found")
			if got := rec.Header().Get("Allow"); got != "" {
				t.Errorf("Allow = %q on a 404, want none", got)
			}
		})
	}
}
