package kafka_test

import (
	"context"
	"errors"
	"testing"
	"time"

	kafkago "github.com/segmentio/kafka-go"

	"github.com/claudioed/dock-scheduling/internal/adapters/outbound/kafka"
	"github.com/claudioed/dock-scheduling/internal/application/outbox"
)

// fakeWriter records every WriteMessages call.
type fakeWriter struct {
	calls [][]kafkago.Message
	err   error
}

func (w *fakeWriter) WriteMessages(_ context.Context, msgs ...kafkago.Message) error {
	if w.err != nil {
		return w.err
	}
	w.calls = append(w.calls, msgs)
	return nil
}

func (w *fakeWriter) last() []kafkago.Message {
	if len(w.calls) == 0 {
		return nil
	}
	return w.calls[len(w.calls)-1]
}

func openedRow() outbox.Row {
	return outbox.Row{
		ID: 7,
		Fact: outbox.Fact{
			EventID: "6f1d2c3b-4a5e-4b7c-8d9e-0a1b2c3d4e5f", Entity: "dockappointment", Event: "DockAppointmentBooked",
			AggregateID: "appt-1", OccurredAt: time.Date(2026, 10, 21, 9, 30, 0, 0, time.UTC),
			Data: []byte(`{"appointment_id":"appt-1","site_id":"WH1"}`),
		},
		RawPayload: []byte(`{"event":"DockAppointmentBooked","aggregate_id":"appt-1","data":{"appointment_id":"appt-1"}}`),
	}
}

// A known row is encoded and written as the TWO twins: the integration one
// on the events topic and the analytics one on the analytics topic, both
// with the aggregate id as the key and exactly the content-type header
// (ADR 0006).
func TestRelaySinkHandleWritesEncodedEvent(t *testing.T) {
	w := &fakeWriter{}
	sink := kafka.NewRelaySinkWithWriter(w)

	if err := sink.Handle(context.Background(), openedRow()); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	msgs := w.last()
	if len(msgs) != 2 {
		t.Fatalf("%d messages, want 2 (integration + analytics)", len(msgs))
	}
	for i, wantTopic := range []string{kafka.TopicEvents, kafka.TopicAnalytics} {
		m := msgs[i]
		if m.Topic != wantTopic || string(m.Key) != "appt-1" {
			t.Fatalf("twin %d: topic %q key %q, want %q", i, m.Topic, m.Key, wantTopic)
		}
		if len(m.Headers) != 1 || m.Headers[0].Key != "content-type" {
			t.Fatalf("twin %d: headers = %+v, want exactly the content-type", i, m.Headers)
		}
		if len(m.Value) == 0 || m.Value[0] != '{' {
			t.Fatalf("twin %d: value is not the CloudEvents JSON: %s", i, m.Value)
		}
	}
}

// The EventsTopic/AnalyticsTopic fields redirect the SAME encodings to other
// topics (the integration proof's isolated topics).
func TestRelaySinkHandleHonorsTopicOverride(t *testing.T) {
	w := &fakeWriter{}
	sink := kafka.NewRelaySinkWithWriter(w)
	sink.EventsTopic = "t.itest-events"
	sink.AnalyticsTopic = "t.itest-analytics"

	if err := sink.Handle(context.Background(), openedRow()); err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"t.itest-events", "t.itest-analytics"} {
		if got := w.last()[i].Topic; got != want {
			t.Fatalf("twin %d topic = %q, want %q", i, got, want)
		}
	}
}

// assertDeadLetter checks one dead-lettered write: the DLQ topic, the
// aggregate key, the raw payload value and the x-dlq-* headers.
func assertDeadLetter(t *testing.T, w *fakeWriter, row outbox.Row) {
	t.Helper()
	msgs := w.last()
	if len(msgs) != 1 {
		t.Fatalf("%d messages, want the single DLQ write", len(msgs))
	}
	m := msgs[0]
	if m.Topic != kafka.DeadLetterTopic {
		t.Fatalf("topic = %q, want %q", m.Topic, kafka.DeadLetterTopic)
	}
	if string(m.Key) != row.Fact.AggregateID {
		t.Fatalf("key = %q, want the aggregate id", m.Key)
	}
	if string(m.Value) != string(row.RawPayload) {
		t.Fatalf("value = %s, want the raw payload", m.Value)
	}
	hdrs := map[string]string{}
	for _, h := range m.Headers {
		hdrs[h.Key] = string(h.Value)
	}
	for _, key := range []string{"content-type", "x-dlq-error", "x-dlq-outbox-row", "x-dlq-aggregate", "x-dlq-failed-at"} {
		if hdrs[key] == "" {
			t.Errorf("header %q missing: %+v", key, hdrs)
		}
	}
	if hdrs["x-dlq-outbox-row"] != "7" || hdrs["x-dlq-aggregate"] != "appt-1" {
		t.Errorf("x-dlq headers = %s / %s", hdrs["x-dlq-outbox-row"], hdrs["x-dlq-aggregate"])
	}
}

