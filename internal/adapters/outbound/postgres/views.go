package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/claudioed/dock-scheduling/internal/application/repository"
	"github.com/claudioed/dock-scheduling/internal/domain/dockappointment"
	"github.com/claudioed/dock-scheduling/internal/domain/shared"
)

// DoorStore is the pgx-backed ports.DoorView over the zones and doors local
// copies fed by facility-layout.
type DoorStore struct {
	pool *pgxpool.Pool
}

// NewDoorStore constructs a DoorStore over pool.
func NewDoorStore(pool *pgxpool.Pool) *DoorStore { return &DoorStore{pool: pool} }

// Door implements ports.DoorView: nil when the door is unknown or its zone
// (hence site) has not arrived yet.
func (s *DoorStore) Door(ctx context.Context, code shared.DoorCode) (*dockappointment.Door, error) {
	var site, flow string
	err := queryFor(ctx, s.pool).QueryRow(ctx, `
		SELECT z.site_code, d.dock_flow FROM doors d JOIN zones z ON z.zone_id = d.zone_id WHERE d.door_code = $1`, string(code)).Scan(&site, &flow)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil //nolint:nilnil // the port's contract: nil door = not bookable
	}
	if err != nil {
		return nil, fmt.Errorf("read door %s: %w", code, err)
	}
	return &dockappointment.Door{Code: code, Site: shared.SiteID(site), Flow: shared.DockFlow(flow)}, nil
}

// UpsertZone implements ports.DoorView.
func (s *DoorStore) UpsertZone(ctx context.Context, zoneID, siteCode string) error {
	_, err := queryFor(ctx, s.pool).Exec(ctx, `
		INSERT INTO zones (zone_id, site_code) VALUES ($1, $2)
		ON CONFLICT (zone_id) DO UPDATE SET site_code = EXCLUDED.site_code`, zoneID, siteCode)
	if err != nil {
		return fmt.Errorf("upsert zone %s: %w", zoneID, err)
	}
	return nil
}

// UpsertDoor implements ports.DoorView.
func (s *DoorStore) UpsertDoor(ctx context.Context, code, zoneID string, flow shared.DockFlow) error {
	_, err := queryFor(ctx, s.pool).Exec(ctx, `
		INSERT INTO doors (door_code, zone_id, dock_flow) VALUES ($1, $2, $3)
		ON CONFLICT (door_code) DO UPDATE SET zone_id = EXCLUDED.zone_id, dock_flow = EXCLUDED.dock_flow`, code, zoneID, string(flow))
	if err != nil {
		return fmt.Errorf("upsert door %s: %w", code, err)
	}
	return nil
}

// RemoveDoor implements ports.DoorView; an unknown door is a no-op.
func (s *DoorStore) RemoveDoor(ctx context.Context, code string) error {
	if _, err := queryFor(ctx, s.pool).Exec(ctx, `DELETE FROM doors WHERE door_code = $1`, code); err != nil {
		return fmt.Errorf("remove door %s: %w", code, err)
	}
	return nil
}

// AsnStore is the pgx-backed ports.AsnView over the asns local copy fed by
// inbound-receiving.
type AsnStore struct {
	pool *pgxpool.Pool
}

// NewAsnStore constructs an AsnStore over pool.
func NewAsnStore(pool *pgxpool.Pool) *AsnStore { return &AsnStore{pool: pool} }

