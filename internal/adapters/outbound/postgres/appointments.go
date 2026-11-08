package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/claudioed/dock-scheduling/internal/adapters/outbound/postgres/pgtx"
	"github.com/claudioed/dock-scheduling/internal/application/ports"
	"github.com/claudioed/dock-scheduling/internal/domain/dockappointment"
)

// Postgres error codes and constraint names the repository maps to domain errors.
const (
	codeExclusionViolation = "23P01"
	codeUniqueViolation    = "23505"
	constraintAsnClaims    = "asn_claims_pkey"
	constraintApptPrimary  = "dock_appointments_pkey"
	constraintDoorWindow   = "ex_dock_appointments_door_window"
)

const appointmentColumns = `appointment_id, site_id, door_code, direction, carrier, carrier_scac, window_start, window_end,
	asn_numbers, cpt_id, shipment_refs, expected_seal, provenance, door_override, state, version,
	eta_at, eta_reported_at, eta_source, arrival_at, arrival_status, arrival_visit_id, arrival_implicit,
	at_risk_cause, completed_at, cancellation_reason, created_at, updated_at`

// AppointmentRepo is the pgx-backed ports.AppointmentRepo over dock_appointments
// and asn_claims (migration 0001). Every statement runs on the transaction
// carried by ctx when a unit of work is open, else on the pool.
type AppointmentRepo struct {
	pool *pgxpool.Pool
}

// NewAppointmentRepo constructs an AppointmentRepo over pool.
func NewAppointmentRepo(pool *pgxpool.Pool) *AppointmentRepo { return &AppointmentRepo{pool: pool} }

// inUnit runs fn atomically: as a SAVEPOINT of the open unit of work (so a
// failing statement does not poison the caller's transaction) or as its own
// transaction when none is open.
func inUnit(ctx context.Context, pool *pgxpool.Pool, fn func(q querier) error) error {
	var (
		tx  pgx.Tx
		err error
	)
	if outer, ok := pgtx.From(ctx); ok {
		tx, err = outer.Begin(ctx)
	} else {
		tx, err = pool.Begin(ctx)
	}
	if err != nil {
		return fmt.Errorf("begin statement group: %w", err)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback(context.WithoutCancel(ctx))
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit statement group: %w", err)
	}
	return nil
}

// Save implements ports.AppointmentRepo. The storage backstops surface as the
// domain errors the port documents: the door exclusion constraint as
// ErrDoorDoubleBooked, the ASN claim key as ErrAsnAlreadyScheduled, a stale or
// duplicate row as ErrConcurrentUpdate.
func (r *AppointmentRepo) Save(ctx context.Context, a *dockappointment.DockAppointment, expectedVersion int64) error {
	s := a.Snapshot()
	return inUnit(ctx, r.pool, func(q querier) error {
		if err := r.write(ctx, q, s, expectedVersion); err != nil {
			return mapSaveError(err)
		}
		if err := syncAsnClaims(ctx, q, s); err != nil {
			return mapSaveError(err)
		}
		return nil
	})
}

func (r *AppointmentRepo) write(ctx context.Context, q querier, s dockappointment.Snapshot, expected int64) error {
	args := snapshotArgs(s)
	if expected == 0 {
		_, err := q.Exec(ctx, `INSERT INTO dock_appointments (`+appointmentColumns+`)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28)`, args...)
		return err
	}
	// created_at (the 27th argument) never changes: drop it, updated_at becomes $27.
	update := append(append([]any{}, args[:26]...), args[27], expected)
	tag, err := q.Exec(ctx, `UPDATE dock_appointments SET
			site_id=$2, door_code=$3, direction=$4, carrier=$5, carrier_scac=$6, window_start=$7, window_end=$8,
			asn_numbers=$9, cpt_id=$10, shipment_refs=$11, expected_seal=$12, provenance=$13, door_override=$14, state=$15, version=$16,
			eta_at=$17, eta_reported_at=$18, eta_source=$19, arrival_at=$20, arrival_status=$21, arrival_visit_id=$22, arrival_implicit=$23,
			at_risk_cause=$24, completed_at=$25, cancellation_reason=$26, updated_at=$27
		WHERE appointment_id=$1 AND version=$28`, update...)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return dockappointment.ErrConcurrentUpdate
	}
	return nil
}

