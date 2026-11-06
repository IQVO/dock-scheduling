// Package telemetry is the Prometheus adapter: the GET /metrics handler, the
// HTTP request counters the REST adapter reports into, and the counters of the
// facts the yard and ASN policies deliberately ignore (ports.Metrics).
package telemetry

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/claudioed/dock-scheduling/internal/application/ports"
)

// Metrics owns a private registry (the Go runtime and process collectors plus
// the service counters) and implements ports.Metrics.
type Metrics struct {
	registry   *prometheus.Registry
	requests   *prometheus.CounterVec
	durations  *prometheus.HistogramVec
	ignored    *prometheus.CounterVec
	implicitly prometheus.Counter
}

var _ ports.Metrics = (*Metrics)(nil)

// New builds the registry and registers every collector.
func New() (*Metrics, error) {
	m := &Metrics{
		registry: prometheus.NewRegistry(),
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "dock_scheduling_http_requests_total", Help: "HTTP requests by method, route pattern and status.",
		}, []string{"method", "route", "status"}),
		durations: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "dock_scheduling_http_request_duration_seconds", Help: "HTTP request duration by method and route pattern.",
			Buckets: prometheus.DefBuckets,
		}, []string{"method", "route"}),
		ignored: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "dock_scheduling_ignored_facts_total", Help: "Yard or ASN facts that changed nothing, by reason.",
		}, []string{"reason"}),
		implicitly: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "dock_scheduling_implicit_arrivals_total", Help: "Door releases applied to a BOOKED appointment (arrival implied).",
		}),
	}
	for _, c := range []prometheus.Collector{
		collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		m.requests, m.durations, m.ignored, m.implicitly,
	} {
		if err := m.registry.Register(c); err != nil {
			return nil, err
		}
	}
	return m, nil
}

// Handler is the GET /metrics scrape handler.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}

// ObserveHTTP records one finished request; route is the router pattern.
func (m *Metrics) ObserveHTTP(method, route string, status int, took time.Duration) {
	m.requests.WithLabelValues(method, route, statusLabel(status)).Inc()
	m.durations.WithLabelValues(method, route).Observe(took.Seconds())
}

// IgnoredFact implements ports.Metrics.
func (m *Metrics) IgnoredFact(reason string) { m.ignored.WithLabelValues(reason).Inc() }

// ImplicitArrival implements ports.Metrics.
func (m *Metrics) ImplicitArrival() { m.implicitly.Inc() }

func statusLabel(status int) string { return strconv.Itoa(status) }
