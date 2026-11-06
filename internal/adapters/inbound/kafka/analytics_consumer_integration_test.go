//go:build integration

package kafka

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/claudioed/dock-scheduling/internal/adapters/outbound/analyticsstore"
	"github.com/claudioed/dock-scheduling/internal/analytics/adherence"
	"github.com/claudioed/dock-scheduling/internal/application/repository"
	"github.com/claudioed/dock-scheduling/internal/testing/kafkatest"
	"github.com/claudioed/dock-scheduling/internal/testing/pgtest"
)

// TestAnalyticsConsumer_RealKafkaAndPostgres: the projector's contract on a real
// broker and a real analytical database. In order: a booking, its check-in, a
// REPLAY of the booking (same id: applied once), an event the projection does
// not fold, a flat legacy message, a poison payload (dead-lettered), and the
// completion of a second appointment that was never checked in (an implicit
// arrival, counted ON_TIME). The consumer commits past all nine.
func TestAnalyticsConsumer_RealKafkaAndPostgres(t *testing.T) {
	w := newITWorld(t)
	url := pgtest.EmptyURL(t)
	if err := analyticsstore.RunMigrations(url); err != nil {
		t.Fatal(err)
	}
	writer, err := analyticsstore.NewPool(w.ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(writer.Close)
	reader, err := analyticsstore.NewReadOnlyPool(w.ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reader.Close)

	topic, dlq := w.topics(AnalyticsTopic)
	second := "appt-7e57ab1e-0000-4000-8000-0000000000a2"
	poison := variant(t, "analytics_cancelled", func(env, data map[string]any) {
		env["id"] = eventIDFor(50)
		data["site_id"] = ""
	})
	w.produce(topic,
		fixture(t, "analytics_booked"),
		fixture(t, "analytics_checked_in"),
		fixture(t, "analytics_booked"), // the same occurrence again
		fixture(t, "analytics_at_risk"),
		flatEnvelope,
		poison,
		variant(t, "analytics_booked", func(env, data map[string]any) {
			env["id"], env["subject"], data["appointment_id"] = eventIDFor(51), second, second
			data["carrier_scac"] = nil
		}),
		variant(t, "analytics_completed", func(env, data map[string]any) {
			env["id"], env["subject"], data["appointment_id"] = eventIDFor(52), second, second
		}),
		fixture(t, "analytics_door_reassigned"),
	)
	group := w.group("analytics")
	c, err := NewAnalyticsConsumerForTopic(w.tr, topic, group, analyticsstore.NewProjection(writer))
	if err != nil {
		t.Fatal(err)
	}
	w.run(c)
	w.committedPast(group, topic, 9)

	rows, err := analyticsstore.NewReader(reader).ScheduleAdherence(context.Background(),
		repository.AdherenceQuery{From: analyticsDay, To: analyticsDay.Add(24 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	want := []repository.AdherenceRow{
		{SiteID: "WH1", CarrierSCAC: "-", OnTime: 1},    // the implicit arrival of the carrier-less booking
		{SiteID: "WH1", CarrierSCAC: "ACMF", OnTime: 1}, // the explicit ON_TIME check-in
	}
	if len(rows) != 2 || rows[0] != want[0] || rows[1] != want[1] {
		t.Fatalf("report = %+v, want %+v", rows, want)
	}
	if n := countRows(t, writer, `SELECT count(*) FROM analytics_processed_events`); n != 4 {
		t.Fatalf("claimed events = %d, want 4 (booked, checked-in, booked, completed; the replay claims nothing)", n)
	}
	assertDeadLetter(t, dlq, topic, eventIDFor(50))
	asOf, err := analyticsstore.NewReader(reader).Freshness(context.Background())
	if err != nil || asOf == nil || !asOf.Equal(time.Date(2026, 11, 6, 9, 40, 10, 0, time.UTC)) {
		t.Fatalf("freshness = %v, %v; want the newest applied event time (the completion at 09:40:10Z)", asOf, err)
	}
}

// TestAnalyticsConsumer_ATransientFailureIsRetriedNeverDeadLettered: the store
// is down for the first attempts; the SAME message is retried until it lands.
func TestAnalyticsConsumer_ATransientFailureIsRetriedNeverDeadLettered(t *testing.T) {
	w := newITWorld(t)
	topic, dlq := w.topics(AnalyticsTopic)
	w.produce(topic, fixture(t, "analytics_booked"))
	flaky := &flakyProjection{failures: 3}
	group := w.group("analytics-flaky")
	c, err := NewAnalyticsConsumerForTopic(w.tr, topic, group, flaky)
	if err != nil {
		t.Fatal(err)
	}
	w.run(c)
	w.committedPast(group, topic, 1)
	if got := flaky.calls.Load(); got != 4 {
		t.Fatalf("Apply calls = %d, want 4 (three transient failures, then success)", got)
	}
	if n := kafkatest.EndOffset(t, dlq); n != 0 {
		t.Fatalf("dead letters = %d, want none for a transient failure", n)
	}
}

var errStoreDown = errors.New("analytics database is down")

// analyticsDay is the UTC day of the analytics fixtures' appointment window.
var analyticsDay = time.Date(2026, 11, 6, 0, 0, 0, 0, time.UTC)

// flakyProjection fails its first `failures` calls with a transient error.
type flakyProjection struct {
	failures int64
	calls    atomic.Int64
}

func (f *flakyProjection) Apply(context.Context, adherence.Event) (bool, error) {
	if f.calls.Add(1) <= f.failures {
		return false, errStoreDown
	}
	return true, nil
}

func countRows(t *testing.T, pool *pgxpool.Pool, query string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), query).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}
