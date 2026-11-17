// Package memory is the in-memory implementation of every outbound port. It
// backs the unit and BDD tests (never cmd/**: the real-wiring fitness test
// forbids it) and is validated by the same repository contract suite as the
// Postgres adapter. Its unit of work snapshots the state and restores it on
// error, so rollback semantics are real.
package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"

	"github.com/claudioed/dock-scheduling/internal/application/outbox"
	"github.com/claudioed/dock-scheduling/internal/application/ports"
	"github.com/claudioed/dock-scheduling/internal/application/repository"
	"github.com/claudioed/dock-scheduling/internal/domain/carrier"
	"github.com/claudioed/dock-scheduling/internal/domain/dockappointment"
	"github.com/claudioed/dock-scheduling/internal/domain/dockcalendar"
	"github.com/claudioed/dock-scheduling/internal/domain/shared"
)

type door struct {
	zoneID string
	flow   shared.DockFlow
	gln    string
}

// WireFact is one appointment or calendar fact as the Postgres outbox would
// have persisted it: the event name and the wire `data` JSON (ADR 0004, with the
// standard fields of ADR 0013). The in-memory twin renders it through the same
// function the outbox publisher uses, so acceptance tests assert the real
// payload, not a reimplementation of it.
type WireFact struct {
	Event string
	Data  json.RawMessage
}

type asnRow struct {
	cancelled bool
}

type state struct {
	appointments map[string]dockappointment.Snapshot
	calendars    map[string]dockcalendar.Snapshot
	carriers     map[string]carrier.Snapshot
	zones        map[string]string
	doors        map[string]door
	asns         map[string]asnRow
	cutoffs      map[string][]repository.Cutoff
	processed    map[string]struct{}
	idempotency  map[string]repository.IdempotencyRecord
	published    []any
	wire         []WireFact
}

func newState() state {
	return state{
		appointments: map[string]dockappointment.Snapshot{}, calendars: map[string]dockcalendar.Snapshot{}, carriers: map[string]carrier.Snapshot{}, zones: map[string]string{},
		doors: map[string]door{}, asns: map[string]asnRow{}, cutoffs: map[string][]repository.Cutoff{}, processed: map[string]struct{}{},
		idempotency: map[string]repository.IdempotencyRecord{},
	}
}

func (s state) clone() state {
	c := newState()
	for k, v := range s.appointments {
		c.appointments[k] = v
	}
	for k, v := range s.calendars {
		c.calendars[k] = v
	}
	for k, v := range s.carriers {
		c.carriers[k] = v
	}
	for k, v := range s.zones {
		c.zones[k] = v
	}
	for k, v := range s.doors {
		c.doors[k] = v
	}
	for k, v := range s.asns {
		c.asns[k] = v
	}
	for k, v := range s.cutoffs {
		c.cutoffs[k] = v
	}
	for k, v := range s.processed {
		c.processed[k] = v
	}
	for k, v := range s.idempotency {
		c.idempotency[k] = v
	}
	c.published = append([]any(nil), s.published...)
	c.wire = append([]WireFact(nil), s.wire...)
	return c
}

// Store implements every outbound port in memory.
type Store struct {
	mu sync.Mutex
	st state
}

// NewStore returns an empty store.
func NewStore() *Store { return &Store{st: newState()} }

type txKey struct{}

// Do implements ports.UnitOfWork: it serialises units of work, joins an open
// one carried in ctx and restores the snapshot when fn fails.
func (s *Store) Do(ctx context.Context, fn func(ctx context.Context) error) error {
	if ctx.Value(txKey{}) != nil {
		return fn(ctx)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	before := s.st.clone()
	if err := fn(context.WithValue(ctx, txKey{}, true)); err != nil {
		s.st = before
		return err
	}
	return nil
}

// locked runs f under the store lock unless a unit of work already holds it.
func (s *Store) locked(ctx context.Context, f func()) {
	if ctx.Value(txKey{}) == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
	}
	f()
}

// Published returns the domain events enqueued so far, in order.
func (s *Store) Published() []any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]any(nil), s.st.published...)
}

// PublishedWire returns the wire data of the appointment and calendar facts
// enqueued so far, in order (carrier facts are not rendered by this twin).
func (s *Store) PublishedWire() []WireFact {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]WireFact(nil), s.st.wire...)
}

