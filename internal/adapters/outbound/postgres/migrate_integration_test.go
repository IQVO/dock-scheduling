//go:build integration

package postgres_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/claudioed/dock-scheduling/internal/adapters/outbound/postgres"
	"github.com/claudioed/dock-scheduling/internal/testing/pgtest"
)

// This file proves the migration runner over the EMBEDDED files: apply, be
// idempotent, carry the tables, the exclusion constraint and the indexes
// exactly as designed, and go down and up again.

var allTables = []string{
	"dock_appointments", "asn_claims", "dock_calendars", "calendar_operating_hours", "calendar_closures",
	"zones", "doors", "asns", "cutoff_schedules", "cutoffs", "processed_events", "outbox", "idempotency_keys",
}

func connect(t *testing.T, url string) (*pgx.Conn, context.Context) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.WithoutCancel(ctx)) })
	return conn, ctx
}

func tableExists(t *testing.T, conn *pgx.Conn, ctx context.Context, table string) bool {
	t.Helper()
	var reg *string
	if err := conn.QueryRow(ctx, `SELECT to_regclass($1)::text`, "public."+table).Scan(&reg); err != nil {
		t.Fatalf("probe %s: %v", table, err)
	}
	return reg != nil
}

func TestMigrationsApplyAndAreIdempotent(t *testing.T) {
	url := pgtest.EmptyURL(t)
	if err := postgres.RunMigrations(url); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if err := postgres.RunMigrations(url); err != nil {
		t.Fatalf("second run must be a no-op: %v", err)
	}
	conn, ctx := connect(t, url)
	for _, table := range allTables {
		if !tableExists(t, conn, ctx, table) {
			t.Fatalf("table %s missing after migration", table)
		}
	}
	var version int
	if err := conn.QueryRow(ctx, `SELECT version FROM schema_migrations`).Scan(&version); err != nil || version != 2 {
		t.Fatalf("schema_migrations version = %d %v, want 2", version, err)
	}
}

func TestMigrationsCarryTheDesignedBackstops(t *testing.T) {
	conn, ctx := connect(t, pgtest.URL(t)) // applied by the shared template
	var def string
	if err := conn.QueryRow(ctx, `SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conname = 'ex_dock_appointments_door_window'`).Scan(&def); err != nil {
		t.Fatalf("exclusion constraint missing: %v", err)
	}
	for _, want := range []string{"EXCLUDE USING gist", "door_code WITH =", "tstzrange(window_start, window_end, '[)'::text) WITH &&", "'BOOKED'", "'CHECKED_IN'", "NOT door_override", "provenance"} {
		if !strings.Contains(def, want) {
			t.Fatalf("door exclusion = %s, want it to contain %q", def, want)
		}
	}
	for _, c := range []struct{ index, want string }{
		{"asn_claims_pkey", "UNIQUE INDEX"},
		{"idx_outbox_attempts_next_attempt", "(attempts, next_attempt_at)"},
		{"idx_dock_appointments_booked_end", "WHERE (state = 'BOOKED'::text)"},
		{"idx_dock_appointments_asns", "USING gin (asn_numbers)"},
	} {
		var indexdef string
		if err := conn.QueryRow(ctx, `SELECT indexdef FROM pg_indexes WHERE indexname = $1`, c.index).Scan(&indexdef); err != nil {
			t.Fatalf("index %s missing: %v", c.index, err)
		}
		if !strings.Contains(indexdef, c.want) {
			t.Fatalf("index %s = %s, want it to contain %q", c.index, indexdef, c.want)
		}
	}
}

func TestMigrationsGoUpDownUp(t *testing.T) {
	url := pgtest.EmptyURL(t)
	if err := postgres.RunMigrations(url); err != nil {
		t.Fatal(err)
	}
	m, err := postgres.Migrator(url)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Down(); err != nil {
		t.Fatalf("down: %v", err)
	}
	_, _ = m.Close()
	conn, ctx := connect(t, url)
	for _, table := range allTables {
		if tableExists(t, conn, ctx, table) {
			t.Fatalf("table %s survived the down migration", table)
		}
	}
	if err := postgres.RunMigrations(url); err != nil {
		t.Fatalf("up again after down: %v", err)
	}
	for _, table := range allTables {
		if !tableExists(t, conn, ctx, table) {
			t.Fatalf("table %s missing after the second up", table)
		}
	}
}
