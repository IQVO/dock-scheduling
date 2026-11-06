//go:build integration

package main

import (
	"context"
	"log/slog"
	"reflect"
	"testing"
	"time"

	inboundkafka "github.com/claudioed/dock-scheduling/internal/adapters/inbound/kafka"
	"github.com/claudioed/dock-scheduling/internal/adapters/outbound/analyticsstore"
	outboundkafka "github.com/claudioed/dock-scheduling/internal/adapters/outbound/kafka"
	"github.com/claudioed/dock-scheduling/internal/application/repository"
	"github.com/claudioed/dock-scheduling/internal/testing/kafkatest"
	"github.com/claudioed/dock-scheduling/internal/testing/pgtest"
)

const analyticsGroup = "it-analytics"

// adherence proves the analytics read side against what the REAL service
// published: the analytics topic the relay filled during this journey (a
// booking, a gate arrival and its door release, an arrival implied by a door
// release, a turned-away visit, an ASN cancellation, a legacy import) is folded
// by the projector's consumer into a fresh analytical database, and the report
// counts exactly the two arrivals - the checked-in one by its status and the
// implicit one as ON_TIME (X6) - while cancelled and merely booked appointments
// are not counted.
func (j *journey) adherence(t *testing.T) {
	waitFor(t, "the outbox to drain", 30*time.Second, func() bool { return count(t, j.databaseURL, `SELECT count(*) FROM outbox`) == 0 })
	ctx := context.Background()
	url := pgtest.EmptyURL(t)
	if err := analyticsstore.RunMigrations(url); err != nil {
		t.Fatal(err)
	}
	writer, err := analyticsstore.NewPool(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(writer.Close)
	reader, err := analyticsstore.NewReadOnlyPool(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reader.Close)

	kafkatest.CreateTopic(t, inboundkafka.DeadLetterTopic(inboundkafka.AnalyticsTopic))
	consumer, err := inboundkafka.NewAnalyticsConsumer(inboundkafka.NewTransport(kafkatest.Brokers(t), slog.Default()), analyticsGroup, analyticsstore.NewProjection(writer))
	if err != nil {
		t.Fatal(err)
	}
	running := inboundkafka.Start(ctx, consumer)
	t.Cleanup(func() { _ = running.Stop() })

	waitCommitted(t, analyticsGroup, outboundkafka.TopicAnalytics, kafkatest.EndOffset(t, outboundkafka.TopicAnalytics))

	store := analyticsstore.NewReader(reader)
	rows, err := store.ScheduleAdherence(ctx, repository.AdherenceQuery{From: j.windowStart, To: j.windowStart.Add(72 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	want := []repository.AdherenceRow{{SiteID: "WH1", CarrierSCAC: "ACMF", OnTime: 2}}
	if !reflect.DeepEqual(rows, want) {
		t.Errorf("schedule adherence = %+v, want %+v", rows, want)
	}
	asOf, err := store.Freshness(ctx)
	if err != nil || asOf == nil || time.Since(*asOf) > time.Hour {
		t.Errorf("freshness = %v, %v: want the time of a recent event", asOf, err)
	}
}
