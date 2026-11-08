package kafka

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/claudioed/dock-scheduling/internal/analytics/adherence"
)

// The analytics fixtures are the exact bytes the outbox relay publishes on the
// analytics topic (internal/adapters/outbound/kafka/testdata/*_analytics.golden.json);
// cmd/projector's test pins the copies byte-equal to the goldens.

const analyticsAppointment = "appt-123e4567-e89b-12d3-a456-426614174000"

// fakeProjection records the events and answers with err.
type fakeProjection struct {
	got []adherence.Event
	err error
}

func (f *fakeProjection) Apply(_ context.Context, e adherence.Event) (bool, error) {
	f.got = append(f.got, e)
	return f.err == nil, f.err
}

func analyticsConsumer(t *testing.T, p adherence.Projection) *Consumer {
	t.Helper()
	tr := Transport{
		Readers:     func(string, string) Reader { return &blockingReader{} },
		DeadLetters: func(string) DeadLetterWriter { return &fakeDLQ{} },
	}
	c, err := NewAnalyticsConsumerForTopic(tr, "analytics.itest", "g-analytics", p)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func at(s string) time.Time {
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return v
}

func TestAnalytics_EveryFoldedEventMapsToItsAdherenceEvent(t *testing.T) {
	window := func(e *adherence.Event, version int64) {
		e.WindowStart, e.WindowEnd, e.Version = at("2026-11-06T08:00:00Z"), at("2026-11-06T10:00:00Z"), version
	}
	tests := []struct {
		fixture string
		want    adherence.Event
	}{
		{"analytics_booked", func() adherence.Event {
			e := adherence.Event{Kind: adherence.Booked, EventID: "6f1d2c3b-4a5e-4b7c-8d9e-0a1b2c3d4e5f", At: at("2026-11-05T12:00:00Z"), CarrierSCAC: "ACMF"}
			window(&e, 1)
			return e
		}()},
		{"analytics_rescheduled", func() adherence.Event {
			e := adherence.Event{Kind: adherence.Rescheduled, EventID: "0b9a8c7d-6e5f-4a3b-9c2d-1e0f9a8b7c6d", At: at("2026-11-05T13:10:00Z")}
			e.WindowStart, e.WindowEnd, e.Version = at("2026-11-06T09:00:00Z"), at("2026-11-06T11:00:00Z"), 2
			return e
		}()},
		{"analytics_checked_in", adherence.Event{Kind: adherence.CheckedIn, EventID: "8c7d6e5f-4a3b-4c2d-9e0f-9a8b7c6d5e4f", At: at("2026-11-06T07:55:30Z"),
			ArrivalStatus: adherence.OnTime, CheckedInAt: at("2026-11-06T07:55:00Z"), Version: 2}},
		{"analytics_completed", adherence.Event{Kind: adherence.Completed, EventID: "d4e5f6a7-b8c9-4d0e-8f2a-3b4c5d6e7f80", At: at("2026-11-06T09:40:10Z"),
			CompletedAt: at("2026-11-06T09:40:00Z"), Version: 3}},
		{"analytics_cancelled", adherence.Event{Kind: adherence.Cancelled, EventID: "2b1a0f9e-8d7c-4b6a-a594-837261504f3e", At: at("2026-11-05T14:20:00Z"),
			Reason: "CARRIER_REQUEST", Version: 2}},
		{"analytics_no_show", adherence.Event{Kind: adherence.NoShow, EventID: "4d3c2b1a-0f9e-4d8c-8b7a-6f5e4d3c2b1a", At: at("2026-11-06T11:05:00Z"),
			CarrierSCAC: "ACMF", WindowEnd: at("2026-11-06T10:00:00Z"), Version: 2}},
	}
	for _, tc := range tests {
		t.Run(tc.fixture, func(t *testing.T) {
			p := &fakeProjection{}
			if err := analyticsConsumer(t, p).HandleMessage(context.Background(), fixture(t, tc.fixture)); err != nil {
				t.Fatalf("HandleMessage = %v", err)
			}
			want := tc.want
			want.AppointmentID, want.SiteID = analyticsAppointment, "WH1"
			if len(p.got) != 1 || p.got[0] != want {
				t.Fatalf("projected %+v\nwant      %+v", p.got, want)
			}
		})
	}
}

func TestAnalytics_EventsThatCannotChangeAdherenceAreIgnored(t *testing.T) {
	p := &fakeProjection{}
	c := analyticsConsumer(t, p)
	for _, name := range []string{"analytics_door_reassigned", "analytics_at_risk"} {
		if err := c.HandleMessage(context.Background(), fixture(t, name)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	// The retired flat envelope is not a CloudEvent: skipped, never retried.
	if err := c.HandleMessage(context.Background(), []byte(`{"event_id":"e-flat","event_type":"DockAppointmentCompleted","occurred_at":"2026-11-05T14:10:00Z"}`)); err != nil {
		t.Fatalf("flat envelope: %v", err)
	}
	if len(p.got) != 0 {
		t.Fatalf("projected %+v, want nothing", p.got)
	}
}

func TestAnalytics_PayloadsThatCanNeverBeProjectedArePoison(t *testing.T) {
	tests := map[string]struct {
		fixture string
		edit    func(env, data map[string]any)
		wantErr string
	}{
		"appointment_id differs from the subject": {"analytics_cancelled", func(_, d map[string]any) { d["appointment_id"] = "appt-other" }, "does not match subject"},
		"no site":                                {"analytics_cancelled", func(_, d map[string]any) { d["site_id"] = "" }, "no site_id"},
		"an unknown arrival status":              {"analytics_checked_in", func(_, d map[string]any) { d["arrival_status"] = "SOMETIME" }, "arrival_status"},
		"a check-in time that is not RFC 3339":   {"analytics_checked_in", func(_, d map[string]any) { d["checked_in_at"] = "yesterday" }, "not an RFC3339 time"},
		"a booking without a window":             {"analytics_booked", func(_, d map[string]any) { delete(d, "window_start") }, "window_start of"},
		"an inverted window":                     {"analytics_rescheduled", func(_, d map[string]any) { d["window_end"] = "2026-11-06T08:00:00Z" }, "window_end must be after"},
		"a booking without a version":            {"analytics_booked", func(_, d map[string]any) { d["version"] = 0 }, "version must be at least 1"},
		"a payload that does not decode":         {"analytics_booked", func(_, d map[string]any) { d["version"] = "one" }, "does not decode"},
		"a no-show window end that is malformed": {"analytics_no_show", func(_, d map[string]any) { d["window_end"] = "later" }, "window_end"},
		"an event without a time": {"analytics_completed", func(env, d map[string]any) {
			delete(env, "time")
			delete(d, "completed_at")
		}, "completed_at"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			p := &fakeProjection{}
			err := analyticsConsumer(t, p).HandleMessage(context.Background(), variant(t, tc.fixture, tc.edit))
			assertPoison(t, err)
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %v, want it to mention %q", err, tc.wantErr)
			}
			if len(p.got) != 0 {
				t.Errorf("a poison message must not reach the projection: %+v", p.got)
			}
		})
	}
}

func TestAnalytics_AStoreRefusalIsPoisonAndAnythingElseIsTransient(t *testing.T) {
	c := func(err error) *Consumer { return analyticsConsumer(t, &fakeProjection{err: err}) }
	msg := fixture(t, "analytics_cancelled")

	assertPoison(t, c(fmt.Errorf("%w: SQLSTATE 22021", adherence.ErrRejected)).HandleMessage(context.Background(), msg))
	assertPoison(t, c(adherence.ErrInvalidEvent).HandleMessage(context.Background(), msg))

	down := errors.New("connection refused")
	err := c(down).HandleMessage(context.Background(), msg)
	var p *deadLetterError
	if err == nil || errors.As(err, &p) || !errors.Is(err, down) {
		t.Fatalf("a database that is down must be a plain transient error for the retry, got %v", err)
	}
}

func TestAnalytics_GroupComesFromTheEnvironment(t *testing.T) {
	if _, err := AnalyticsGroupFromEnv(func(string) string { return "  " }); !errors.Is(err, ErrNoConsumerGroup) || !strings.Contains(err.Error(), "ANALYTICS_CONSUMER_GROUP") {
		t.Fatalf("a blank group must be a boot error naming ANALYTICS_CONSUMER_GROUP, got %v", err)
	}
	if g, err := AnalyticsGroupFromEnv(func(k string) string {
		if k != "ANALYTICS_CONSUMER_GROUP" {
			t.Errorf("read %s", k)
		}
		return " dock-scheduling-analytics "
	}); err != nil || g != "dock-scheduling-analytics" {
		t.Fatalf("group = %q, %v", g, err)
	}
	tr := Transport{Readers: func(string, string) Reader { return &blockingReader{} }}
	if _, err := NewAnalyticsConsumer(tr, "", &fakeProjection{}); !errors.Is(err, ErrNoConsumerGroup) || !strings.Contains(err.Error(), "ANALYTICS_CONSUMER_GROUP") {
		t.Fatalf("a consumer without a group must name the variable, got %v", err)
	}
	c, err := NewAnalyticsConsumer(tr, "g", &fakeProjection{})
	if err != nil || c.Topic != "warehouse.dock-scheduling.analytics" || c.GroupID != "g" {
		t.Fatalf("consumer = %+v, %v; want the service's own analytics topic", c, err)
	}
}
