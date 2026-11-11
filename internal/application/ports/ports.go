// Package ports declares the application's OUT ports: the interfaces the use
// cases drive and the outbound adapters (Postgres, the transactional outbox,
// the in-memory doubles) implement. The package holds interfaces and the
// ErrNotFound sentinel only (the architecture fitness test enforces
// interfaces-only here); the read DTOs the inbound adapters consume live one
// package over, in internal/application/views.
package ports

import (
	"context"
	"errors"
	"time"

	"github.com/claudioed/dock-scheduling/internal/application/repository"
	"github.com/claudioed/dock-scheduling/internal/domain/carrier"
	"github.com/claudioed/dock-scheduling/internal/domain/dockappointment"
	"github.com/claudioed/dock-scheduling/internal/domain/dockcalendar"
	"github.com/claudioed/dock-scheduling/internal/domain/shared"
)

// ErrNotFound is the typed miss every Get port returns: the requested
// appointment does not exist. The HTTP adapter maps it to 404; the yard-fact
// policies treat it as "ignore and count". errors.Is is the test.
var ErrNotFound = errors.New("ports: not found")

// AppointmentRepo persists DockAppointment aggregates by appointment id.
// Inside a UnitOfWork it joins the transaction carried in ctx.
type AppointmentRepo interface {
	// Save inserts the appointment when expectedVersion is 0, else updates it
	// guarded by the version the caller loaded. A stale version is
	// dockappointment.ErrConcurrentUpdate; the storage backstops surface as
	// dockappointment.ErrDoorDoubleBooked (exclusion constraint) and
	// dockappointment.ErrAsnAlreadyScheduled (unique ASN index).
	Save(ctx context.Context, a *dockappointment.DockAppointment, expectedVersion int64) error
	// Get returns the appointment, or ErrNotFound.
	Get(ctx context.Context, id string) (*dockappointment.DockAppointment, error)
	// GetForUpdate is Get with a row lock held until the unit of work ends.
	GetForUpdate(ctx context.Context, id string) (*dockappointment.DockAppointment, error)
	// LockDoor takes the per-door advisory lock for the rest of the unit of work.
	LockDoor(ctx context.Context, door shared.DoorCode) error
	// ListActiveOnDoor returns the BOOKED and CHECKED_IN appointments of a door.
	ListActiveOnDoor(ctx context.Context, door shared.DoorCode) ([]*dockappointment.DockAppointment, error)
	// ActiveIDsByASN returns the ids of the active appointments covering any of
	// the ASN numbers, other than the excluded id ("" excludes none).
	ActiveIDsByASN(ctx context.Context, asns []string, excluding string) ([]string, error)
	// ActiveBookings returns the active appointments of a site as calendar
	// bookings, narrowed to one door when door is non-empty.
	ActiveBookings(ctx context.Context, site shared.SiteID, door shared.DoorCode) ([]dockcalendar.Booking, error)
	// ActiveByASN returns the BOOKED INBOUND appointments covering the ASN.
	ActiveByASN(ctx context.Context, asn string) ([]*dockappointment.DockAppointment, error)
	// ListBookedEndedBy returns the ids of BOOKED appointments whose window ended at or before t.
	ListBookedEndedBy(ctx context.Context, t time.Time, limit int) ([]string, error)
	// ListPage returns at most limit appointments matching the filter ordered
	// by (window start, id), strictly after `after` (nil = first page).
	ListPage(ctx context.Context, f repository.AppointmentFilter, after *repository.AppointmentAfter, limit int) ([]*dockappointment.DockAppointment, error)
}

// CalendarRepo persists DockCalendar aggregates by site id.
type CalendarRepo interface {
	// Get returns the site's calendar; a site that never declared anything
	// answers the lazily created default calendar (version 0), never a miss.
	Get(ctx context.Context, site shared.SiteID) (*dockcalendar.DockCalendar, error)
	// GetForUpdate is Get with a lock held until the unit of work ends.
	GetForUpdate(ctx context.Context, site shared.SiteID) (*dockcalendar.DockCalendar, error)
	// Save inserts (expectedVersion 0) or updates guarded by the loaded
	// version; a stale version is dockappointment.ErrConcurrentUpdate.
	Save(ctx context.Context, c *dockcalendar.DockCalendar, expectedVersion int64) error
}

// CarrierRepo persists Carrier aggregates by SCAC (the carrier registry).
type CarrierRepo interface {
	// Get returns the carrier, or ErrNotFound when the SCAC is not registered.
	Get(ctx context.Context, scac carrier.Scac) (*carrier.Carrier, error)
	// GetForUpdate is Get with a per-SCAC lock held until the unit of work
	// ends, so two registrations of one SCAC are serialised (a miss is still
	// ErrNotFound).
	GetForUpdate(ctx context.Context, scac carrier.Scac) (*carrier.Carrier, error)
	// Save inserts the carrier when expectedVersion is 0, else updates it
	// guarded by the version the caller loaded; a stale version is
	// dockappointment.ErrConcurrentUpdate.
	Save(ctx context.Context, c *carrier.Carrier, expectedVersion int64) error
	// ListPage returns at most limit carriers matching the filter ordered by
	// SCAC, strictly after the SCAC `after` ("" = first page).
	ListPage(ctx context.Context, f repository.CarrierFilter, after string, limit int) ([]*carrier.Carrier, error)
}

