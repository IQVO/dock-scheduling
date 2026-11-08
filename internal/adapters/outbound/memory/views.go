package memory

import (
	"context"
	"sync"
	"time"

	"github.com/claudioed/dock-scheduling/internal/application/ports"
	"github.com/claudioed/dock-scheduling/internal/application/repository"
	"github.com/claudioed/dock-scheduling/internal/domain/dockappointment"
	"github.com/claudioed/dock-scheduling/internal/domain/dockcalendar"
	"github.com/claudioed/dock-scheduling/internal/domain/shared"
)

// Calendars returns the ports.CalendarRepo view of the store.
func (s *Store) Calendars() ports.CalendarRepo { return cals{s} }

type cals struct{ s *Store }

func (c cals) Get(ctx context.Context, site shared.SiteID) (cal *dockcalendar.DockCalendar, err error) {
	c.s.locked(ctx, func() {
		snap, ok := c.s.st.calendars[string(site)]
		if !ok {
			cal, err = dockcalendar.New(string(site))
			return
		}
		cal, err = dockcalendar.Restore(snap)
	})
	return cal, err
}

func (c cals) GetForUpdate(ctx context.Context, site shared.SiteID) (*dockcalendar.DockCalendar, error) {
	return c.Get(ctx, site)
}

func (c cals) Save(ctx context.Context, cal *dockcalendar.DockCalendar, expected int64) (err error) {
	snap := cal.Snapshot()
	c.s.locked(ctx, func() {
		cur, exists := c.s.st.calendars[snap.SiteID]
		if (exists && cur.Version != expected) || (!exists && expected != 0) {
			err = dockappointment.ErrConcurrentUpdate
			return
		}
		c.s.st.calendars[snap.SiteID] = snap
	})
	return err
}

// Doors returns the ports.DoorView view of the store.
func (s *Store) Doors() ports.DoorView { return doors{s} }

type doors struct{ s *Store }

func (d doors) Door(ctx context.Context, code shared.DoorCode) (out *dockappointment.Door, err error) {
	d.s.locked(ctx, func() {
		row, ok := d.s.st.doors[string(code)]
		site, zoned := d.s.st.zones[row.zoneID]
		if !ok || !zoned {
			return
		}
		out = &dockappointment.Door{Code: code, Site: shared.SiteID(site), Flow: row.flow}
	})
	return out, nil
}

func (d doors) UpsertZone(ctx context.Context, zoneID, siteCode string) error {
	d.s.locked(ctx, func() { d.s.st.zones[zoneID] = siteCode })
	return nil
}

func (d doors) UpsertDoor(ctx context.Context, code, zoneID string, flow shared.DockFlow) error {
	d.s.locked(ctx, func() { d.s.st.doors[code] = door{zoneID: zoneID, flow: flow} })
	return nil
}

func (d doors) RemoveDoor(ctx context.Context, code string) error {
	d.s.locked(ctx, func() { delete(d.s.st.doors, code) })
	return nil
}

// Asns returns the ports.AsnView view of the store.
func (s *Store) Asns() ports.AsnView { return asns{s} }

type asns struct{ s *Store }

func (a asns) Statuses(ctx context.Context, numbers []string) (map[string]dockappointment.AsnStatus, error) {
	out := map[string]dockappointment.AsnStatus{}
	a.s.locked(ctx, func() {
		for _, n := range numbers {
			row, ok := a.s.st.asns[n]
			switch {
			case !ok:
			case row.cancelled:
				out[n] = dockappointment.AsnCancelledStatus
			default:
				out[n] = dockappointment.AsnRegistered
			}
		}
	})
	return out, nil
}

func (a asns) Register(ctx context.Context, number string, _ time.Time) error {
	a.s.locked(ctx, func() {
		if _, ok := a.s.st.asns[number]; !ok {
			a.s.st.asns[number] = asnRow{}
		}
	})
	return nil
}

func (a asns) Cancel(ctx context.Context, number string) error {
	a.s.locked(ctx, func() { a.s.st.asns[number] = asnRow{cancelled: true} })
	return nil
}

// Cutoffs returns the ports.CutoffView view of the store.
func (s *Store) Cutoffs() ports.CutoffView { return cutoffs{s} }

type cutoffs struct{ s *Store }

func (c cutoffs) Replace(ctx context.Context, site, _ string, rows []repository.Cutoff) error {
	c.s.locked(ctx, func() { c.s.st.cutoffs[site] = append([]repository.Cutoff(nil), rows...) })
	return nil
}

func (c cutoffs) Known(ctx context.Context, site shared.SiteID, ids []string) (map[string]bool, error) {
	out := map[string]bool{}
	c.s.locked(ctx, func() {
		for _, cut := range c.s.st.cutoffs[string(site)] {
			for _, id := range ids {
				if cut.ID == id {
					out[id] = true
				}
			}
		}
	})
	return out, nil
}

// Metrics counts what the policies ignore; it implements ports.Metrics.
type Metrics struct {
	mu       sync.Mutex
	ignored  map[string]int
	implicit int
}

// IgnoredFact implements ports.Metrics.
func (m *Metrics) IgnoredFact(reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ignored == nil {
		m.ignored = map[string]int{}
	}
	m.ignored[reason]++
}

// ImplicitArrival implements ports.Metrics.
func (m *Metrics) ImplicitArrival() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.implicit++
}

// Ignored returns how many facts were ignored for reason.
func (m *Metrics) Ignored(reason string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.ignored[reason]
}

// Implicit returns how many implicit arrivals were counted.
func (m *Metrics) Implicit() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.implicit
}
