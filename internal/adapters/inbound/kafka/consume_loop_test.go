package kafka

import (
	"context"
	"errors"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	ce "github.com/cloudevents/sdk-go/v2/event"
)

const loopType = "com.warehouse.wms.yard-management.trailer.TrailerDocked"

// loopConsumer is a Consumer over reader whose only handler is h; the sleeps
// are recorded, not slept.
func loopConsumer(reader Reader, dlq DeadLetterWriter, h HandlerFunc, sleeps *[]time.Duration) *Consumer {
	c := &Consumer{
		Name: "loop test consumer", Topic: "t", GroupID: "g", Reader: reader, DLQ: dlq,
		Handlers: map[string]HandlerFunc{loopType: h},
		Retry:    RetryPolicy{Initial: 10 * time.Millisecond, Max: 40 * time.Millisecond},
		sleep: func(_ context.Context, d time.Duration) error {
			*sleeps = append(*sleeps, d)
			return nil
		},
	}
	return c
}

func eventBytes(t *testing.T, id string) []byte {
	t.Helper()
	return withID(t, "yard_TrailerDocked", id)
}

func runDrained(t *testing.T, c *Consumer) {
	t.Helper()
	if err := c.Run(context.Background()); !errors.Is(err, errDrained) {
		t.Fatalf("Run = %v, want the reader's drain error", err)
	}
}

func TestRun_CommitsOnlyAfterTheMessageWasHandled(t *testing.T) {
	var handled []string
	r := newFakeReader(eventBytes(t, "a"), eventBytes(t, "b"))
	var sleeps []time.Duration
	c := loopConsumer(r, nil, func(_ context.Context, e ce.Event) error {
		if got := r.commits(); len(got) != len(handled) {
			t.Fatalf("offset committed before the message was handled: commits=%v handled=%v", got, handled)
		}
		handled = append(handled, e.ID())
		return nil
	}, &sleeps)
	runDrained(t, c)
	if !slices.Equal(handled, []string{"a", "b"}) || !slices.Equal(r.commits(), []int64{0, 1}) {
		t.Fatalf("handled=%v commits=%v", handled, r.commits())
	}
}

func TestRun_ATransientFailureRetriesTheSameMessageWithCappedBackoff(t *testing.T) {
	var attempts int
	r := newFakeReader(eventBytes(t, "a"))
	var sleeps []time.Duration
	c := loopConsumer(r, &fakeDLQ{}, func(context.Context, ce.Event) error {
		attempts++
		if attempts <= 5 {
			return errors.New("postgres is down")
		}
		return nil
	}, &sleeps)
	runDrained(t, c)
	// the first failure is the handle call itself, the retries follow
	want := []time.Duration{10 * time.Millisecond, 20 * time.Millisecond, 40 * time.Millisecond, 40 * time.Millisecond}
	if attempts != 6 || !slices.Equal(sleeps, want) || !slices.Equal(r.commits(), []int64{0}) {
		t.Fatalf("attempts=%d sleeps=%v commits=%v", attempts, sleeps, r.commits())
	}
}

func TestRun_ACommitThatFailsIsRetriedNotSkipped(t *testing.T) {
	r := newFakeReader(eventBytes(t, "a"))
	r.commitFails = 2
	var sleeps []time.Duration
	c := loopConsumer(r, nil, func(context.Context, ce.Event) error { return nil }, &sleeps)
	runDrained(t, c)
	if len(sleeps) != 2 || !slices.Equal(r.commits(), []int64{0}) {
		t.Fatalf("sleeps=%v commits=%v", sleeps, r.commits())
	}
}

func poison(msg string) HandlerFunc {
	return func(context.Context, ce.Event) error { return &deadLetterError{cause: invalidf("%s", msg)} }
}

func TestRun_APoisonMessageIsDeadLetteredOnceAndCommittedPast(t *testing.T) {
	good := eventBytes(t, "good")
	bad := eventBytes(t, "bad")
	r := newFakeReader(bad, good)
	dlq := &fakeDLQ{}
	var sleeps []time.Duration
	var handled []string
	c := loopConsumer(r, dlq, func(ctx context.Context, e ce.Event) error {
		handled = append(handled, e.ID())
		if e.ID() == "bad" {
			return poison("never applies")(ctx, e)
		}
		return nil
	}, &sleeps)
	runDrained(t, c)
	if dlq.count() != 1 || string(dlq.letters[0].Value) != string(bad) || dlq.sources[0] != "t" {
		t.Fatalf("dead letters = %d from %v", dlq.count(), dlq.sources)
	}
	if dlq.causes[0] == "" || len(sleeps) != 0 {
		t.Fatalf("cause=%q sleeps=%v: a poison message is never retried", dlq.causes, sleeps)
	}
	if !slices.Equal(handled, []string{"bad", "good"}) || !slices.Equal(r.commits(), []int64{0, 1}) {
		t.Fatalf("handled=%v commits=%v", handled, r.commits())
	}
}

