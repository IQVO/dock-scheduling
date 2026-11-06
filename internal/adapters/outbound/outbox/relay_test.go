package outbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	appoutbox "github.com/claudioed/dock-scheduling/internal/application/outbox"
)

// fakeStore records what Drain was asked for and replays a scripted pass:
// every row is handed to handle; the first row whose name is in failOn gets
// the scripted failure instead.
type fakeStore struct {
	mu          sync.Mutex
	calls       []drainCall
	rows        []appoutbox.Row
	failOn      map[string]error
	failAttempt int // attempts count the scripted failure reports
}

type drainCall struct {
	limit, maxAttempts int
}

func (s *fakeStore) Drain(ctx context.Context, limit, maxAttempts int, handle func(context.Context, appoutbox.Row) error) (int, error) {
	s.mu.Lock()
	s.calls = append(s.calls, drainCall{limit: limit, maxAttempts: maxAttempts})
	rows := append([]appoutbox.Row(nil), s.rows...)
	failOn := s.failOn
	failAttempt := s.failAttempt
	s.mu.Unlock()

	published := 0
	for _, row := range rows {
		if err, bad := failOn[row.Fact.Event]; bad {
			return published, &appoutbox.SendFailureError{RowID: row.ID, Attempts: failAttempt, Row: row, Err: err}
		}
		if err := handle(ctx, row); err != nil {
			return published, err
		}
		published++
	}
	return published, nil
}

func (s *fakeStore) lastCall() drainCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[len(s.calls)-1]
}

func (s *fakeStore) callsMade() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

// fakeHandler records the rows it was handed.
type fakeHandler struct {
	mu   sync.Mutex
	seen []appoutbox.Row
}

func (h *fakeHandler) Handle(_ context.Context, row appoutbox.Row) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.seen = append(h.seen, row)
	return nil
}

func testRow(id int64, event string) appoutbox.Row {
	return appoutbox.Row{ID: id, Fact: appoutbox.Fact{EventID: "evt-1", Entity: "dockappointment", Event: event, AggregateID: "appt-1"}}
}

func testRows() []appoutbox.Row {
	return []appoutbox.Row{testRow(1, "DockAppointmentBooked"), testRow(2, "DockAppointmentRescheduled")}
}

func testLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(buf, nil))
}

// capRecords parses the ERROR records of buf.
func capRecords(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	dec := json.NewDecoder(buf)
	for dec.More() {
		var rec map[string]any
		if err := dec.Decode(&rec); err != nil {
			t.Fatalf("decode log record: %v", err)
		}
		if rec["level"] == "ERROR" {
			out = append(out, rec)
		}
	}
	return out
}

// RelayOnce hands its batch size and cap to the store and the rows to the
// handler.
func TestRelayOncePassesBatchAndCapToStore(t *testing.T) {
	store := &fakeStore{rows: testRows()}
	handler := &fakeHandler{}
	r := NewOutboxRelay(store, handler, testLogger(&bytes.Buffer{}),
		WithBatchSize(7), WithMaxAttempts(13))

	n, err := r.RelayOnce(context.Background())
	if err != nil || n != 2 {
		t.Fatalf("RelayOnce = %d, %v; want 2, nil", n, err)
	}
	if call := store.lastCall(); call.limit != 7 || call.maxAttempts != 13 {
		t.Fatalf("Drain asked for limit=%d maxAttempts=%d, want 7 and 13", call.limit, call.maxAttempts)
	}
	if len(handler.seen) != 2 || handler.seen[0].Fact.Event != "DockAppointmentBooked" {
		t.Fatalf("handler saw %+v", handler.seen)
	}
}

