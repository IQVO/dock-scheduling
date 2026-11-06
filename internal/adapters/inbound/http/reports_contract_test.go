package http_test

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"gopkg.in/yaml.v3"

	inboundhttp "github.com/claudioed/dock-scheduling/internal/adapters/inbound/http"
	"github.com/claudioed/dock-scheduling/internal/application/repository"
)

// reportsSpecPath is the contract of the cmd/reports binary (ADR 0006).
var reportsSpecPath = filepath.Join("..", "..", "..", "..", "apis", "openapi-reports.yaml")

// specDoc is the subset of an OpenAPI 3.0 document these contract tests read.
type specDoc struct {
	Paths      map[string]map[string]yaml.Node `yaml:"paths"`
	Components struct {
		Schemas map[string]specSchema `yaml:"schemas"`
	} `yaml:"components"`
}

type specSchema struct {
	Required   []string              `yaml:"required"`
	Properties map[string]specSchema `yaml:"properties"`
	Items      *specSchema           `yaml:"items"`
	Ref        string                `yaml:"$ref"`
}

func loadReportsSpec(t *testing.T) specDoc {
	t.Helper()
	raw, err := os.ReadFile(reportsSpecPath)
	if err != nil {
		t.Fatalf("read %s: %v (every route of cmd/reports must be specified there)", reportsSpecPath, err)
	}
	var doc specDoc
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse %s: %v", reportsSpecPath, err)
	}
	return doc
}

func (d specDoc) schema(t *testing.T, name string) specSchema {
	t.Helper()
	s, ok := d.Components.Schemas[name]
	if !ok {
		t.Fatalf("schema %q is not in %s", name, reportsSpecPath)
	}
	return s
}

// TestReportsContractRoutesMatchSpec fails when cmd/reports serves a route
// apis/openapi-reports.yaml does not specify, or the spec names a route the
// reports router does not serve.
func TestReportsContractRoutesMatchSpec(t *testing.T) {
	served := map[string]bool{}
	router := inboundhttp.NewReportsRouter(&inboundhttp.ReportsServer{Metrics: http.NotFoundHandler()})
	routes, ok := router.(chi.Routes)
	if !ok {
		t.Fatalf("the reports router is a %T, want a chi router", router)
	}
	err := chi.Walk(routes, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		served[strings.ToLower(method)+" "+route] = true
		return nil
	})
	if err != nil {
		t.Fatalf("walk the reports router: %v", err)
	}
	specified := map[string]bool{}
	for path, ops := range loadReportsSpec(t).Paths {
		for method := range ops {
			specified[strings.ToLower(method)+" "+path] = true
		}
	}
	if missing := diff(served, specified); len(missing) > 0 {
		t.Errorf("served by cmd/reports but absent from %s: %v", reportsSpecPath, missing)
	}
	if extra := diff(specified, served); len(extra) > 0 {
		t.Errorf("specified in %s but not served by cmd/reports: %v", reportsSpecPath, extra)
	}
}

func diff(a, b map[string]bool) []string {
	var out []string
	for k := range a {
		if !b[k] {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// assertMatchesSchema fails when body lacks a required property of the named
// schema or carries a property the schema does not document.
func assertMatchesSchema(t *testing.T, doc specDoc, name string, body map[string]any) {
	t.Helper()
	s := doc.schema(t, name)
	for _, req := range s.Required {
		if _, ok := body[req]; !ok {
			t.Errorf("%s: required property %q is missing from the response %v", name, req, body)
		}
	}
	for key := range body {
		if _, ok := s.Properties[key]; !ok {
			t.Errorf("%s: response property %q is not documented in %s", name, key, reportsSpecPath)
		}
	}
}

func decodeBody(t *testing.T, rec interface{ Bytes() []byte }) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(rec.Bytes(), &out); err != nil {
		t.Fatalf("response is not a JSON object: %v", err)
	}
	return out
}

// TestReportsContractResponsesMatchSpec drives both report operations and
// checks the 200 bodies (and the first report row) against the spec schemas.
func TestReportsContractResponsesMatchSpec(t *testing.T) {
	doc := loadReportsSpec(t)
	asOf := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	rs := &inboundhttp.ReportsServer{
		Adherence: &fakeAdherence{rows: []repository.AdherenceRow{{SiteID: "WH1", CarrierSCAC: "ACMF", OnTime: 3, Late: 1}}},
		Freshness: fakeFreshness{asOf: &asOf},
		Now:       func() time.Time { return asOf.Add(5 * time.Second) },
	}

	rec := reportsGet(rs, "/reports/schedule-adherence"+adherenceRange)
	wantStatus(t, rec, http.StatusOK)
	body := decodeBody(t, rec.Body)
	assertMatchesSchema(t, doc, "ScheduleAdherenceReport", body)
	items, _ := body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("items = %v, want one row", body["items"])
	}
	row, _ := items[0].(map[string]any)
	assertMatchesSchema(t, doc, "AdherenceRow", row)

	rec = reportsGet(rs, "/reports/freshness")
	wantStatus(t, rec, http.StatusOK)
	assertMatchesSchema(t, doc, "Freshness", decodeBody(t, rec.Body))

	bad := reportsGet(rs, "/reports/schedule-adherence")
	wantProblem(t, bad, http.StatusBadRequest, "invalid-report-range")
	assertMatchesSchema(t, doc, "Problem", decodeBody(t, bad.Body))
}