func TestRun_AFailingDeadLetterPublishIsRetriedNeverSkipped(t *testing.T) {
	r := newFakeReader(eventBytes(t, "bad"))
	dlq := &fakeDLQ{failFirst: 2}
	var sleeps []time.Duration
	c := loopConsumer(r, dlq, poison("never applies"), &sleeps)
	runDrained(t, c)
	if dlq.attempts != 3 || dlq.count() != 1 || len(sleeps) != 2 || !slices.Equal(r.commits(), []int64{0}) {
		t.Fatalf("attempts=%d letters=%d sleeps=%v commits=%v", dlq.attempts, dlq.count(), sleeps, r.commits())
	}
}

func TestRun_WithoutADeadLetterWriterAPoisonMessageIsSkipped(t *testing.T) {
	r := newFakeReader(eventBytes(t, "bad"))
	var sleeps []time.Duration
	c := loopConsumer(r, nil, poison("never applies"), &sleeps)
	runDrained(t, c)
	if !slices.Equal(r.commits(), []int64{0}) || len(sleeps) != 0 {
		t.Fatalf("commits=%v sleeps=%v", r.commits(), sleeps)
	}
}

func TestRun_ShutdownMidHandlingNeitherDeadLettersNorCommits(t *testing.T) {
	r := newFakeReader(eventBytes(t, "a"))
	dlq := &fakeDLQ{}
	var sleeps []time.Duration
	ctx, cancel := context.WithCancel(context.Background())
	c := loopConsumer(r, dlq, func(context.Context, ce.Event) error {
		cancel() // the process is stopping while the handler runs...
		return &deadLetterError{cause: errors.New("context canceled mid-transaction")}
	}, &sleeps)
	err := c.Run(ctx)
	if !errors.Is(err, context.Canceled) || dlq.count() != 0 || len(r.commits()) != 0 {
		t.Fatalf("err=%v letters=%d commits=%v: the message must stay uncommitted for redelivery", err, dlq.count(), r.commits())
	}
}

func TestHandleMessage_NotACloudEventIsSkippedAndNeverDeadLettered(t *testing.T) {
	flat := []byte(`{"event_id":"e-flat","event_type":"TrailerDocked","occurred_at":"2026-11-05T14:10:00Z"}`)
	var called atomic.Int32
	r := newFakeReader(flat, []byte(`not json`), eventBytes(t, "ok"))
	dlq := &fakeDLQ{}
	var sleeps []time.Duration
	c := loopConsumer(r, dlq, func(context.Context, ce.Event) error { called.Add(1); return nil }, &sleeps)
	runDrained(t, c)
	if called.Load() != 1 || dlq.count() != 0 || !slices.Equal(r.commits(), []int64{0, 1, 2}) {
		t.Fatalf("called=%d letters=%d commits=%v", called.Load(), dlq.count(), r.commits())
	}
}

func TestClose_ReleasesTheReaderAndTheDeadLetterWriter(t *testing.T) {
	r, dlq := newFakeReader(), &fakeDLQ{}
	c := &Consumer{Reader: r, DLQ: dlq}
	if err := c.Close(); err != nil || !r.closed || !dlq.closed {
		t.Fatalf("Close = %v reader=%v dlq=%v", err, r.closed, dlq.closed)
	}
}

func TestRetryPolicy_DefaultsAndCap(t *testing.T) {
	p := RetryPolicy{}.withDefaults()
	if p.Initial != DefaultRetryInitial || p.Max != DefaultRetryMax {
		t.Fatalf("defaults = %+v", p)
	}
	if got := p.next(4 * time.Second); got != DefaultRetryMax {
		t.Fatalf("next(4s) = %v, want the cap", got)
	}
	if got := p.next(time.Second); got != 2*time.Second {
		t.Fatalf("next(1s) = %v", got)
	}
}

func TestSleepCtx_ReturnsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sleepCtx(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("sleepCtx = %v", err)
	}
	if err := sleepCtx(context.Background(), time.Millisecond); err != nil {
		t.Fatal(err)
	}
}