// A row with no wire shape (unknown event, or a payload that is not the
// publisher's envelope) is dead-lettered RAW — payload bytes, aggregate key,
// x-dlq-* headers — and Handle reports success so the relay disposes of the
// row.
func TestRelaySinkHandleDeadLettersUnknownRows(t *testing.T) {
	for _, tc := range []struct {
		name string
		row  outbox.Row
	}{
		{"unknown event", func() outbox.Row {
			r := openedRow()
			r.Fact.Event = "DockAppointmentReopened"
			return r
		}()},
		{"foreign payload", func() outbox.Row {
			r := openedRow()
			r.Fact.Event, r.Fact.Entity = "", ""
			return r
		}()},
		{"undecodable data", func() outbox.Row {
			r := openedRow()
			r.Fact.Data = []byte(`{"appointment_id":`)
			return r
		}()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := &fakeWriter{}
			sink := kafka.NewRelaySinkWithWriter(w)

			if err := sink.Handle(context.Background(), tc.row); err != nil {
				t.Fatalf("Handle: %v (poison must be disposed, not failed)", err)
			}
			assertDeadLetter(t, w, tc.row)
		})
	}
}

// A dead-letter write that itself FAILS is an error: the store records an
// attempt and the row is retried — poison is never dropped on a broker hiccup.
func TestRelaySinkDeadLetterFailureIsAnAttempt(t *testing.T) {
	w := &fakeWriter{err: errors.New("broker down")}
	sink := kafka.NewRelaySinkWithWriter(w)

	r := openedRow()
	r.Fact.Event = "Nope"
	if err := sink.Handle(context.Background(), r); err == nil {
		t.Fatal("want the dead-letter write error, got nil")
	}
}

// A cancelled context never writes and never dead-letters: shutdown mid-send
// leaves the row unpublished.
func TestRelaySinkCancelledContextWritesNothing(t *testing.T) {
	w := &fakeWriter{}
	sink := kafka.NewRelaySinkWithWriter(w)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := sink.Handle(ctx, openedRow()); !errors.Is(err, context.Canceled) {
		t.Fatalf("Handle on a cancelled ctx = %v, want context.Canceled", err)
	}
	poison := openedRow()
	poison.Fact.Event = "Nope"
	if err := sink.Handle(ctx, poison); !errors.Is(err, context.Canceled) {
		t.Fatalf("poison Handle on a cancelled ctx = %v, want context.Canceled (no DLQ on shutdown)", err)
	}
	if w.calls != nil {
		t.Fatalf("writer saw %+v, want nothing", w.calls)
	}
}

// Send writes what it is given, each message on the topic it names, and
// rejects a message without a topic before anything is written.
func TestRelaySinkSend(t *testing.T) {
	w := &fakeWriter{}
	sink := kafka.NewRelaySinkWithWriter(w)

	if err := sink.Send(context.Background()); err != nil {
		t.Fatalf("empty Send: %v", err)
	}
	msg := outbox.Message{Topic: "t", Key: []byte("k"), Value: []byte("v"),
		Headers: []outbox.Header{{Key: "content-type", Value: "application/cloudevents+json; charset=UTF-8"}}}
	if err := sink.Send(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	got := w.last()
	if len(got) != 1 || got[0].Topic != "t" || string(got[0].Key) != "k" || len(got[0].Headers) != 1 {
		t.Fatalf("written = %+v", got)
	}

	if err := sink.Send(context.Background(), outbox.Message{Topic: "", EventType: "X"}); err == nil {
		t.Fatal("want the no-topic rejection")
	}
	if len(w.calls) != 1 {
		t.Fatalf("writer saw %d calls, want 1 (the rejected batch was not written)", len(w.calls))
	}
}
