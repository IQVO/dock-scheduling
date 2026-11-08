//go:build integration

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/claudioed/dock-scheduling/internal/adapters/outbound/analyticsstore"
	"github.com/claudioed/dock-scheduling/internal/analytics/adherence"
	"github.com/claudioed/dock-scheduling/internal/testing/pgtest"
)

// TestRunServesTheReportsFromTheAnalyticalDatabaseAndShutsDownGracefully boots
// the real binary's run() against a populated analytical database under a
// SELECT-only role (as the chart deploys it): the schedule-adherence report
// counts an explicit ON_TIME arrival and an implicit one (completed with no
// check-in, X6), /freshness answers, /healthz and /readyz are 200, and run
// returns nil once its context ends.
func TestRunServesTheReportsFromTheAnalyticalDatabaseAndShutsDownGracefully(t *testing.T) {
	ctx := context.Background()
	url := pgtest.EmptyURL(t)
	if err := analyticsstore.RunMigrations(url); err != nil {
		t.Fatal(err)
	}
	writer, err := analyticsstore.NewPool(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	proj := analyticsstore.NewProjection(writer)

	start := time.Date(2026, 11, 6, 8, 0, 0, 0, time.UTC)
	n := 0
	apply := func(e adherence.Event) {
		t.Helper()
		n++
		e.EventID = fmt.Sprintf("ev-%d", n)
		if e.At.IsZero() {
			e.At = time.Now().UTC()
		}
		if applied, err := proj.Apply(ctx, e); err != nil || !applied {
			t.Fatalf("Apply(%s) = %v, %v", e.Kind, applied, err)
		}
	}
	booked := func(id string) adherence.Event {
		return adherence.Event{Kind: adherence.Booked, AppointmentID: id, SiteID: "WH1", CarrierSCAC: "ACMF",
			WindowStart: start, WindowEnd: start.Add(time.Hour), Version: 1, At: start.Add(-48 * time.Hour)}
	}
	apply(booked("a-explicit"))
	apply(adherence.Event{Kind: adherence.CheckedIn, AppointmentID: "a-explicit", SiteID: "WH1", ArrivalStatus: adherence.ArrivalStatus("ON_TIME"), CheckedInAt: start, At: start})
	apply(booked("a-implicit"))
	apply(adherence.Event{Kind: adherence.Completed, AppointmentID: "a-implicit", SiteID: "WH1", CompletedAt: start.Add(30 * time.Minute), At: start.Add(30 * time.Minute)})
	apply(booked("a-only-booked"))

	roleURL := pgtest.AsRole(t, url, "ds_reports_run", "SELECT ON ALL TABLES IN SCHEMA public")
	getenv := env(map[string]string{"ANALYTICS_DATABASE_URL": roleURL, "HTTP_ADDR": "127.0.0.1:0", "SHUTDOWN_DRAIN_DELAY": "0"})
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	listening := make(chan string, 1)
	finished := make(chan error, 1)
	go func() { finished <- run(runCtx, getenv, func(addr string) { listening <- addr }) }()

	var addr string
	select {
	case addr = <-listening:
	case err := <-finished:
		t.Fatalf("run returned before listening: %v", err)
	case <-time.After(60 * time.Second):
		t.Fatal("the reports binary never listened")
	}

	get := func(path string) (int, []byte) {
		resp, err := http.Get("http://" + addr + path)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, body
	}
	for _, probe := range []string{"/healthz", "/readyz"} {
		if code, body := get(probe); code != http.StatusOK {
			t.Fatalf("%s = %d %s", probe, code, body)
		}
	}

	code, body := get("/reports/schedule-adherence?siteId=WH1&carrierScac=ACMF&from=" + start.Add(-time.Hour).Format(time.RFC3339) + "&to=" + start.Add(24*time.Hour).Format(time.RFC3339))
	if code != http.StatusOK {
		t.Fatalf("schedule-adherence = %d %s", code, body)
	}
	var report struct {
		Items []struct {
			SiteID      string  `json:"siteId"`
			CarrierSCAC string  `json:"carrierScac"`
			OnTime      int     `json:"onTime"`
			Total       int     `json:"total"`
			OnTimeShare float64 `json:"onTimeShare"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &report); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	if len(report.Items) != 1 || report.Items[0].SiteID != "WH1" || report.Items[0].CarrierSCAC != "ACMF" ||
		report.Items[0].OnTime != 2 || report.Items[0].Total != 2 || report.Items[0].OnTimeShare != 1 {
		t.Fatalf("report = %s, want the two arrivals (explicit and implicit) as ON_TIME and the merely booked one uncounted", body)
	}

	if code, body := get("/reports/schedule-adherence?siteId=NOPE&from=" + start.Add(-time.Hour).Format(time.RFC3339) + "&to=" + start.Add(24*time.Hour).Format(time.RFC3339)); code != http.StatusOK {
		t.Fatalf("an unknown site is an empty report, got %d %s", code, body)
	} else {
		var empty struct {
			Items []any `json:"items"`
		}
		if err := json.Unmarshal(body, &empty); err != nil || len(empty.Items) != 0 {
			t.Fatalf("unknown site report = %s, %v", body, err)
		}
	}
	if code, body := get("/reports/freshness"); code != http.StatusOK {
		t.Fatalf("freshness = %d %s", code, body)
	}

	cancel()
	select {
	case err := <-finished:
		if err != nil {
			t.Fatalf("a clean stop returns nil, got %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the reports binary did not stop after its context ended")
	}
}
