package postgres

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/claudioed/dock-scheduling/internal/application/repository"
	"github.com/claudioed/dock-scheduling/internal/domain/dockappointment"
	"github.com/claudioed/dock-scheduling/internal/domain/dockcalendar"
	"github.com/claudioed/dock-scheduling/internal/domain/shared"
)

const activeStates = `state IN ('BOOKED', 'CHECKED_IN')`
const startOrder = ` ORDER BY window_start, appointment_id`

// LockDoor implements ports.AppointmentRepo: the per-door transaction-level
// advisory lock. Door codes are globally unique (X5), so the door code alone
// keys the lock. Outside a unit of work the lock is released at once (there
// is no transaction to hold it).
func (r *AppointmentRepo) LockDoor(ctx context.Context, door shared.DoorCode) error {
	if _, err := queryFor(ctx, r.pool).Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "door:"+string(door)); err != nil {
		return fmt.Errorf("lock door %s: %w", door, err)
	}
	return nil
}

// ListActiveOnDoor implements ports.AppointmentRepo.
func (r *AppointmentRepo) ListActiveOnDoor(ctx context.Context, door shared.DoorCode) ([]*dockappointment.DockAppointment, error) {
	return r.collect(ctx, `SELECT `+appointmentColumns+` FROM dock_appointments WHERE door_code = $1 AND `+activeStates+startOrder, string(door))
}

// ActiveIDsByASN implements ports.AppointmentRepo (active native appointments,
// the ones asn_claims holds).
func (r *AppointmentRepo) ActiveIDsByASN(ctx context.Context, asns []string, excluding string) ([]string, error) {
	rows, err := queryFor(ctx, r.pool).Query(ctx, `
		SELECT a.appointment_id FROM dock_appointments a
		WHERE a.appointment_id IN (SELECT appointment_id FROM asn_claims WHERE asn_number = ANY($1)) AND a.appointment_id <> $2
		ORDER BY a.window_start, a.appointment_id`, nonNil(asns), excluding)
	if err != nil {
		return nil, fmt.Errorf("query appointments by asn: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ActiveBookings implements ports.AppointmentRepo.
func (r *AppointmentRepo) ActiveBookings(ctx context.Context, site shared.SiteID, door shared.DoorCode) ([]dockcalendar.Booking, error) {
	sql, args := `SELECT appointment_id, door_code, window_start, window_end FROM dock_appointments WHERE site_id = $1 AND `+activeStates, []any{string(site)}
	if door != "" {
		sql, args = sql+` AND door_code = $2`, append(args, string(door))
	}
	rows, err := queryFor(ctx, r.pool).Query(ctx, sql+startOrder, args...)
	if err != nil {
		return nil, fmt.Errorf("query active bookings: %w", err)
	}
	defer rows.Close()
	var out []dockcalendar.Booking
	for rows.Next() {
		var b dockcalendar.Booking
		var code string
		if err := rows.Scan(&b.AppointmentID, &code, &b.Start, &b.End); err != nil {
			return nil, err
		}
		b.Door, b.Start, b.End = shared.DoorCode(code), b.Start.UTC(), b.End.UTC()
		out = append(out, b)
	}
	return out, rows.Err()
}

// ActiveByASN implements ports.AppointmentRepo: BOOKED INBOUND appointments
// covering the ASN, native or legacy.
func (r *AppointmentRepo) ActiveByASN(ctx context.Context, asn string) ([]*dockappointment.DockAppointment, error) {
	return r.collect(ctx, `SELECT `+appointmentColumns+` FROM dock_appointments
		WHERE state = 'BOOKED' AND direction = 'INBOUND' AND asn_numbers @> ARRAY[$1]::text[]`+startOrder, asn)
}

// ListBookedEndedBy implements ports.AppointmentRepo.
func (r *AppointmentRepo) ListBookedEndedBy(ctx context.Context, t time.Time, limit int) ([]string, error) {
	rows, err := queryFor(ctx, r.pool).Query(ctx, `
		SELECT appointment_id FROM dock_appointments WHERE state = 'BOOKED' AND window_end <= $1`+startOrder+` LIMIT $2`, t.UTC(), limit)
	if err != nil {
		return nil, fmt.Errorf("query ended bookings: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ListPage implements ports.AppointmentRepo: keyset pagination ordered by
// (window start, id).
func (r *AppointmentRepo) ListPage(ctx context.Context, f repository.AppointmentFilter, after *repository.AppointmentAfter, limit int) ([]*dockappointment.DockAppointment, error) {
	var (
		where []string
		args  []any
	)
	add := func(cond string, v any) {
		args = append(args, v)
		where = append(where, strings.ReplaceAll(cond, "?", "$"+strconv.Itoa(len(args))))
	}
	for _, c := range []struct{ col, val string }{
		{"site_id", f.SiteID}, {"door_code", f.DoorCode}, {"state", f.State}, {"direction", f.Direction}, {"carrier_scac", f.CarrierSCAC},
	} {
		if c.val != "" {
			add(c.col+" = ?", c.val)
		}
	}
	if f.AtRiskOnly {
		where = append(where, `at_risk_cause <> ''`)
	}
	if !f.From.IsZero() {
		add("window_end > ?", f.From.UTC())
	}
	if !f.To.IsZero() {
		add("window_start < ?", f.To.UTC())
	}
	if after != nil {
		args = append(args, after.WindowStart.UTC(), after.ID)
		where = append(where, fmt.Sprintf("(window_start, appointment_id) > ($%d, $%d)", len(args)-1, len(args)))
	}
	sql := `SELECT ` + appointmentColumns + ` FROM dock_appointments`
	if len(where) > 0 {
		sql += ` WHERE ` + strings.Join(where, " AND ")
	}
	args = append(args, limit)
	return r.collect(ctx, sql+startOrder+` LIMIT $`+strconv.Itoa(len(args)), args...)
}
