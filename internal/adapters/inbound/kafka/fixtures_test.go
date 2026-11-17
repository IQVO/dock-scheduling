package kafka

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	kafkago "github.com/segmentio/kafka-go"

	"github.com/claudioed/dock-scheduling/internal/adapters/outbound/memory"
	"github.com/claudioed/dock-scheduling/internal/application/usecases"
)

// testdata holds one CloudEvent per consumed type, taken from the producers'
// AsyncAPI examples (yard-management on origin/feature/service,
// inbound-receiving and process-path-management on origin/develop). facility-
// layout publishes no examples, so its fixtures are built from the schema:
// camelCase payload with eventName, eventType and occurredAt.
//
//go:embed testdata/*.json
var testdata embed.FS

// The ids and the door the yard fixtures name.
const (
	fixtureAppointment = "appt-0a1b2c3d-4e5f-4a6b-8c7d-9e0f1a2b3c4d"
	fixtureDoor        = "WH1-DOCK-D01"
	fixtureSite        = "WH1"
	fixtureASN         = "ASN-1001"
)

// The yard example's day: arrived 14:02, docked 14:10, undocked 15:30.
var (
	bookedStart = time.Date(2026, 11, 5, 14, 0, 0, 0, time.UTC)
	bookedEnd   = bookedStart.Add(2 * time.Hour)
	testNow     = time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := testdata.ReadFile("testdata/" + name + ".json")
	if err != nil {
		t.Fatalf("fixture %s: %v", name, err)
	}
	return b
}

// variant decodes a fixture, lets edit change the envelope map (and its
// "data" map) and re-encodes it.
func variant(t *testing.T, name string, edit func(env, data map[string]any)) []byte {
	t.Helper()
	var env map[string]any
	if err := json.Unmarshal(fixture(t, name), &env); err != nil {
		t.Fatal(err)
	}
	data, _ := env["data"].(map[string]any)
	edit(env, data)
	out, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// withID changes the CloudEvents id only: a different occurrence of the same fact.
func withID(t *testing.T, name, id string) []byte {
	return variant(t, name, func(env, _ map[string]any) { env["id"] = id })
}

// withData overrides payload fields (a nil value removes the key... as JSON null).
func withData(t *testing.T, name, id string, fields map[string]any) []byte {
	return variant(t, name, func(env, data map[string]any) {
		env["id"] = id
		for k, v := range fields {
			data[k] = v
		}
	})
}

// typeOf rewrites the CloudEvents type (an unknown type of the same topic).
func typeOf(t *testing.T, name, eventType string) []byte {
	return variant(t, name, func(env, _ map[string]any) { env["type"] = eventType })
}

// ---- fakes -----------------------------------------------------------------

var errDrained = errors.New("fake reader drained")

// fakeReader serves a fixed queue, then fails FetchMessage with errDrained so
// Run returns. It records the commits.
type fakeReader struct {
	mu          sync.Mutex
	queue       []kafkago.Message
	next        int
	committed   []int64
	commitFails int
	closed      bool
}

func newFakeReader(values ...[]byte) *fakeReader {
	r := &fakeReader{}
	for i, v := range values {
		r.queue = append(r.queue, kafkago.Message{Partition: 0, Offset: int64(i), Key: []byte("k"), Value: v})
	}
	return r
}

func (r *fakeReader) FetchMessage(ctx context.Context) (kafkago.Message, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return kafkago.Message{}, err
	}
	if r.next >= len(r.queue) {
		return kafkago.Message{}, errDrained
	}
	m := r.queue[r.next]
	r.next++
	return m, nil
}

func (r *fakeReader) CommitMessages(_ context.Context, msgs ...kafkago.Message) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.commitFails > 0 {
		r.commitFails--
		return errors.New("commit failed")
	}
	for _, m := range msgs {
		r.committed = append(r.committed, m.Offset)
	}
	return nil
}

func (r *fakeReader) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	return nil
}

func (r *fakeReader) commits() []int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]int64(nil), r.committed...)
}

// fakeDLQ records dead letters; the first failFirst writes fail.
type fakeDLQ struct {
	mu        sync.Mutex
	letters   []kafkago.Message
	causes    []string
	sources   []string
	failFirst int
	attempts  int
	closed    bool
}

func (d *fakeDLQ) WriteDeadLetter(_ context.Context, source string, msg kafkago.Message, cause error) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.attempts++
	if d.failFirst > 0 {
		d.failFirst--
		return errors.New("dlq down")
	}
	d.letters = append(d.letters, msg)
	d.causes = append(d.causes, cause.Error())
	d.sources = append(d.sources, source)
	return nil
}

func (d *fakeDLQ) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.closed = true
	return nil
}

func (d *fakeDLQ) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.letters)
}

// fakeTransport hands out one fakeReader per (topic, group) request, loaded
// with the queue its test set, and one shared fakeDLQ.
type fakeTransport struct {
	queue   [][]byte
	readers []*fakeReader
	dlq     *fakeDLQ
	asked   [][2]string // (topic, group) of every reader requested
}

