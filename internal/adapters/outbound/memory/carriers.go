package memory

import (
	"context"
	"sort"

	"github.com/claudioed/dock-scheduling/internal/application/ports"
	"github.com/claudioed/dock-scheduling/internal/application/repository"
	"github.com/claudioed/dock-scheduling/internal/domain/carrier"
	"github.com/claudioed/dock-scheduling/internal/domain/dockappointment"
)

// Carriers returns the ports.CarrierRepo view of the store.
func (s *Store) Carriers() ports.CarrierRepo { return carriers{s} }

type carriers struct{ s *Store }

func (c carriers) Get(ctx context.Context, scac carrier.Scac) (out *carrier.Carrier, err error) {
	c.s.locked(ctx, func() {
		snap, ok := c.s.st.carriers[string(scac)]
		if !ok {
			err = ports.ErrNotFound
			return
		}
		out, err = carrier.Restore(snap)
	})
	return out, err
}

func (c carriers) GetForUpdate(ctx context.Context, scac carrier.Scac) (*carrier.Carrier, error) {
	return c.Get(ctx, scac)
}

func (c carriers) Save(ctx context.Context, ca *carrier.Carrier, expected int64) (err error) {
	snap := ca.Snapshot()
	c.s.locked(ctx, func() {
		cur, exists := c.s.st.carriers[snap.SCAC]
		if (expected == 0 && exists) || (expected != 0 && (!exists || cur.Version != expected)) {
			err = dockappointment.ErrConcurrentUpdate
			return
		}
		c.s.st.carriers[snap.SCAC] = snap
	})
	return err
}

func (c carriers) ListPage(ctx context.Context, f repository.CarrierFilter, after string, limit int) (out []*carrier.Carrier, err error) {
	c.s.locked(ctx, func() {
		var rows []carrier.Snapshot
		for _, r := range c.s.st.carriers {
			if (f.Status == "" || string(r.Status) == f.Status) && r.SCAC > after {
				rows = append(rows, r)
			}
		}
		sort.Slice(rows, func(i, j int) bool { return rows[i].SCAC < rows[j].SCAC })
		if len(rows) > limit {
			rows = rows[:limit]
		}
		for _, r := range rows {
			var ca *carrier.Carrier
			if ca, err = carrier.Restore(r); err != nil {
				return
			}
			out = append(out, ca)
		}
	})
	return out, err
}
