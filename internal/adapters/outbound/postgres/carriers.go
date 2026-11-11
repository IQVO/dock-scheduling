package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/claudioed/dock-scheduling/internal/application/ports"
	"github.com/claudioed/dock-scheduling/internal/application/repository"
	"github.com/claudioed/dock-scheduling/internal/domain/carrier"
	"github.com/claudioed/dock-scheduling/internal/domain/dockappointment"
)

const carrierColumns = `scac, name, usdot, mc, status, suspension_reason, suspended_at, version, created_at, updated_at`

// CarrierRepo is the pgx-backed ports.CarrierRepo over carriers (migration
// 0003). Every statement runs on the transaction carried by ctx when a unit of
// work is open, else on the pool.
type CarrierRepo struct {
	pool *pgxpool.Pool
}

// NewCarrierRepo constructs a CarrierRepo over pool.
func NewCarrierRepo(pool *pgxpool.Pool) *CarrierRepo { return &CarrierRepo{pool: pool} }

// Get implements ports.CarrierRepo.
func (r *CarrierRepo) Get(ctx context.Context, scac carrier.Scac) (*carrier.Carrier, error) {
	row := queryFor(ctx, r.pool).QueryRow(ctx, `SELECT `+carrierColumns+` FROM carriers WHERE scac = $1`, string(scac))
	c, err := scanCarrier(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ports.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read carrier %s: %w", scac, err)
	}
	return c, nil
}

// GetForUpdate implements ports.CarrierRepo: it takes a per-SCAC advisory lock
// for the rest of the unit of work (an unregistered SCAC has no row to lock, so
// two first registrations of it are serialised here), then reads. The version
// guard of Save is the second line.
func (r *CarrierRepo) GetForUpdate(ctx context.Context, scac carrier.Scac) (*carrier.Carrier, error) {
	if _, err := queryFor(ctx, r.pool).Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "carrier:"+string(scac)); err != nil {
		return nil, fmt.Errorf("lock carrier %s: %w", scac, err)
	}
	return r.Get(ctx, scac)
}

// Save implements ports.CarrierRepo: insert (expectedVersion 0) or update
// guarded by the loaded version.
func (r *CarrierRepo) Save(ctx context.Context, c *carrier.Carrier, expectedVersion int64) error {
	s := c.Snapshot()
	q := queryFor(ctx, r.pool)
	if expectedVersion == 0 {
		_, err := q.Exec(ctx, `INSERT INTO carriers (`+carrierColumns+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
			s.SCAC, s.Name, s.USDOT, s.MC, string(s.Status), s.Reason, nullableTime(s.SuspendedAt), s.Version, s.CreatedAt.UTC(), s.UpdatedAt.UTC())
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == codeUniqueViolation {
			return dockappointment.ErrConcurrentUpdate
		}
		return err
	}
	tag, err := q.Exec(ctx, `UPDATE carriers SET name=$2, usdot=$3, mc=$4, status=$5, suspension_reason=$6, suspended_at=$7, version=$8, updated_at=$9
		WHERE scac=$1 AND version=$10`,
		s.SCAC, s.Name, s.USDOT, s.MC, string(s.Status), s.Reason, nullableTime(s.SuspendedAt), s.Version, s.UpdatedAt.UTC(), expectedVersion)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return dockappointment.ErrConcurrentUpdate
	}
	return nil
}

// ListPage implements ports.CarrierRepo: ordered by SCAC (byte order, the
// column's "C" collation, the order the in-memory adapter uses), strictly after
// `after`.
func (r *CarrierRepo) ListPage(ctx context.Context, f repository.CarrierFilter, after string, limit int) ([]*carrier.Carrier, error) {
	rows, err := queryFor(ctx, r.pool).Query(ctx, `SELECT `+carrierColumns+` FROM carriers
		WHERE ($1 = '' OR status = $1) AND scac > $2 ORDER BY scac LIMIT $3`, f.Status, after, limit)
	if err != nil {
		return nil, fmt.Errorf("list carriers: %w", err)
	}
	defer rows.Close()
	var out []*carrier.Carrier
	for rows.Next() {
		c, err := scanCarrier(rows)
		if err != nil {
			return nil, fmt.Errorf("read carrier: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func scanCarrier(row pgx.Row) (*carrier.Carrier, error) {
	var (
		s           carrier.Snapshot
		status      string
		suspendedAt *time.Time
	)
	if err := row.Scan(&s.SCAC, &s.Name, &s.USDOT, &s.MC, &status, &s.Reason, &suspendedAt, &s.Version, &s.CreatedAt, &s.UpdatedAt); err != nil {
		return nil, err
	}
	s.Status = carrier.Status(status)
	if suspendedAt != nil {
		s.SuspendedAt = suspendedAt.UTC()
	}
	return carrier.Restore(s)
}
