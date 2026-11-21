package http

import (
	"errors"
	"net/http"

	"github.com/claudioed/dock-scheduling/internal/adapters/inbound/http/page"
	"github.com/claudioed/dock-scheduling/internal/application/ports"
	"github.com/claudioed/dock-scheduling/internal/application/usecases"
	"github.com/claudioed/dock-scheduling/internal/domain/carrier"
	"github.com/claudioed/dock-scheduling/internal/domain/dockappointment"
	"github.com/claudioed/dock-scheduling/internal/domain/dockcalendar"
	"github.com/claudioed/dock-scheduling/internal/domain/shared"
)

// problemBaseURI is the namespace of this service's RFC 7807 `type` URIs.
const problemBaseURI = "https://errors.dock-scheduling.warehouse-systems.dev/"

// problem is the fixed (status, slug, title) of one error category; the
// detail comes from the error at write time.
type problem struct {
	status int
	slug   string
	title  string
}

var internalProblem = problem{http.StatusInternalServerError, "internal-error", "Internal server error"}

// Problems the adapter writes itself, before or instead of a use case. None
// of them reaches the replay decision: the idempotency ones are written
// before a key is read or on a replay, not-found and method-not-allowed by
// the router outside the keyed routes, and the other two are 5xx (ADR 0014
// lists them as OUTSIDE).
var (
	idempotencyKeyRequired = problem{http.StatusBadRequest, "idempotency-key-required", "Idempotency-Key header is required"}
	idempotencyKeyInvalid  = problem{http.StatusBadRequest, "idempotency-key-invalid", "Idempotency-Key header is invalid"}
	idempotencyKeyReused   = problem{http.StatusConflict, "idempotency-key-reused", "Idempotency-Key was already used with a different request"}
	notFoundResource       = problem{http.StatusNotFound, "not-found", "Not found"}
	dependencyUnavailable  = problem{http.StatusServiceUnavailable, "dependency-unavailable", "A runtime dependency is unavailable"}
)

var errInvalidReportRange = errors.New("report range is invalid")

// Decision D-10: 409 for conflicts and illegal states, 422 for value-object
// and reference rules; the slug is the kebab-case of the Err name without the
// Err prefix. These helpers build the catalogue entries.
func conflict(slug, title string) problem { return problem{http.StatusConflict, slug, title} }
func unprocessable(slug, title string) problem {
	return problem{http.StatusUnprocessableEntity, slug, title}
}
func badRequest(slug, title string) problem { return problem{http.StatusBadRequest, slug, title} }
func notFound(slug, title string) problem   { return problem{http.StatusNotFound, slug, title} }

// catalogueEntry is one typed error, its problem and the replay class of
// that problem: what the Idempotency-Key middleware does with the answer
// (ADR 0014 holds the reason of every class).
type catalogueEntry struct {
	err error
	p   problem
	// replay is FINAL when the answer cannot change for this request,
	// OUTCOME when the command committed state and then refused (its writes
	// commit with the stored answer), RETRYABLE when nothing was committed
	// and the answer depends on state that can change (the refusal is not
	// stored, the key is released). When unsure the class is RETRYABLE: a
	// refusal that rolled everything back costs nothing to run again, while
	// a wrongly FINAL one pins a refusal that is no longer true for as long
	// as the key row lives (this store has no TTL, ADR 0014 open points).
	replay replayClass
}

