package http_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	inboundhttp "github.com/claudioed/dock-scheduling/internal/adapters/inbound/http"
)

func TestNULInAPathQueryOrBodyIsAClientError(t *testing.T) {
	e := newEnv(t)
	wantProblem(t, e.get("/appointments/appt-%00"), 404, "not-found")
	wantProblem(t, e.get("/doors/D%00IN/availability"), 404, "not-found")
	wantProblem(t, e.get("/appointments?siteId=WH%001"), 400, "malformed-request-body")
}

func TestNULInABodyIsRefusedWhateverItsEncoding(t *testing.T) {
	e := newEnv(t)
	for name, body := range map[string]string{
		"raw byte":                        "{\"carrier\":\"a\x00b\"}",
		"json escape":                     `{"carrier":"a\u0000b"}`,
		"after an escaped backslash pair": `{"carrier":"\\\u0000"}`,
	} {
		t.Run(name, func(t *testing.T) { wantProblem(t, e.post("/appointments", body), 400, "malformed-request-body") })
	}
	// An escaped backslash followed by the text u0000 is not a NUL.
	body := strings.Replace(inboundBody("D-IN", at8, at10, "ASN-1"), "ACME Freight", `ACME \\u0000 Freight`, 1)
	wantStatus(t, e.post("/appointments", body), http.StatusCreated)
}

func TestNotFoundAndMethodNotAllowedAreProblems(t *testing.T) {
	e := newEnv(t)
	wantProblem(t, e.get("/nope"), 404, "not-found")
	wantProblem(t, e.do(req{method: http.MethodDelete, path: "/appointments"}), 405, "method-not-allowed")
	wantProblem(t, e.get("/appointments?q=%zz"), 400, "invalid-query-parameter")
}

func TestProbesAndMetrics(t *testing.T) {
	e := newEnv(t)
	rec := e.get("/healthz")
	wantStatus(t, rec, http.StatusOK)
	if decode(t, rec)["status"] != "ok" {
		t.Fatalf("healthz: %s", rec.Body.String())
	}
	rec = e.get("/readyz")
	wantStatus(t, rec, http.StatusOK)
	if decode(t, rec)["status"] != "ready" {
		t.Fatalf("readyz: %s", rec.Body.String())
	}
	if got := e.get("/metrics"); got.Code != http.StatusNotFound {
		t.Fatalf("/metrics is unregistered without a handler, got %d", got.Code)
	}
	e.server.Metrics = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("# metrics\n")) })
	e.router = inboundhttp.NewRouter(e.server)
	if got := e.get("/metrics"); got.Code != http.StatusOK || got.Body.String() != "# metrics\n" {
		t.Fatalf("/metrics serves the handler: %d %q", got.Code, got.Body.String())
	}
}

func TestReadyzReportsDrainAndDependencies(t *testing.T) {
	e := newEnv(t)
	healthy := true
	e.server.Readiness = inboundhttp.NewReadiness(time.Nanosecond, time.Second,
		inboundhttp.Dependency{Name: "postgres", Check: func(context.Context) error { return nil }},
		inboundhttp.Dependency{Name: "kafka", Check: func(context.Context) error {
			if healthy {
				return nil
			}
			return errors.New("no broker")
		}},
	)
	e.router = inboundhttp.NewRouter(e.server)
	wantStatus(t, e.get("/readyz"), http.StatusOK)

	healthy = false
	time.Sleep(2 * time.Millisecond) // the verdict cache is one nanosecond
	p := wantProblem(t, e.get("/readyz"), 503, "dependency-unavailable")
	if p["detail"] != "not ready: kafka" {
		t.Fatalf("the problem names the failing dependency: %v", p["detail"])
	}
	// Liveness is independent of the dependencies.
	wantStatus(t, e.get("/healthz"), http.StatusOK)

	healthy = true
	time.Sleep(2 * time.Millisecond)
	wantStatus(t, e.get("/readyz"), http.StatusOK)
	e.server.Readiness.SetNotReady()
	rec := e.get("/readyz")
	wantStatus(t, rec, http.StatusServiceUnavailable)
	if decode(t, rec)["status"] != "not_ready" {
		t.Fatalf("draining answers not_ready: %s", rec.Body.String())
	}
	wantStatus(t, e.get("/healthz"), http.StatusOK)
}

func TestObserveSeesTheRoutePatternNotTheRawPath(t *testing.T) {
	e := newEnv(t)
	type seen struct {
		method, route string
		status        int
	}
	var got []seen
	e.server.Observe = func(method, route string, status int, took time.Duration) {
		if took < 0 {
			t.Error("negative duration")
		}
		got = append(got, seen{method, route, status})
	}
	e.router = inboundhttp.NewRouter(e.server)
	id := e.book("D-IN", at8, at10, "ASN-1")
	e.get("/appointments/" + id)
	e.get("/nope")
	want := []seen{{"POST", "/appointments", 201}, {"GET", "/appointments/{appointmentId}", 200}, {"GET", "unmatched", 404}}
	if len(got) != len(want) {
		t.Fatalf("observed %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("observed[%d] = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestAPanickingHandlerIsA500Problem(t *testing.T) {
	srv := &inboundhttp.Server{} // no Service: the handler dereferences nil
	rec := httptest.NewRecorder()
	inboundhttp.NewRouter(srv).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/appointments/appt-00000000-0000-0000-0000-000000000000", nil))
	wantProblem(t, rec, 500, "internal-error")
}
