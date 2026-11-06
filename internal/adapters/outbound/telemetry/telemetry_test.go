package telemetry

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func scrape(t *testing.T, m *Metrics) string {
	t.Helper()
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("scrape status = %d", rec.Code)
	}
	return rec.Body.String()
}

func TestMetricsExposeTheServiceCounters(t *testing.T) {
	m, err := New()
	if err != nil {
		t.Fatal(err)
	}
	m.ObserveHTTP("POST", "/appointments", 201, 30*time.Millisecond)
	m.IgnoredFact("terminal_appointment")
	m.IgnoredFact("terminal_appointment")
	m.ImplicitArrival()
	body := scrape(t, m)
	for _, want := range []string{
		`dock_scheduling_http_requests_total{method="POST",route="/appointments",status="201"} 1`,
		`dock_scheduling_http_request_duration_seconds_count{method="POST",route="/appointments"} 1`,
		`dock_scheduling_ignored_facts_total{reason="terminal_appointment"} 2`,
		`dock_scheduling_implicit_arrivals_total 1`,
		`go_goroutines`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("scrape lacks %q", want)
		}
	}
}