// problemCatalogue maps every typed error to its problem, in match order:
// 400 for the request itself, 404 for a missing resource, 409 for conflicts
// and illegal states, 412 for a stale If-Match, 422 for value and reference
// rules. Every entry declares its replay class; a new entry without one
// fails TestEveryCatalogueEntryDeclaresAReplayClass.
var problemCatalogue = []catalogueEntry{
	{errMalformedRequest, badRequest("malformed-request-body", "Malformed request body"), replayFinal},
	{errUnknownQueryParameter, badRequest("unknown-query-parameter", "Query parameter is not supported"), replayFinal},
	{page.ErrInvalidLimit, badRequest("invalid-limit", "Limit is invalid"), replayFinal},
	{page.ErrInvalidCursor, badRequest("invalid-cursor", "Cursor is invalid"), replayFinal},
	{usecases.ErrInvalidListQuery, badRequest("invalid-cursor", "Cursor is invalid"), replayFinal},
	{usecases.ErrInvalidAvailabilityQuery, badRequest("invalid-availability-query", "Availability query is invalid"), replayFinal},
	{errInvalidQuery, badRequest("invalid-query-parameter", "Query parameter is invalid"), replayFinal},
	{errInvalidPathParameter, badRequest("invalid-path-parameter", "Path parameter is invalid"), replayFinal},
	{errInvalidIfMatch, badRequest("invalid-if-match", "If-Match header is invalid"), replayFinal},
	{errInvalidReportRange, badRequest("invalid-report-range", "Report range is invalid"), replayFinal},

	// The appointment id is minted by this service and an appointment row is
	// never deleted (only its asn_claims are, on cancel), so an id not found
	// now is never found later: FINAL. A carrier miss is a different slug and
	// a missing calendar is lazily defaulted, never ErrNotFound.
	{ports.ErrNotFound, notFound("appointment-not-found", "Appointment not found"), replayFinal},
	// A door is an event-fed local copy of a facility-layout fact (ADR 0003):
	// the fact can arrive later, so RETRYABLE.
	{usecases.ErrDoorNotFound, notFound("door-not-found", "Door not found"), replayRetryable},
	// The closure id is minted by this service and a closure is lifted, never
	// deleted: FINAL.
	{dockcalendar.ErrUnknownClosure, notFound("unknown-closure", "Closure not found"), replayFinal},
	// A carrier is named by its caller-chosen SCAC and registered by a later
	// PUT: the fact can arrive later, so RETRYABLE (the fleet natural-key
	// rule, ADR 0014 J4).
	{carrier.ErrCarrierNotFound, notFound("carrier-not-found", "Carrier not found"), replayRetryable},
	// The If-Match names a version and versions only grow (1, +1 per accepted
	// change): a stale one never matches again. FINAL.
	{usecases.ErrPreconditionFailed, problem{http.StatusPreconditionFailed, "precondition-failed", "Precondition failed"}, replayFinal},

	// 409: conflicts and illegal states.
	{dockappointment.ErrDoorDoubleBooked, conflict("door-double-booked", "Door is already booked in an overlapping window"), replayRetryable},
	{dockcalendar.ErrDoorUnavailable, conflict("door-unavailable", "The door is closed in this window"), replayRetryable},
	{dockappointment.ErrAsnAlreadyScheduled, conflict("asn-already-scheduled", "An ASN is already on an active appointment"), replayRetryable},
	{dockcalendar.ErrClosureConflictsWithAppointments, conflict("closure-conflicts-with-appointments", "Closure covers active appointments"), replayRetryable},
	{dockcalendar.ErrHoursConflictWithAppointments, conflict("hours-conflict-with-appointments", "Operating hours would strand active appointments"), replayRetryable},
	{dockcalendar.ErrOverlappingClosure, conflict("overlapping-closure", "Closure overlaps another closure of the door"), replayRetryable},
	// Lifted is a one-way flag (calendar.go LiftClosure sets it and nothing
	// resets it; a later closure is a new id): FINAL.
	{dockcalendar.ErrClosureAlreadyLifted, conflict("closure-already-lifted", "Closure is already lifted"), replayFinal},
	// The appointment state machine only moves forward (Booked -> CheckedIn
	// -> Completed, Booked/CheckedIn -> Cancelled, Booked -> NoShow; commands
	// are legal in Booked or Active only) and no transition re-enters Booked,
	// so a command refused for its state can never pass: FINAL (ADR 0014 J2).
	{dockappointment.ErrIllegalTransition, conflict("illegal-transition", "The command is not legal in the current state"), replayFinal},
	// The optimistic version guard lost a race: the retry reads the new
	// version and passes. RETRYABLE.
	{dockappointment.ErrConcurrentUpdate, conflict("concurrent-update", "The appointment was modified by another request; re-fetch the latest version and retry"), replayRetryable},
	// The clock moves, so the grace passes. RETRYABLE.
	{dockappointment.ErrNoShowTooEarly, conflict("no-show-too-early", "The no-show grace has not passed"), replayRetryable},
	// A plain pre-check (usecases/carriers.go checkCarrierMayBook: a read, no
	// record); a reinstatement clears it. RETRYABLE — the same slug is an
	// OUTCOME in yard-management, where the turn-away is recorded (ADR 0014
	// J1).
	{carrier.ErrCarrierSuspended, conflict("carrier-suspended", "The carrier is suspended and may not book"), replayRetryable},
	// The registered data can be changed by a later update, so the same
	// registration could match later. RETRYABLE.
	{carrier.ErrCarrierAlreadyRegistered, conflict("carrier-already-registered", "The SCAC is registered with different data; update it instead"), replayRetryable},
	// Both directions clear: a reinstatement, a suspension. RETRYABLE.
	{carrier.ErrCarrierAlreadySuspended, conflict("carrier-already-suspended", "The carrier is already suspended"), replayRetryable},
	{carrier.ErrCarrierNotSuspended, conflict("carrier-not-suspended", "The carrier is not suspended"), replayRetryable},

	// 422: value-object and reference rules.
	{shared.ErrInvalidDoorCode, unprocessable("invalid-door-code", "Door code is invalid"), replayFinal},
	{shared.ErrInvalidSiteID, unprocessable("invalid-site-id", "Site id is invalid"), replayFinal},
	{shared.ErrInvalidCarrier, unprocessable("invalid-carrier", "Carrier is invalid"), replayFinal},
	{shared.ErrInvalidDirection, unprocessable("invalid-direction", "Direction is invalid"), replayFinal},
	{dockappointment.ErrInvalidWindow, unprocessable("invalid-window", "Window is invalid"), replayFinal},
	// The window is compared against the clock (commands.go, appointment.go):
	// the same request passes tomorrow. RETRYABLE (ADR 0014 J3).
	{dockappointment.ErrWindowInPast, unprocessable("window-in-past", "Window starts in the past"), replayRetryable},
	// The door copy is event-fed: the door can become known later. RETRYABLE.
	{dockappointment.ErrUnknownDoor, unprocessable("unknown-door", "Door is not a known dock slot"), replayRetryable},
	// The door's flow comes from the facility copy and can change. RETRYABLE.
	{dockappointment.ErrDoorFlowMismatch, unprocessable("door-flow-mismatch", "The door does not take this direction"), replayRetryable},
	// An ASN is a fact fed by inbound-receiving events (ADR 0003): it can
	// arrive or be uncancelled later. RETRYABLE.
	{dockappointment.ErrUnknownAsn, unprocessable("unknown-asn", "An ASN is unknown or cancelled"), replayRetryable},
	{dockappointment.ErrMissingOutboundRef, unprocessable("missing-outbound-ref", "Outbound appointment names no cutoff or shipment"), replayFinal},
	{dockappointment.ErrDuplicateAsn, unprocessable("duplicate-asn", "ASN numbers must be distinct"), replayFinal},
	{dockappointment.ErrInvalidRefs, unprocessable("invalid-refs", "References do not fit the direction"), replayFinal},
	{dockappointment.ErrInvalidReason, unprocessable("invalid-reason", "Cancellation reason is invalid"), replayFinal},
	{dockappointment.ErrInvalidEta, unprocessable("invalid-eta", "ETA is invalid"), replayFinal},
	{dockcalendar.ErrInvalidOperatingHours, unprocessable("invalid-operating-hours", "Operating hours are invalid"), replayFinal},
	{dockcalendar.ErrInvalidSettings, unprocessable("invalid-settings", "Calendar settings are invalid"), replayFinal},
	{dockcalendar.ErrInvalidClosure, unprocessable("invalid-closure", "Closure is invalid"), replayFinal},
	{carrier.ErrInvalidScac, unprocessable("invalid-scac", "SCAC is invalid"), replayFinal},
	{carrier.ErrInvalidUSDOT, unprocessable("invalid-usdot", "USDOT number is invalid"), replayFinal},
	{carrier.ErrInvalidMC, unprocessable("invalid-mc", "MC or MX number is invalid"), replayFinal},
	{carrier.ErrInvalidCarrierName, unprocessable("invalid-carrier-name", "Carrier name is invalid"), replayFinal},
	{carrier.ErrInvalidSuspensionReason, unprocessable("invalid-suspension-reason", "Suspension reason is invalid"), replayFinal},
}

// entryFor returns the first catalogue entry err matches.
func entryFor(err error) (catalogueEntry, bool) {
	for _, entry := range problemCatalogue {
		if errors.Is(err, entry.err) {
			return entry, true
		}
	}
	return catalogueEntry{}, false
}

// problemFor returns the problem of the first catalogue entry err matches,
// or the internal-error problem.
func problemFor(err error) problem {
	if entry, ok := entryFor(err); ok {
		return entry.p
	}
	return internalProblem
}

// replayOf returns the replay class of the first catalogue entry err matches;
// replayUnset for an error the catalogue does not know (a 5xx, never stored).
func replayOf(err error) replayClass {
	entry, _ := entryFor(err)
	return entry.replay
}