// DoorView is the local copy of facility-layout's dock doors and zones.
type DoorView interface {
	// Door returns the bookable door, or nil when the door is unknown, not a
	// Dock slot, decommissioned, or its zone (hence site) has not arrived yet.
	Door(ctx context.Context, code shared.DoorCode) (*dockappointment.Door, error)
	// UpsertZone records zoneId -> siteCode.
	UpsertZone(ctx context.Context, zoneID, siteCode string) error
	// UpsertDoor records a Dock slot; RemoveDoor deletes a slot of any role.
	UpsertDoor(ctx context.Context, code, zoneID string, flow shared.DockFlow) error
	// RemoveDoor deletes the door (a re-registration with another role or a decommission).
	RemoveDoor(ctx context.Context, code string) error
}

// AsnView is the local copy of inbound-receiving's ASNs.
type AsnView interface {
	// Statuses returns the status of each number (absent = never seen).
	Statuses(ctx context.Context, numbers []string) (map[string]dockappointment.AsnStatus, error)
	// Register records a registered ASN; a cancelled ASN stays cancelled.
	Register(ctx context.Context, number string, expectedArrival time.Time) error
	// Cancel marks the ASN cancelled (inserting it when unseen).
	Cancel(ctx context.Context, number string) error
}

// CutoffView is the local copy of process-path-management's CPT schedule.
type CutoffView interface {
	// Replace swaps the whole schedule of a site.
	Replace(ctx context.Context, site, timezone string, cutoffs []repository.Cutoff) error
	// Known reports which of the cpt ids exist in the site's schedule.
	Known(ctx context.Context, site shared.SiteID, ids []string) (map[string]bool, error)
}

// ProcessedEvents is the inbound consumers' dedupe guard (one row per
// CloudEvents id). Claim runs inside the SAME UnitOfWork as the effect it
// guards, so a rollback un-claims the id.
type ProcessedEvents interface {
	// Claim records eventID as processed, returning false when an earlier
	// call already recorded it (the duplicate outcome).
	Claim(ctx context.Context, eventID string) (bool, error)
}

// IdempotencyStore keeps the answers of requests made with an
// Idempotency-Key. The first writer of a key wins.
type IdempotencyStore interface {
	// Lookup returns the stored answer of key, or found=false.
	Lookup(ctx context.Context, key string) (repository.IdempotencyRecord, bool, error)
	// Save stores rec unless the key already has an answer.
	Save(ctx context.Context, rec repository.IdempotencyRecord) error
}

// EventPublisher is the write side of the transactional outbox, in domain
// terms: called in the same UnitOfWork as the aggregate's Save.
type EventPublisher interface {
	// PublishAppointmentEvents enqueues the facts an appointment raised.
	PublishAppointmentEvents(ctx context.Context, events ...dockappointment.Event) error
	// PublishCalendarEvents enqueues the facts a calendar raised.
	PublishCalendarEvents(ctx context.Context, events ...dockcalendar.Event) error
	// PublishCarrierEvents enqueues the facts a carrier raised.
	PublishCarrierEvents(ctx context.Context, events ...carrier.Event) error
}

// Metrics counts what the policies deliberately ignore.
type Metrics interface {
	// IgnoredFact counts a yard or ASN fact that changed nothing, by reason.
	IgnoredFact(reason string)
	// ImplicitArrival counts a door release applied to a BOOKED appointment.
	ImplicitArrival()
}

// IDGen mints aggregate ids.
type IDGen interface {
	NewAppointmentID() string
	NewClosureID() string
}

// AdherenceReader is the read side of the schedule-adherence report (D-13):
// the analytics store's reader implements it (ADR 0006), and cmd/reports serves
// it.
type AdherenceReader interface {
	// ScheduleAdherence returns one row per (site, carrier) that has at least
	// one counted appointment in the range, ordered by site then carrier.
	ScheduleAdherence(ctx context.Context, q repository.AdherenceQuery) ([]repository.AdherenceRow, error)
}

// FreshnessReader tells how far the analytics projection has got.
type FreshnessReader interface {
	// Freshness returns the CloudEvents time of the newest event the projection
	// applied, or nil before the first one.
	Freshness(ctx context.Context) (*time.Time, error)
}

// Clock is the service's notion of "now"; the domain never reads a clock.
type Clock interface {
	Now() time.Time
}

// UnitOfWork runs fn as ONE atomic unit: every port call fn makes with the
// ctx it is handed commits together when fn returns nil, or rolls back
// together when fn returns an error. Do is re-entrant: it JOINS an open unit
// of work carried in ctx. The error fn returns is returned unchanged.
type UnitOfWork interface {
	Do(ctx context.Context, fn func(ctx context.Context) error) error
}