// The defaults are the fleet's: 100 rows a pass, unlimited retries, 1s
// interval; negative options cannot silently change anything.
func TestRelayDefaultsAndOptionGuards(t *testing.T) {
	store := &fakeStore{rows: testRows()}
	r := NewOutboxRelay(store, &fakeHandler{}, nil)
	if r.batchSize != DefaultBatchSize || r.maxAttempts != 0 || r.interval != DefaultInterval {
		t.Fatalf("defaults: batch=%d cap=%d interval=%s", r.batchSize, r.maxAttempts, r.interval)
	}
	r = NewOutboxRelay(store, &fakeHandler{}, nil, WithMaxAttempts(-5), WithBatchSize(0), WithInterval(-time.Second))
	if r.maxAttempts != 0 || r.batchSize != DefaultBatchSize || r.interval != DefaultInterval {
		t.Fatalf("negative options changed the defaults: batch=%d cap=%d interval=%s", r.batchSize, r.maxAttempts, r.interval)
	}
}

// The cap-reaching failure is logged ONCE, at ERROR, with the row's id,
// event and key; a failure below the cap is not.
func TestRelayLogsAttemptCapExactlyOnceAtTheCap(t *testing.T) {
	boom := errors.New("kafka: broker unreachable")
	for _, tc := range []struct {
		attempts, cap int
		wantRecords   int
	}{
		{attempts: 1, cap: 3, wantRecords: 0},
		{attempts: 2, cap: 3, wantRecords: 0},
		{attempts: 3, cap: 3, wantRecords: 1},
	} {
		var buf bytes.Buffer
		store := &fakeStore{rows: testRows(), failOn: map[string]error{"DockAppointmentBooked": boom}, failAttempt: tc.attempts}
		r := NewOutboxRelay(store, &fakeHandler{}, testLogger(&buf), WithMaxAttempts(tc.cap))

		n, err := r.RelayOnce(context.Background())
		if err == nil || !errors.Is(err, boom) || n != 0 {
			t.Fatalf("attempts=%d: RelayOnce = %d, %v; want 0 and the failure", tc.attempts, n, err)
		}
		recs := capRecords(t, &buf)
		if len(recs) != tc.wantRecords {
			t.Fatalf("attempts=%d cap=%d: %d ERROR record(s), want %d", tc.attempts, tc.cap, len(recs), tc.wantRecords)
		}
		if tc.wantRecords == 1 {
			rec := recs[0]
			for field, want := range map[string]any{
				"msg":          AttemptCapReachedMsg,
				"outbox_id":    float64(1),
				"event_type":   "dockappointment.DockAppointmentBooked",
				"key":          "appt-1",
				"attempts":     float64(tc.attempts),
				"max_attempts": float64(tc.cap),
			} {
				if rec[field] != want {
					t.Errorf("attempts=%d: %s = %v, want %v", tc.attempts, field, rec[field], want)
				}
			}
		}
	}
}

// With the cap at 0 (unlimited), the attempt-cap log never fires.
func TestRelayNoCapNeverLogsAttemptCap(t *testing.T) {
	var buf bytes.Buffer
	store := &fakeStore{rows: testRows(), failOn: map[string]error{"DockAppointmentBooked": errors.New("x")}, failAttempt: 50}
	r := NewOutboxRelay(store, &fakeHandler{}, testLogger(&buf)) // no WithMaxAttempts: unlimited
	if _, err := r.RelayOnce(context.Background()); err == nil {
		t.Fatal("want the pass failure")
	}
	if recs := capRecords(t, &buf); len(recs) != 0 {
		t.Fatalf("unlimited relay logged %d ERROR record(s), want 0", len(recs))
	}
}

// Run stops on cancellation and reports ctx.Err(); a failing pass is logged,
// not fatal.
func TestRelayRunStopsOnCancellation(t *testing.T) {
	var buf bytes.Buffer
	store := &fakeStore{rows: testRows(), failOn: map[string]error{"DockAppointmentBooked": errors.New("down")}, failAttempt: 1}
	r := NewOutboxRelay(store, &fakeHandler{}, testLogger(&buf), WithInterval(10*time.Millisecond))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	time.Sleep(60 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run returned %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop after cancellation")
	}
	if store.callsMade() == 0 {
		t.Fatal("Run never made a pass")
	}
}
