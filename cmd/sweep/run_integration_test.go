//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	inboundhttp "github.com/claudioed/dock-scheduling/internal/adapters/inbound/http"
	"github.com/claudioed/dock-scheduling/internal/adapters/outbound/postgres"
	"github.com/claudioed/dock-scheduling/internal/adapters/outbound/system"
	"github.com/claudioed/dock-scheduling/internal/adapters/outbound/telemetry"
	"github.com/claudioed/dock-scheduling/internal/application/usecases"
	"github.com/claudioed/dock-scheduling/internal/domain/shared"
	"github.com/claudioed/dock-scheduling/internal/testing/pgtest"
	"github.com/jackc/pgx/v5/pgxpool"
)

// stepClock is ports.Clock held still: the booking must happen at a past
// instant (a real BOOKED appointment whose window end + the site's 60-minute
// no-show grace has already passed — the domain refuses windows that start in
// the past), and the sweep must run at the real now. Advancing the clock
// between the two is exactly what time does in production.
type stepClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *stepClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *stepClock) advanceTo(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
}

// TestRunMainMarksABookedAppointmentNoShowOverTheRealAPI runs the sweep
// entrypoint's real runMain() against the real REST surface on a real
// testcontainers Postgres: the router is wired exactly as cmd/api's wire.go
// wires it (real migrations, pool, repos, unit of work, idempotency store),
// a BOOKED appointment whose grace has passed is swept to NO_SHOW by the
// POST the binary sends, the no-show fact lands in the outbox, a second pass
// marks nothing (the sweep is naturally idempotent), and the exit code is 0.
func TestRunMainMarksABookedAppointmentNoShowOverTheRealAPI(t *testing.T) {
	ctx := context.Background()
	pool := pgtest.NewPool(t)

	// The reference data the Kafka consumers would have folded, written
	// through the same stores their effects use.
	doors := postgres.NewDoorStore(pool)
	if err := doors.UpsertZone(ctx, "zone-it-sweep", "WH1"); err != nil {
		t.Fatal(err)
	}
	const sweepDoor, sweepASN = "WH1-DOCK-IT-SWEEP", "ASN-IT-SWEEP"
	if err := doors.UpsertDoor(ctx, sweepDoor, "zone-it-sweep", shared.FlowBoth, ""); err != nil {
		t.Fatal(err)
	}
	bookedAt := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Hour)
	if err := postgres.NewAsnStore(pool).Register(ctx, sweepASN, bookedAt.Add(48*time.Hour)); err != nil {
		t.Fatal(err)
	}

	// cmd/api's wire.go Deps, with the one test seam the scenario needs: the
	// Clock port, held at the booking instant and advanced before the sweep.
	clock := &stepClock{now: bookedAt}
	metrics, err := telemetry.New()
	if err != nil {
		t.Fatal(err)
	}
	svc := usecases.New(usecases.Deps{
		Appointments: postgres.NewAppointmentRepo(pool), Calendars: postgres.NewCalendarRepo(pool), Carriers: postgres.NewCarrierRepo(pool),
		Doors: postgres.NewDoorStore(pool), Asns: postgres.NewAsnStore(pool), Cutoffs: postgres.NewCutoffStore(pool),
		Processed: postgres.NewProcessedEventRepo(pool), Events: postgres.NewOutboxPublisher(pool), Metrics: metrics,
		IDs: system.IDs{}, Clock: clock, UoW: postgres.NewUnitOfWork(pool),
	})
	router := inboundhttp.NewRouter(&inboundhttp.Server{
		Service: svc, Idempotency: postgres.NewIdempotencyStore(pool), UoW: postgres.NewUnitOfWork(pool), Now: system.Clock{}.Now,
	})
	api := httptest.NewServer(router)
	t.Cleanup(api.Close)

	// A booking in the past of the real clock, valid at the booked instant.
	windowStart, windowEnd := bookedAt, bookedAt.Add(30*time.Minute)
	body, _ := json.Marshal(map[string]any{
		"direction": "INBOUND", "doorCode": sweepDoor, "carrier": "ACME Freight", "carrierScac": "ACMF",
		"windowStart": windowStart.Format(time.RFC3339), "windowEnd": windowEnd.Format(time.RFC3339),
		"asnNumbers": []string{sweepASN},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, api.URL+"/appointments", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "it-sweep-book-1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var view struct {
		AppointmentID string `json:"appointmentId"`
		State         string `json:"state"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&view); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusCreated || view.State != "BOOKED" {
		t.Fatalf("POST /appointments = %d %+v, want 201 BOOKED", resp.StatusCode, view)
	}
	if n := pgtest.Count(t, pool, "dock_appointments"); n != 1 {
		t.Fatalf("dock_appointments rows after the booking = %d, want 1", n)
	}

	// The scheduled moment arrives: the sweep entrypoint runs as the CronJob
	// runs it, against this release's own Service.
	clock.advanceTo(time.Now().UTC())
	lookup := env(map[string]string{
		"SWEEP_URL": api.URL + "/no-show-sweeps", "SIDECAR_QUIT_URL": "", "SWEEP_REQUEST_TIMEOUT": "10s",
	})
	r := runner{client: http.DefaultClient, logger: slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil)), sleep: sleepCtx}
	if code := r.runMain(ctx, lookup); code != exitOK {
		t.Fatalf("runMain exit code = %d, want %d", code, exitOK)
	}

	if n := countState(t, pool, "NO_SHOW", view.AppointmentID); n != 1 {
		t.Fatalf("NO_SHOW rows for %s = %d, want 1 (version 2)", view.AppointmentID, n)
	}
	if got := outboxTypes(t, pool); len(got) != 2 ||
		got[0] != "DockAppointmentBooked" || got[1] != "DockAppointmentNoShow" {
		t.Fatalf("outbox events = %v, want the booking then the no-show fact", got)
	}

	// A second pass marks nothing: the sweep is naturally idempotent.
	if code := r.runMain(ctx, lookup); code != exitOK {
		t.Fatalf("second runMain exit code = %d, want %d", code, exitOK)
	}
	if n := pgtest.Count(t, pool, "outbox"); n != 2 {
		t.Errorf("outbox rows after the second pass = %d, want still 2", n)
	}
	if n := countState(t, pool, "NO_SHOW", view.AppointmentID); n != 1 {
		t.Errorf("NO_SHOW rows after the second pass = %d, want still 1", n)
	}
}

func countState(t *testing.T, pool *pgxpool.Pool, state, id string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM dock_appointments WHERE appointment_id = $1 AND state = $2 AND version = 2`, id, state).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// outboxTypes returns the event name of every outbox row, in order.
func outboxTypes(t *testing.T, pool *pgxpool.Pool) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(), `SELECT payload FROM outbox ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			t.Fatal(err)
		}
		var env struct {
			Event string `json:"event"`
		}
		if err := json.Unmarshal(payload, &env); err != nil {
			t.Fatal(err)
		}
		out = append(out, env.Event)
	}
	return out
}
