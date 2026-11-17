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

// Problems the adapter writes itself, before or instead of a use case.
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

// problemCatalogue maps every typed error to its problem, in match order.
var problemCatalogue = []struct {
	err error
	p   problem
}{
	{errMalformedRequest, badRequest("malformed-request-body", "Malformed request body")},
	{errUnknownQueryParameter, badRequest("unknown-query-parameter", "Query parameter is not supported")},
	{page.ErrInvalidLimit, badRequest("invalid-limit", "Limit is invalid")},
	{page.ErrInvalidCursor, badRequest("invalid-cursor", "Cursor is invalid")},
	{usecases.ErrInvalidListQuery, badRequest("invalid-cursor", "Cursor is invalid")},
	{usecases.ErrInvalidAvailabilityQuery, badRequest("invalid-availability-query", "Availability query is invalid")},
	{errInvalidQuery, badRequest("invalid-query-parameter", "Query parameter is invalid")},
	{errInvalidPathParameter, badRequest("invalid-path-parameter", "Path parameter is invalid")},
	{errInvalidIfMatch, badRequest("invalid-if-match", "If-Match header is invalid")},
	{errInvalidReportRange, badRequest("invalid-report-range", "Report range is invalid")},

	{ports.ErrNotFound, notFound("appointment-not-found", "Appointment not found")},
	{usecases.ErrDoorNotFound, notFound("door-not-found", "Door not found")},
	{dockcalendar.ErrUnknownClosure, notFound("unknown-closure", "Closure not found")},
	{carrier.ErrCarrierNotFound, notFound("carrier-not-found", "Carrier not found")},
	{usecases.ErrPreconditionFailed, problem{http.StatusPreconditionFailed, "precondition-failed", "Precondition failed"}},

	// 409: conflicts and illegal states.
	{dockappointment.ErrDoorDoubleBooked, conflict("door-double-booked", "Door is already booked in an overlapping window")},
	{dockcalendar.ErrDoorUnavailable, conflict("door-unavailable", "The door is closed in this window")},
	{dockappointment.ErrAsnAlreadyScheduled, conflict("asn-already-scheduled", "An ASN is already on an active appointment")},
	{dockcalendar.ErrClosureConflictsWithAppointments, conflict("closure-conflicts-with-appointments", "Closure covers active appointments")},
	{dockcalendar.ErrHoursConflictWithAppointments, conflict("hours-conflict-with-appointments", "Operating hours would strand active appointments")},
	{dockcalendar.ErrOverlappingClosure, conflict("overlapping-closure", "Closure overlaps another closure of the door")},
	{dockcalendar.ErrClosureAlreadyLifted, conflict("closure-already-lifted", "Closure is already lifted")},
	{dockappointment.ErrIllegalTransition, conflict("illegal-transition", "The command is not legal in the current state")},
	{dockappointment.ErrConcurrentUpdate, conflict("concurrent-update", "The appointment was modified by another request; re-fetch the latest version and retry")},
	{dockappointment.ErrNoShowTooEarly, conflict("no-show-too-early", "The no-show grace has not passed")},
	{carrier.ErrCarrierSuspended, conflict("carrier-suspended", "The carrier is suspended and may not book")},
	{carrier.ErrCarrierAlreadyRegistered, conflict("carrier-already-registered", "The SCAC is registered with different data; update it instead")},
	{carrier.ErrCarrierAlreadySuspended, conflict("carrier-already-suspended", "The carrier is already suspended")},
	{carrier.ErrCarrierNotSuspended, conflict("carrier-not-suspended", "The carrier is not suspended")},

	// 422: value-object and reference rules.
	{shared.ErrInvalidDoorCode, unprocessable("invalid-door-code", "Door code is invalid")},
	{shared.ErrInvalidSiteID, unprocessable("invalid-site-id", "Site id is invalid")},
	{shared.ErrInvalidCarrier, unprocessable("invalid-carrier", "Carrier is invalid")},
	{shared.ErrInvalidDirection, unprocessable("invalid-direction", "Direction is invalid")},
	{dockappointment.ErrInvalidWindow, unprocessable("invalid-window", "Window is invalid")},
	{dockappointment.ErrWindowInPast, unprocessable("window-in-past", "Window starts in the past")},
	{dockappointment.ErrUnknownDoor, unprocessable("unknown-door", "Door is not a known dock slot")},
	{dockappointment.ErrDoorFlowMismatch, unprocessable("door-flow-mismatch", "The door does not take this direction")},
	{dockappointment.ErrUnknownAsn, unprocessable("unknown-asn", "An ASN is unknown or cancelled")},
	{dockappointment.ErrMissingOutboundRef, unprocessable("missing-outbound-ref", "Outbound appointment names no cutoff or shipment")},
	{dockappointment.ErrDuplicateAsn, unprocessable("duplicate-asn", "ASN numbers must be distinct")},
	{dockappointment.ErrInvalidRefs, unprocessable("invalid-refs", "References do not fit the direction")},
	{dockappointment.ErrInvalidReason, unprocessable("invalid-reason", "Cancellation reason is invalid")},
	{dockappointment.ErrInvalidEta, unprocessable("invalid-eta", "ETA is invalid")},
	{dockcalendar.ErrInvalidOperatingHours, unprocessable("invalid-operating-hours", "Operating hours are invalid")},
	{dockcalendar.ErrInvalidSettings, unprocessable("invalid-settings", "Calendar settings are invalid")},
	{dockcalendar.ErrInvalidClosure, unprocessable("invalid-closure", "Closure is invalid")},
	{carrier.ErrInvalidScac, unprocessable("invalid-scac", "SCAC is invalid")},
	{carrier.ErrInvalidUSDOT, unprocessable("invalid-usdot", "USDOT number is invalid")},
	{carrier.ErrInvalidMC, unprocessable("invalid-mc", "MC or MX number is invalid")},
	{carrier.ErrInvalidCarrierName, unprocessable("invalid-carrier-name", "Carrier name is invalid")},
	{carrier.ErrInvalidSuspensionReason, unprocessable("invalid-suspension-reason", "Suspension reason is invalid")},
}

// problemFor returns the problem of the first catalogue entry err matches, or
// the internal-error problem.
func problemFor(err error) problem {
	for _, entry := range problemCatalogue {
		if errors.Is(err, entry.err) {
			return entry.p
		}
	}
	return internalProblem
}
