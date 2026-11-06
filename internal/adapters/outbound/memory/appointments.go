package memory

import (
	"context"
	"time"

	"github.com/claudioed/dock-scheduling/internal/application/ports"
	"github.com/claudioed/dock-scheduling/internal/application/repository"
	"github.com/claudioed/dock-scheduling/internal/domain/dockappointment"
	"github.com/claudioed/dock-scheduling/internal/domain/dockcalendar"
	"github.com/claudioed/dock-scheduling/internal/domain/shared"
)

// Appointments returns the ports.AppointmentRepo view of the store.
func (s *Store) Appointments() ports.AppointmentRepo { return appts{s} }

type appts struct{ s *Store }

func (a appts) Save(ctx context.Context, ap *dockappointment.DockAppointment, expected int64) (err error) {
	snap := ap.Snapshot()
	a.s.locked(ctx, func() {
		cur, exists := a.s.st.appointments[snap.ID]
		switch {
		case expected == 0 && exists, expected != 0 && (!exists || cur.Version != expected):
			err = dockappointment.ErrConcurrentUpdate
			return
		}
		if snap.State.Active() {
			if err = a.checkBackstops(snap); err != nil {
				return
			}
		}
		a.s.st.appointments[snap.ID] = snap
	})
	return err
}

// checkBackstops mimics the storage constraints: the door exclusion (native
// appointments) and the ASN uniqueness.
func (a appts) checkBackstops(snap dockappointment.Snapshot) error {
	for id, o := range a.s.st.appointments {
		if id == snap.ID || !o.State.Active() {
			continue
		}
		overlap := o.DoorCode == snap.DoorCode && o.WindowStart.Before(snap.WindowEnd) && snap.WindowStart.Before(o.WindowEnd)
		if overlap && !snap.DoorOverride && !o.DoorOverride {
			return dockappointment.ErrDoorDoubleBooked
		}
		if snap.Provenance == dockappointment.Native && o.Provenance == dockappointment.Native && shareAsn(o.ASNNumbers, snap.ASNNumbers) {
			return dockappointment.ErrAsnAlreadyScheduled
		}
	}
	return nil
}

func shareAsn(a, b []string) bool {
	for _, x := range a {
		for _, y := range b {
			if x == y {
				return true
			}
		}
	}
	return false
}

func (a appts) Get(ctx context.Context, id string) (ap *dockappointment.DockAppointment, err error) {
	a.s.locked(ctx, func() {
		snap, ok := a.s.st.appointments[id]
		if !ok {
			err = ports.ErrNotFound
			return
		}
		ap, err = dockappointment.Restore(snap)
	})
	return ap, err
}

func (a appts) GetForUpdate(ctx context.Context, id string) (*dockappointment.DockAppointment, error) {
	return a.Get(ctx, id)
}

func (a appts) LockDoor(context.Context, shared.DoorCode) error { return nil }

func (a appts) ListActiveOnDoor(ctx context.Context, door shared.DoorCode) (out []*dockappointment.DockAppointment, err error) {
	a.s.locked(ctx, func() {
		out, err = a.collect(func(r dockappointment.Snapshot) bool { return r.State.Active() && r.DoorCode == string(door) })
	})
	return out, err
}

func (a appts) collect(keep func(dockappointment.Snapshot) bool) ([]*dockappointment.DockAppointment, error) {
	var out []*dockappointment.DockAppointment
	for _, r := range sortedPage(a.all(), nil, 1<<30) {
		if !keep(r) {
			continue
		}
		ap, err := dockappointment.Restore(r)
		if err != nil {
			return nil, err
		}
		out = append(out, ap)
	}
	return out, nil
}

func (a appts) all() []dockappointment.Snapshot {
	rows := make([]dockappointment.Snapshot, 0, len(a.s.st.appointments))
	for _, r := range a.s.st.appointments {
		rows = append(rows, r)
	}
	return rows
}

func (a appts) ActiveIDsByASN(ctx context.Context, asns []string, excluding string) (ids []string, err error) {
	a.s.locked(ctx, func() {
		for _, r := range sortedPage(a.all(), nil, 1<<30) {
			if r.ID != excluding && r.State.Active() && r.Provenance == dockappointment.Native && shareAsn(r.ASNNumbers, asns) {
				ids = append(ids, r.ID)
			}
		}
	})
	return ids, nil
}

func (a appts) ActiveBookings(ctx context.Context, site shared.SiteID, door shared.DoorCode) (out []dockcalendar.Booking, err error) {
	a.s.locked(ctx, func() {
		for _, r := range sortedPage(a.all(), nil, 1<<30) {
			if r.State.Active() && r.SiteID == string(site) && (door == "" || r.DoorCode == string(door)) {
				out = append(out, dockcalendar.Booking{AppointmentID: r.ID, Door: shared.DoorCode(r.DoorCode), Start: r.WindowStart, End: r.WindowEnd})
			}
		}
	})
	return out, nil
}

func (a appts) ActiveByASN(ctx context.Context, asn string) (out []*dockappointment.DockAppointment, err error) {
	a.s.locked(ctx, func() {
		out, err = a.collect(func(r dockappointment.Snapshot) bool {
			return r.State == dockappointment.Booked && r.Direction == string(shared.Inbound) && shareAsn(r.ASNNumbers, []string{asn})
		})
	})
	return out, err
}

func (a appts) ListBookedEndedBy(ctx context.Context, t time.Time, limit int) (ids []string, err error) {
	a.s.locked(ctx, func() {
		for _, r := range sortedPage(a.all(), nil, 1<<30) {
			if r.State == dockappointment.Booked && !r.WindowEnd.After(t) && len(ids) < limit {
				ids = append(ids, r.ID)
			}
		}
	})
	return ids, nil
}

func (a appts) ListPage(ctx context.Context, f repository.AppointmentFilter, after *repository.AppointmentAfter, limit int) (out []*dockappointment.DockAppointment, err error) {
	a.s.locked(ctx, func() {
		var rows []dockappointment.Snapshot
		for _, r := range a.all() {
			if matches(r, f) {
				rows = append(rows, r)
			}
		}
		for _, r := range sortedPage(rows, after, limit) {
			var ap *dockappointment.DockAppointment
			if ap, err = dockappointment.Restore(r); err != nil {
				return
			}
			out = append(out, ap)
		}
	})
	return out, err
}