// Statuses implements ports.AsnView: absent = never seen.
func (s *AsnStore) Statuses(ctx context.Context, numbers []string) (map[string]dockappointment.AsnStatus, error) {
	out := map[string]dockappointment.AsnStatus{}
	if len(numbers) == 0 {
		return out, nil
	}
	rows, err := queryFor(ctx, s.pool).Query(ctx, `SELECT asn_number, status FROM asns WHERE asn_number = ANY($1)`, numbers)
	if err != nil {
		return nil, fmt.Errorf("read asn statuses: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var number, status string
		if err := rows.Scan(&number, &status); err != nil {
			return nil, err
		}
		out[number] = dockappointment.AsnRegistered
		if status == "CANCELLED" {
			out[number] = dockappointment.AsnCancelledStatus
		}
	}
	return out, rows.Err()
}

// Register implements ports.AsnView: a cancelled ASN stays cancelled and a
// repeated registration changes nothing.
func (s *AsnStore) Register(ctx context.Context, number string, expectedArrival time.Time) error {
	var arrival *time.Time
	if !expectedArrival.IsZero() {
		a := expectedArrival.UTC()
		arrival = &a
	}
	_, err := queryFor(ctx, s.pool).Exec(ctx, `
		INSERT INTO asns (asn_number, status, expected_arrival, updated_at) VALUES ($1, 'REGISTERED', $2, now())
		ON CONFLICT (asn_number) DO NOTHING`, number, arrival)
	if err != nil {
		return fmt.Errorf("register asn %s: %w", number, err)
	}
	return nil
}

// Cancel implements ports.AsnView: marks the ASN cancelled, inserting it when unseen.
func (s *AsnStore) Cancel(ctx context.Context, number string) error {
	_, err := queryFor(ctx, s.pool).Exec(ctx, `
		INSERT INTO asns (asn_number, status, updated_at) VALUES ($1, 'CANCELLED', now())
		ON CONFLICT (asn_number) DO UPDATE SET status = 'CANCELLED', updated_at = now()`, number)
	if err != nil {
		return fmt.Errorf("cancel asn %s: %w", number, err)
	}
	return nil
}

// CutoffStore is the pgx-backed ports.CutoffView over the cutoff_schedules
// and cutoffs local copy fed by process-path-management.
type CutoffStore struct {
	pool *pgxpool.Pool
}

// NewCutoffStore constructs a CutoffStore over pool.
func NewCutoffStore(pool *pgxpool.Pool) *CutoffStore { return &CutoffStore{pool: pool} }

// Replace implements ports.CutoffView: the whole schedule of the site is swapped.
func (s *CutoffStore) Replace(ctx context.Context, site, timezone string, cutoffs []repository.Cutoff) error {
	return inUnit(ctx, s.pool, func(q querier) error {
		if _, err := q.Exec(ctx, `
			INSERT INTO cutoff_schedules (site_id, timezone) VALUES ($1, $2)
			ON CONFLICT (site_id) DO UPDATE SET timezone = EXCLUDED.timezone`, site, timezone); err != nil {
			return fmt.Errorf("upsert cutoff schedule %s: %w", site, err)
		}
		if _, err := q.Exec(ctx, `DELETE FROM cutoffs WHERE site_id = $1`, site); err != nil {
			return fmt.Errorf("clear cutoffs of %s: %w", site, err)
		}
		for _, c := range cutoffs {
			if _, err := q.Exec(ctx, `
				INSERT INTO cutoffs (site_id, cpt_id, local_time, days_of_week, ship_method) VALUES ($1,$2,$3,$4,$5)
				ON CONFLICT (site_id, cpt_id) DO UPDATE SET local_time = EXCLUDED.local_time, days_of_week = EXCLUDED.days_of_week, ship_method = EXCLUDED.ship_method`,
				site, c.ID, c.LocalTime, nonNil(c.DaysOfWeek), c.ShipMethod); err != nil {
				return fmt.Errorf("insert cutoff %s of %s: %w", c.ID, site, err)
			}
		}
		return nil
	})
}

// Known implements ports.CutoffView.
func (s *CutoffStore) Known(ctx context.Context, site shared.SiteID, ids []string) (map[string]bool, error) {
	out := map[string]bool{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := queryFor(ctx, s.pool).Query(ctx, `SELECT cpt_id FROM cutoffs WHERE site_id = $1 AND cpt_id = ANY($2)`, string(site), ids)
	if err != nil {
		return nil, fmt.Errorf("read cutoffs of %s: %w", site, err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}
