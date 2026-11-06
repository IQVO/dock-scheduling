package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/claudioed/dock-scheduling/internal/domain/dockappointment"
	"github.com/claudioed/dock-scheduling/internal/domain/dockcalendar"
	"github.com/claudioed/dock-scheduling/internal/domain/shared"
)

// CalendarRepo is the pgx-backed ports.CalendarRepo over dock_calendars,
// calendar_operating_hours and calendar_closures (migration 0001).
type CalendarRepo struct {
	pool *pgxpool.Pool
}

// NewCalendarRepo constructs a CalendarRepo over pool.
func NewCalendarRepo(pool *pgxpool.Pool) *CalendarRepo { return &CalendarRepo{pool: pool} }

// Get implements ports.CalendarRepo: a site that never declared anything
// answers the lazily created default calendar (version 0).
func (r *CalendarRepo) Get(ctx context.Context, site shared.SiteID) (*dockcalendar.DockCalendar, error) {
	q := queryFor(ctx, r.pool)
	snap := dockcalendar.Snapshot{SiteID: string(site)}
	err := q.QueryRow(ctx, `
		SELECT max_window_minutes, slot_granularity_minutes, arrival_tolerance_minutes, no_show_grace_minutes, version
		FROM dock_calendars WHERE site_id = $1`, string(site)).
		Scan(&snap.Settings.MaxWindowMinutes, &snap.Settings.SlotGranularityMinutes, &snap.Settings.ArrivalToleranceMinutes, &snap.Settings.NoShowGraceMinutes, &snap.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		return dockcalendar.New(string(site))
	}
	if err != nil {
		return nil, fmt.Errorf("read calendar %s: %w", site, err)
	}
	if snap.Hours, err = r.hours(ctx, q, string(site)); err != nil {
		return nil, err
	}
	if snap.Closures, err = r.closures(ctx, q, string(site)); err != nil {
		return nil, err
	}
	cal, err := dockcalendar.Restore(snap)
	if err != nil {
		return nil, fmt.Errorf("restore calendar %s: %w", site, err)
	}
	return cal, nil
}

func (r *CalendarRepo) hours(ctx context.Context, q querier, site string) ([]dockcalendar.HoursSnapshot, error) {
	rows, err := q.Query(ctx, `SELECT door_code, weekday, opens_at, closes_at, timezone FROM calendar_operating_hours WHERE site_id = $1 ORDER BY position`, site)
	if err != nil {
		return nil, fmt.Errorf("read operating hours of %s: %w", site, err)
	}
	defer rows.Close()
	var out []dockcalendar.HoursSnapshot
	for rows.Next() {
		var h dockcalendar.HoursSnapshot
		var weekday string
		if err := rows.Scan(&h.DoorCode, &weekday, &h.OpensAt, &h.ClosesAt, &h.Timezone); err != nil {
			return nil, err
		}
		h.Weekday = dockcalendar.Weekday(weekday)
		out = append(out, h)
	}
	return out, rows.Err()
}

func (r *CalendarRepo) closures(ctx context.Context, q querier, site string) ([]dockcalendar.Closure, error) {
	rows, err := q.Query(ctx, `SELECT closure_id, door_code, from_at, to_at, reason, lifted FROM calendar_closures WHERE site_id = $1 ORDER BY position`, site)
	if err != nil {
		return nil, fmt.Errorf("read closures of %s: %w", site, err)
	}
	defer rows.Close()
	var out []dockcalendar.Closure
	for rows.Next() {
		var c dockcalendar.Closure
		var door string
		var from, to time.Time
		if err := rows.Scan(&c.ID, &door, &from, &to, &c.Reason, &c.Lifted); err != nil {
			return nil, err
		}
		c.Door, c.From, c.To = shared.DoorCode(door), from.UTC(), to.UTC()
		out = append(out, c)
	}
	return out, rows.Err()
}

// GetForUpdate implements ports.CalendarRepo: it takes a per-site advisory
// lock for the rest of the unit of work (a site without a calendar row has
// no row to lock), then reads. The version guard of Save is the second line.
func (r *CalendarRepo) GetForUpdate(ctx context.Context, site shared.SiteID) (*dockcalendar.DockCalendar, error) {
	if _, err := queryFor(ctx, r.pool).Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "calendar:"+string(site)); err != nil {
		return nil, fmt.Errorf("lock calendar %s: %w", site, err)
	}
	return r.Get(ctx, site)
}

// Save implements ports.CalendarRepo: insert (expectedVersion 0) or update
// guarded by the loaded version; the child rows are replaced with the
// snapshot's.
func (r *CalendarRepo) Save(ctx context.Context, c *dockcalendar.DockCalendar, expectedVersion int64) error {
	s := c.Snapshot()
	return inUnit(ctx, r.pool, func(q querier) error {
		if err := r.writeHead(ctx, q, s, expectedVersion); err != nil {
			return err
		}
		if _, err := q.Exec(ctx, `DELETE FROM calendar_operating_hours WHERE site_id = $1`, s.SiteID); err != nil {
			return err
		}
		if _, err := q.Exec(ctx, `DELETE FROM calendar_closures WHERE site_id = $1`, s.SiteID); err != nil {
			return err
		}
		for i, h := range s.Hours {
			if _, err := q.Exec(ctx, `INSERT INTO calendar_operating_hours (site_id, door_code, weekday, opens_at, closes_at, timezone, position)
				VALUES ($1,$2,$3,$4,$5,$6,$7)`, s.SiteID, h.DoorCode, string(h.Weekday), h.OpensAt, h.ClosesAt, h.Timezone, i); err != nil {
				return err
			}
		}
		for i, k := range s.Closures {
			if _, err := q.Exec(ctx, `INSERT INTO calendar_closures (site_id, closure_id, door_code, from_at, to_at, reason, lifted, position)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, s.SiteID, k.ID, string(k.Door), k.From.UTC(), k.To.UTC(), k.Reason, k.Lifted, i); err != nil {
				return err
			}
		}
		return nil
	})
}

func (r *CalendarRepo) writeHead(ctx context.Context, q querier, s dockcalendar.Snapshot, expected int64) error {
	st := s.Settings
	if expected == 0 {
		_, err := q.Exec(ctx, `INSERT INTO dock_calendars (site_id, max_window_minutes, slot_granularity_minutes, arrival_tolerance_minutes, no_show_grace_minutes, version)
			VALUES ($1,$2,$3,$4,$5,$6)`, s.SiteID, st.MaxWindowMinutes, st.SlotGranularityMinutes, st.ArrivalToleranceMinutes, st.NoShowGraceMinutes, s.Version)
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == codeUniqueViolation {
			return dockappointment.ErrConcurrentUpdate
		}
		return err
	}
	tag, err := q.Exec(ctx, `UPDATE dock_calendars SET max_window_minutes=$2, slot_granularity_minutes=$3, arrival_tolerance_minutes=$4,
			no_show_grace_minutes=$5, version=$6 WHERE site_id=$1 AND version=$7`,
		s.SiteID, st.MaxWindowMinutes, st.SlotGranularityMinutes, st.ArrivalToleranceMinutes, st.NoShowGraceMinutes, s.Version, expected)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return dockappointment.ErrConcurrentUpdate
	}
	return nil
}