func (f *fakeTransport) transport() Transport {
	f.dlq = &fakeDLQ{}
	return Transport{
		Readers: func(topic, group string) Reader {
			f.asked = append(f.asked, [2]string{topic, group})
			r := newFakeReader(f.queue...)
			f.readers = append(f.readers, r)
			return r
		},
		DeadLetters: func(string) DeadLetterWriter { return f.dlq },
		Logger:      slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
		Retry:       RetryPolicy{Initial: time.Millisecond, Max: 2 * time.Millisecond},
	}
}

// ---- the world: memory-backed use cases ------------------------------------

type fixedIDs struct{ ids []string }

func (f *fixedIDs) NewAppointmentID() string {
	id := f.ids[0]
	f.ids = f.ids[1:]
	return id
}
func (f *fixedIDs) NewClosureID() string { return "clo-00000000-0000-4000-8000-000000000001" }

type fixedClock struct{ now time.Time }

func (c *fixedClock) Now() time.Time { return c.now }

type world struct {
	t       *testing.T
	store   *memory.Store
	metrics *memory.Metrics
	ids     *fixedIDs
	clock   *fixedClock
	svc     *usecases.Service
	ft      *fakeTransport
	tr      Transport
}

func newWorld(t *testing.T) *world {
	t.Helper()
	s := memory.NewStore()
	w := &world{t: t, store: s, metrics: &memory.Metrics{}, ids: &fixedIDs{}, clock: &fixedClock{now: testNow}, ft: &fakeTransport{}}
	w.svc = usecases.New(usecases.Deps{
		Appointments: s.Appointments(), Calendars: s.Calendars(), Carriers: s.Carriers(), Doors: s.Doors(), Asns: s.Asns(), Cutoffs: s.Cutoffs(),
		Processed: s, Events: s, Metrics: w.metrics, IDs: w.ids, Clock: w.clock, UoW: s,
	})
	w.tr = w.ft.transport()
	return w
}

// handle pushes raw through consumer c and fails the test on a returned error.
func (w *world) handle(c *Consumer, raw []byte) {
	w.t.Helper()
	if err := c.HandleMessage(context.Background(), raw); err != nil {
		w.t.Fatalf("%s: %v", c.Name, err)
	}
}

func mustConsumer(t *testing.T, c *Consumer, err error) *Consumer {
	t.Helper()
	if err != nil {
		t.Fatalf("build consumer: %v", err)
	}
	return c
}

func (w *world) yard() *Consumer {
	c, err := NewYardConsumer(w.tr, "yard-group", w.svc)
	return mustConsumer(w.t, c, err)
}
func (w *world) facility() *Consumer {
	c, err := NewFacilityConsumer(w.tr, "facility-group", w.svc)
	return mustConsumer(w.t, c, err)
}
func (w *world) asn() *Consumer {
	c, err := NewAsnConsumer(w.tr, "asn-group", w.svc)
	return mustConsumer(w.t, c, err)
}
func (w *world) cutoff() *Consumer {
	c, err := NewCutoffConsumer(w.tr, "cpt-group", w.svc)
	return mustConsumer(w.t, c, err)
}

// seedDockAndAsn registers the zone, two doors (D01 from the fixture, D02 by
// variant) and the ASN through the real consumers, then books the fixture
// appointment on D01 for the yard example's window.
func (w *world) seedBooked() {
	w.t.Helper()
	fac, asn := w.facility(), w.asn()
	w.handle(fac, fixture(w.t, "facility_ZoneRegistered"))
	w.handle(fac, fixture(w.t, "facility_LocationSlotRegistered"))
	w.handle(fac, withData(w.t, "facility_LocationSlotRegistered", "3b0d6c1e-8f2a-4d57-9c41-5a7e2b9d0c10",
		map[string]any{"locationCode": "WH1-DOCK-D02", "dockFlow": "Inbound"}))
	w.handle(asn, fixture(w.t, "inbound_ASNRegistered"))
	w.ids.ids = []string{fixtureAppointment}
	if _, err := w.svc.BookAppointment(context.Background(), usecases.BookCommand{
		DoorCode: fixtureDoor, Direction: "INBOUND", Carrier: "Acme Freight", CarrierSCAC: "ACME",
		WindowStart: bookedStart, WindowEnd: bookedEnd, ASNNumbers: []string{fixtureASN},
	}); err != nil {
		w.t.Fatalf("book the fixture appointment: %v", err)
	}
}

type apptState struct {
	State, Door, Reason, Arrival, Provenance string
	Implicit, AtRisk                         bool
	Version                                  int64
}

func (w *world) appt(id string) apptState {
	w.t.Helper()
	v, err := w.svc.GetAppointment(context.Background(), id)
	if err != nil {
		w.t.Fatalf("GetAppointment(%s): %v", id, err)
	}
	s := apptState{State: v.State, Door: v.DoorCode, Reason: v.CancellationReason, Provenance: v.Provenance, AtRisk: v.AtRisk, Version: v.Version}
	if v.Arrival != nil {
		s.Arrival, s.Implicit = v.Arrival.Status, v.Arrival.Implicit
	}
	return s
}

func (w *world) wantState(id, state string) apptState {
	w.t.Helper()
	s := w.appt(id)
	if s.State != state {
		w.t.Fatalf("appointment %s is %s, want %s (%+v)", id, s.State, state, s)
	}
	return s
}

func eventIDFor(n int) string { return fmt.Sprintf("00000000-0000-4000-8000-%012d", n) }
