package http_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	inboundhttp "github.com/claudioed/dock-scheduling/internal/adapters/inbound/http"
	"github.com/claudioed/dock-scheduling/internal/application/repository"
)

type fakeAdherence struct {
	rows []repository.AdherenceRow
	err  error
	got  repository.AdherenceQuery
}

func (f *fakeAdherence) ScheduleAdherence(_ context.Context, q repository.AdherenceQuery) ([]repository.AdherenceRow, error) {
	f.got = q
	return f.rows, f.err
}

type fakeFreshness struct {
	asOf *time.Time
	err  error
}

func (f fakeFreshness) Freshness(context.Context) (*time.Time, error) { return f.asOf, f.err }

const adherenceRange = "?from=2030-01-01T00:00:00Z&to=2030-02-01T00:00:00Z"

func reportsGet(rs *inboundhttp.ReportsServer, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	inboundhttp.NewReportsRouter(rs).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestTheOLTPRouterDoesNotServeTheReports(t *testing.T) {
	e := newEnv(t)
	e.server.Adherence = &fakeAdherence{}
	e.router = inboundhttp.NewRouter(e.server)
	wantProblem(t, e.get("/reports/schedule-adherence"+adherenceRange), 404, "not-found")
	wantProblem(t, e.get("/reports/freshness"), 404, "not-found")
}

func TestScheduleAdherenceReportComputesTotalsAndShares(t *testing.T) {
	reader := &fakeAdherence{rows: []repository.AdherenceRow{
		{SiteID: "WH1", CarrierSCAC: "ACMF", OnTime: 6, Early: 1, Late: 2, NoShow: 1},
		{SiteID: "WH1", CarrierSCAC: "-"},
	}}
	rec := reportsGet(&inboundhttp.ReportsServer{Adherence: reader}, "/reports/schedule-adherence"+adherenceRange+"&siteId=WH1&carrierScac=ACMF")
	wantStatus(t, rec, http.StatusOK)
	if reader.got.SiteID != "WH1" || reader.got.CarrierSCAC != "ACMF" || !reader.got.From.Equal(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("the query reaches the read side: %+v", reader.got)
	}
	items := decode(t, rec)["items"].([]any)
	first := items[0].(map[string]any)
	if first["total"] != float64(10) || first["onTimeShare"] != 0.6 || first["earlyShare"] != 0.1 || first["lateShare"] != 0.2 || first["noShowShare"] != 0.1 {
		t.Fatalf("totals and shares: %v", first)
	}
	if empty := items[1].(map[string]any); empty["total"] != float64(0) || empty["onTimeShare"] != float64(0) {
		t.Fatalf("a row with no appointments has zero shares, not NaN: %v", empty)
	}
}

func TestScheduleAdherenceReportIsPerSiteAndCarrier(t *testing.T) {
	reader := &fakeAdherence{rows: []repository.AdherenceRow{
		{SiteID: "WH1", CarrierSCAC: "ACMF", OnTime: 3, Late: 1},
		{SiteID: "WH1", CarrierSCAC: "-", OnTime: 1},
		{SiteID: "WH2", CarrierSCAC: "ACMF", NoShow: 2},
	}}
	rec := reportsGet(&inboundhttp.ReportsServer{Adherence: reader}, "/reports/schedule-adherence"+adherenceRange)
	wantStatus(t, rec, http.StatusOK)
	if reader.got.SiteID != "" || reader.got.CarrierSCAC != "" {
		t.Fatalf("no filter was asked for: %+v", reader.got)
	}
	items := decode(t, rec)["items"].([]any)
	if len(items) != 3 {
		t.Fatalf("one item per site and carrier, got %v", items)
	}
	for i, want := range []struct {
		site, scac string
		total      float64
		onTime     float64
	}{{"WH1", "ACMF", 4, 0.75}, {"WH1", "-", 1, 1}, {"WH2", "ACMF", 2, 0}} {
		it := items[i].(map[string]any)
		if it["siteId"] != want.site || it["carrierScac"] != want.scac || it["total"] != want.total || it["onTimeShare"] != want.onTime {
			t.Errorf("item %d = %v, want %+v", i, it, want)
		}
	}
	if noShow := items[2].(map[string]any); noShow["noShow"] != float64(2) || noShow["noShowShare"] != float64(1) {
		t.Errorf("a site with only no-shows: %v", noShow)
	}
}

func TestScheduleAdherenceReportWithNoRowsKeepsTheContractShape(t *testing.T) {
	rec := reportsGet(&inboundhttp.ReportsServer{Adherence: &fakeAdherence{}}, "/reports/schedule-adherence"+adherenceRange)
	wantStatus(t, rec, http.StatusOK)
	b := decode(t, rec)
	if items, ok := b["items"].([]any); !ok || len(items) != 0 || b["from"] != "2030-01-01T00:00:00Z" || b["to"] != "2030-02-01T00:00:00Z" {
		t.Fatalf("an empty report keeps the contract shape: %s", rec.Body.String())
	}
}

func TestScheduleAdherenceReportRefusals(t *testing.T) {
	rs := &inboundhttp.ReportsServer{Adherence: &fakeAdherence{}}
	for _, bad := range []string{"", "?from=2030-01-01T00:00:00Z", "?from=2030-02-01T00:00:00Z&to=2030-01-01T00:00:00Z", "?from=2030-01-01T00:00:00Z&to=2030-06-01T00:00:00Z"} {
		wantProblem(t, reportsGet(rs, "/reports/schedule-adherence"+bad), 400, "invalid-report-range")
	}
	wantProblem(t, reportsGet(rs, "/reports/schedule-adherence?from=nope&to=2030-01-01T00:00:00Z"), 400, "invalid-query-parameter")
	wantProblem(t, reportsGet(rs, "/reports/schedule-adherence"+adherenceRange+"&limit=5"), 400, "unknown-query-parameter")

	failing := &inboundhttp.ReportsServer{Adherence: &fakeAdherence{err: errors.New("db down")}}
	wantProblem(t, reportsGet(failing, "/reports/schedule-adherence"+adherenceRange), 500, "internal-error")
}

func TestFreshnessIsTheLagBehindTheNewestAppliedEvent(t *testing.T) {
	now := time.Date(2030, 1, 7, 12, 0, 0, 0, time.UTC)
	asOf := now.Add(-90 * time.Second)
	rs := &inboundhttp.ReportsServer{Freshness: fakeFreshness{asOf: &asOf}, Now: func() time.Time { return now }}
	rec := reportsGet(rs, "/reports/freshness")
	wantStatus(t, rec, http.StatusOK)
	b := decode(t, rec)
	if b["asOf"] != "2030-01-07T11:58:30Z" || b["lagSeconds"] != float64(90) {
		t.Fatalf("freshness = %s", rec.Body.String())
	}

	// A clock behind the event is a lag of zero, never negative.
	ahead := now.Add(time.Minute)
	rs.Freshness = fakeFreshness{asOf: &ahead}
	if b := decode(t, reportsGet(rs, "/reports/freshness")); b["lagSeconds"] != float64(0) {
		t.Fatalf("lag must not be negative: %v", b)
	}
}

func TestFreshnessBeforeTheFirstEventIsNull(t *testing.T) {
	rec := reportsGet(&inboundhttp.ReportsServer{Freshness: fakeFreshness{}}, "/reports/freshness")
	wantStatus(t, rec, http.StatusOK)
	b := decode(t, rec)
	if v, ok := b["asOf"]; !ok || v != nil {
		t.Fatalf("asOf must be present and null: %s", rec.Body.String())
	}
	if v, ok := b["lagSeconds"]; !ok || v != nil {
		t.Fatalf("lagSeconds must be present and null: %s", rec.Body.String())
	}
}

func TestFreshnessRefusals(t *testing.T) {
	wantProblem(t, reportsGet(&inboundhttp.ReportsServer{Freshness: fakeFreshness{}}, "/reports/freshness?x=1"), 400, "unknown-query-parameter")
	wantProblem(t, reportsGet(&inboundhttp.ReportsServer{Freshness: fakeFreshness{err: errors.New("db down")}}, "/reports/freshness"), 500, "internal-error")
}

func TestReportsRouterProbesAndUnknownPaths(t *testing.T) {
	rs := &inboundhttp.ReportsServer{}
	wantStatus(t, reportsGet(rs, "/healthz"), http.StatusOK)
	wantStatus(t, reportsGet(rs, "/readyz"), http.StatusOK)
	wantProblem(t, reportsGet(rs, "/appointments"), 404, "not-found")
	if got := reportsGet(rs, "/metrics"); got.Code != http.StatusNotFound {
		t.Fatalf("/metrics is unregistered without a handler, got %d", got.Code)
	}

	gate := inboundhttp.NewReadiness(0, 0, inboundhttp.Dependency{Name: "analytics-postgres", Check: func(context.Context) error { return errors.New("down") }})
	wantProblem(t, reportsGet(&inboundhttp.ReportsServer{Readiness: gate}, "/readyz"), 503, "dependency-unavailable")
	gate.SetNotReady()
	wantStatus(t, reportsGet(&inboundhttp.ReportsServer{Readiness: gate}, "/readyz"), http.StatusServiceUnavailable)
}