func snapshotArgs(s dockappointment.Snapshot) []any {
	var etaAt, etaReported, arrivalAt, completed *time.Time
	var etaSource, arrivalStatus *string
	var arrivalVisit string
	var implicit bool
	if s.Eta != nil {
		a, b, c := s.Eta.At.UTC(), s.Eta.ReportedAt.UTC(), s.Eta.Source
		etaAt, etaReported, etaSource = &a, &b, &c
	}
	if s.Arrival != nil {
		a, st := s.Arrival.At.UTC(), string(s.Arrival.Status)
		arrivalAt, arrivalStatus, arrivalVisit, implicit = &a, &st, s.Arrival.VisitID, s.Arrival.Implicit
	}
	if !s.CompletedAt.IsZero() {
		c := s.CompletedAt.UTC()
		completed = &c
	}
	return []any{
		s.ID, s.SiteID, s.DoorCode, s.Direction, s.Carrier, s.CarrierSCAC, s.WindowStart.UTC(), s.WindowEnd.UTC(),
		nonNil(s.ASNNumbers), s.CPTID, nonNil(s.ShipmentRefs), s.ExpectedSeal, string(s.Provenance), s.DoorOverride, string(s.State), s.Version,
		etaAt, etaReported, etaSource, arrivalAt, arrivalStatus, arrivalVisit, implicit,
		string(s.AtRisk), completed, string(s.CancellationReason), s.CreatedAt.UTC(), s.UpdatedAt.UTC(),
	}
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// syncAsnClaims keeps asn_claims at one row per ASN of an ACTIVE NATIVE
// appointment: the unique key is the ErrAsnAlreadyScheduled backstop.
func syncAsnClaims(ctx context.Context, q querier, s dockappointment.Snapshot) error {
	if _, err := q.Exec(ctx, `DELETE FROM asn_claims WHERE appointment_id = $1`, s.ID); err != nil {
		return err
	}
	if !s.State.Active() || s.Provenance != dockappointment.Native || len(s.ASNNumbers) == 0 {
		return nil
	}
	_, err := q.Exec(ctx, `INSERT INTO asn_claims (asn_number, appointment_id) SELECT unnest($1::text[]), $2`, s.ASNNumbers, s.ID)
	return err
}

func mapSaveError(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return err
	}
	switch {
	case pgErr.Code == codeExclusionViolation && pgErr.ConstraintName == constraintDoorWindow:
		return dockappointment.ErrDoorDoubleBooked
	case pgErr.Code == codeUniqueViolation && pgErr.ConstraintName == constraintAsnClaims:
		return dockappointment.ErrAsnAlreadyScheduled
	case pgErr.Code == codeUniqueViolation && pgErr.ConstraintName == constraintApptPrimary:
		return dockappointment.ErrConcurrentUpdate
	}
	return err
}

// Get implements ports.AppointmentRepo.
func (r *AppointmentRepo) Get(ctx context.Context, id string) (*dockappointment.DockAppointment, error) {
	return r.one(ctx, `SELECT `+appointmentColumns+` FROM dock_appointments WHERE appointment_id = $1`, id)
}

// GetForUpdate implements ports.AppointmentRepo: the row stays locked until
// the unit of work ends.
func (r *AppointmentRepo) GetForUpdate(ctx context.Context, id string) (*dockappointment.DockAppointment, error) {
	return r.one(ctx, `SELECT `+appointmentColumns+` FROM dock_appointments WHERE appointment_id = $1 FOR UPDATE`, id)
}

func (r *AppointmentRepo) one(ctx context.Context, sql string, args ...any) (*dockappointment.DockAppointment, error) {
	rows, err := r.collect(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, ports.ErrNotFound
	}
	return rows[0], nil
}

func (r *AppointmentRepo) collect(ctx context.Context, sql string, args ...any) ([]*dockappointment.DockAppointment, error) {
	rows, err := queryFor(ctx, r.pool).Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("query appointments: %w", err)
	}
	defer rows.Close()
	var out []*dockappointment.DockAppointment
	for rows.Next() {
		snap, err := scanSnapshot(rows)
		if err != nil {
			return nil, fmt.Errorf("scan appointment: %w", err)
		}
		a, err := dockappointment.Restore(snap)
		if err != nil {
			return nil, fmt.Errorf("restore appointment %s: %w", snap.ID, err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read appointments: %w", err)
	}
	return out, nil
}

func scanSnapshot(row pgx.Row) (dockappointment.Snapshot, error) {
	var (
		s                                            dockappointment.Snapshot
		provenance, state, atRisk, reason            string
		etaAt, etaReported, arrivalAt, completed     *time.Time
		etaSource, arrivalStatus                     *string
		arrivalVisit                                 string
		implicit                                     bool
		windowStart, windowEnd, createdAt, updatedAt time.Time
	)
	err := row.Scan(&s.ID, &s.SiteID, &s.DoorCode, &s.Direction, &s.Carrier, &s.CarrierSCAC, &windowStart, &windowEnd,
		&s.ASNNumbers, &s.CPTID, &s.ShipmentRefs, &s.ExpectedSeal, &provenance, &s.DoorOverride, &state, &s.Version,
		&etaAt, &etaReported, &etaSource, &arrivalAt, &arrivalStatus, &arrivalVisit, &implicit,
		&atRisk, &completed, &reason, &createdAt, &updatedAt)
	if err != nil {
		return s, err
	}
	s.Provenance, s.State = dockappointment.Provenance(provenance), dockappointment.State(state)
	s.AtRisk, s.CancellationReason = dockappointment.AtRiskCause(atRisk), dockappointment.Reason(reason)
	s.WindowStart, s.WindowEnd, s.CreatedAt, s.UpdatedAt = windowStart.UTC(), windowEnd.UTC(), createdAt.UTC(), updatedAt.UTC()
	if etaAt != nil && etaReported != nil && etaSource != nil {
		s.Eta = &dockappointment.Eta{At: etaAt.UTC(), ReportedAt: etaReported.UTC(), Source: *etaSource}
	}
	if arrivalAt != nil && arrivalStatus != nil {
		s.Arrival = &dockappointment.Arrival{At: arrivalAt.UTC(), Status: dockappointment.ArrivalStatus(*arrivalStatus), VisitID: arrivalVisit, Implicit: implicit}
	}
	if completed != nil {
		s.CompletedAt = completed.UTC()
	}
	return s, nil
}
