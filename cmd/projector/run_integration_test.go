//go:build integration

package main

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	inboundkafka "github.com/claudioed/dock-scheduling/internal/adapters/inbound/kafka"
	"github.com/claudioed/dock-scheduling/internal/adapters/outbound/analyticsstore"
	"github.com/claudioed/dock-scheduling/internal/application/repository"
	"github.com/claudioed/dock-scheduling/internal/testing/kafkatest"
	"github.com/claudioed/dock-scheduling/internal/testing/pgtest"
)

const itGroup = "it-projector"

// fixtureBytes reads a CloudEvents fixture of the analytics consumer's tests.
func fixtureBytes(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "internal", "adapters", "inbound", "kafka", "testdata", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestRunProjectsTheAnalyticsTopicAndShutsDownGracefully boots the real
// binary's run(): it migrates an empty analytical database, consumes the
// analytics topic, serves /healthz and /readyz, folds a booking and its
// ON_TIME check-in into the report, and returns nil once the context ends.
func TestRunProjectsTheAnalyticsTopicAndShutsDownGracefully(t *testing.T) {
	url := pgtest.EmptyURL(t)
	brokers := kafkatest.Brokers(t)
	kafkatest.CreateTopic(t, inboundkafka.AnalyticsTopic)
	kafkatest.CreateTopic(t, inboundkafka.DeadLetterTopic(inboundkafka.AnalyticsTopic))
	kafkatest.Produce(t, inboundkafka.AnalyticsTopic,
		kafkatest.Message{Key: "appt", Value: fixtureBytes(t, "analytics_booked")},
		kafkatest.Message{Key: "appt", Value: fixtureBytes(t, "analytics_checked_in")},
	)

	getenv := env(map[string]string{
		"ANALYTICS_DATABASE_URL": url, "KAFKA_BROKERS": brokers[0], "ANALYTICS_CONSUMER_GROUP": itGroup,
		"ADMIN_ADDR": "127.0.0.1:0", "SHUTDOWN_DRAIN_DELAY": "0",
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	listening := make(chan string, 1)
	finished := make(chan error, 1)
	go func() { finished <- run(ctx, getenv, func(addr string) { listening <- addr }) }()

	var addr string
	select {
	case addr = <-listening:
	case err := <-finished:
		t.Fatalf("run returned before listening: %v", err)
	case <-time.After(60 * time.Second):
		t.Fatal("the projector never listened")
	}

	status := func(path string) int {
		resp, err := http.Get("http://" + addr + path)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		_, _ = io.Copy(io.Discard, resp.Body)
		return resp.StatusCode
	}
	if got := status("/healthz"); got != http.StatusOK {
		t.Fatalf("/healthz = %d", got)
	}
	deadline := time.Now().Add(30 * time.Second)
	for status("/readyz") != http.StatusOK {
		if time.Now().After(deadline) {
			t.Fatal("/readyz never became ready with Postgres and Kafka up")
		}
		time.Sleep(200 * time.Millisecond)
	}

	deadline = time.Now().Add(90 * time.Second)
	for {
		if offset, ok := kafkatest.TryCommittedOffset(t, itGroup, inboundkafka.AnalyticsTopic); ok && offset >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the projector never committed past the two events")
		}
		time.Sleep(300 * time.Millisecond)
	}

	reader, err := analyticsstore.NewReadOnlyPool(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	day := time.Date(2026, 11, 6, 0, 0, 0, 0, time.UTC)
	rows, err := analyticsstore.NewReader(reader).ScheduleAdherence(context.Background(), repository.AdherenceQuery{From: day, To: day.Add(24 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0] != (repository.AdherenceRow{SiteID: "WH1", CarrierSCAC: "ACMF", OnTime: 1}) {
		t.Fatalf("report = %+v, want one ON_TIME arrival of ACMF at WH1", rows)
	}

	cancel()
	select {
	case err := <-finished:
		if err != nil {
			t.Fatalf("a clean stop returns nil, got %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the projector did not stop after its context ended")
	}
}