// glnOf is the outbox.GLNLookup over the doors copy; the caller holds the lock.
func (s *Store) glnOf(code string) (string, error) { return s.st.doors[code].gln, nil }

func (s *Store) render(event string, data any, err error) error {
	if err != nil {
		return err
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("render %s: %w", event, err)
	}
	s.st.wire = append(s.st.wire, WireFact{Event: event, Data: raw})
	return nil
}

// PublishAppointmentEvents implements ports.EventPublisher.
func (s *Store) PublishAppointmentEvents(ctx context.Context, events ...dockappointment.Event) (err error) {
	s.locked(ctx, func() {
		for _, e := range events {
			data, derr := outbox.AppointmentFactData(e, s.glnOf)
			if err = s.render(e.EventName(), data, derr); err != nil {
				return
			}
			s.st.published = append(s.st.published, e)
		}
	})
	return err
}

// PublishCalendarEvents implements ports.EventPublisher.
func (s *Store) PublishCalendarEvents(ctx context.Context, events ...dockcalendar.Event) (err error) {
	s.locked(ctx, func() {
		for _, e := range events {
			data, derr := outbox.CalendarFactData(e, s.glnOf)
			if err = s.render(e.EventName(), data, derr); err != nil {
				return
			}
			s.st.published = append(s.st.published, e)
		}
	})
	return err
}

// PublishCarrierEvents implements ports.EventPublisher.
func (s *Store) PublishCarrierEvents(ctx context.Context, events ...carrier.Event) error {
	s.locked(ctx, func() {
		for _, e := range events {
			s.st.published = append(s.st.published, e)
		}
	})
	return nil
}

// Claim implements ports.ProcessedEvents.
func (s *Store) Claim(ctx context.Context, eventID string) (first bool, err error) {
	s.locked(ctx, func() {
		_, seen := s.st.processed[eventID]
		first = !seen
		s.st.processed[eventID] = struct{}{}
	})
	return first, nil
}

// Idempotency returns the ports.IdempotencyStore view of the store.
func (s *Store) Idempotency() ports.IdempotencyStore { return idempotency{s} }

type idempotency struct{ s *Store }

func (i idempotency) Lookup(ctx context.Context, key string) (rec repository.IdempotencyRecord, found bool, err error) {
	i.s.locked(ctx, func() { rec, found = i.s.st.idempotency[key] })
	return rec, found, nil
}

// Save stores rec unless the key already has an answer (the first writer wins).
func (i idempotency) Save(ctx context.Context, rec repository.IdempotencyRecord) error {
	i.s.locked(ctx, func() {
		if _, ok := i.s.st.idempotency[rec.Key]; !ok {
			i.s.st.idempotency[rec.Key] = rec
		}
	})
	return nil
}

func sortedPage(rows []dockappointment.Snapshot, after *repository.AppointmentAfter, limit int) []dockappointment.Snapshot {
	sort.Slice(rows, func(i, j int) bool {
		if !rows[i].WindowStart.Equal(rows[j].WindowStart) {
			return rows[i].WindowStart.Before(rows[j].WindowStart)
		}
		return rows[i].ID < rows[j].ID
	})
	var out []dockappointment.Snapshot
	for _, r := range rows {
		if after != nil && (r.WindowStart.Before(after.WindowStart) || (r.WindowStart.Equal(after.WindowStart) && r.ID <= after.ID)) {
			continue
		}
		out = append(out, r)
		if len(out) == limit {
			break
		}
	}
	return out
}

func matches(r dockappointment.Snapshot, f repository.AppointmentFilter) bool {
	return matchesText(r, f) && matchesRange(r, f) && (!f.AtRiskOnly || r.AtRisk != "")
}

func differs(want, got string) bool { return want != "" && want != got }

func matchesText(r dockappointment.Snapshot, f repository.AppointmentFilter) bool {
	return !differs(f.SiteID, r.SiteID) && !differs(f.DoorCode, r.DoorCode) && !differs(f.State, string(r.State)) &&
		!differs(f.Direction, r.Direction) && !differs(f.CarrierSCAC, r.CarrierSCAC)
}

func matchesRange(r dockappointment.Snapshot, f repository.AppointmentFilter) bool {
	if !f.From.IsZero() && !r.WindowEnd.After(f.From) {
		return false
	}
	return f.To.IsZero() || r.WindowStart.Before(f.To)
}
