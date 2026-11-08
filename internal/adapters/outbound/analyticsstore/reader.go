package analyticsstore

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/claudioed/dock-scheduling/internal/application/ports"
	"github.com/claudioed/dock-scheduling/internal/application/repository"
)

// Reader answers the report queries over the READ-ONLY pool. It writes nothing.
type Reader struct{ pool *pgxpool.Pool }

var (
	_ ports.AdherenceReader = (*Reader)(nil)
	_ ports.FreshnessReader = (*Reader)(nil)
)

// NewReader builds the reader over a pool from NewReadOnlyPool.
func NewReader(pool *pgxpool.Pool) *Reader { return &Reader{pool: pool} }

// carrierExpr is the carrier a row reports under: its SCAC, or "-" when the
// appointment had none (a legacy import).
const carrierExpr = `COALESCE(NULLIF(carrier_scac, ''), '-')`

// ScheduleAdherence counts, per (site, carrier), the appointments whose
// window starts in [From, To) by outcome. Only appointments with an outcome
// count (appointment_facts.outcome, ADR 0006): an arrival by its status, an
// implicit arrival (completed with no check-in) as ON_TIME (X6), a no-show.
// SQL only counts; the shares are computed by the REST adapter.
func (r *Reader) ScheduleAdherence(ctx context.Context, q repository.AdherenceQuery) ([]repository.AdherenceRow, error) {
	rows, err := r.pool.Query(ctx, `
SELECT site_id, `+carrierExpr+` AS carrier,
       count(*) FILTER (WHERE outcome = 'ON_TIME'),
       count(*) FILTER (WHERE outcome = 'EARLY'),
       count(*) FILTER (WHERE outcome = 'LATE'),
       count(*) FILTER (WHERE outcome = 'NO_SHOW')
FROM appointment_facts
WHERE outcome IS NOT NULL
  AND ref_at >= $1 AND ref_at < $2
  AND ($3::text = '' OR site_id = $3)
  AND ($4::text = '' OR `+carrierExpr+` = $4)
GROUP BY site_id, `+carrierExpr+`
ORDER BY site_id, carrier`, q.From, q.To, q.SiteID, q.CarrierSCAC)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []repository.AdherenceRow{}
	for rows.Next() {
		var row repository.AdherenceRow
		if err := rows.Scan(&row.SiteID, &row.CarrierSCAC, &row.OnTime, &row.Early, &row.Late, &row.NoShow); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// Freshness is the CloudEvents time of the newest applied event, nil before the
// first.
func (r *Reader) Freshness(ctx context.Context) (*time.Time, error) {
	var asOf *time.Time
	if err := r.pool.QueryRow(ctx, `SELECT max(occurred_at) FROM analytics_processed_events`).Scan(&asOf); err != nil {
		return nil, err
	}
	if asOf != nil {
		u := asOf.UTC()
		asOf = &u
	}
	return asOf, nil
}
