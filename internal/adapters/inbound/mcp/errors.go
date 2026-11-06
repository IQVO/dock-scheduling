package mcp

import (
	"errors"
	"fmt"
	"log/slog"

	"github.com/claudioed/dock-scheduling/internal/application/ports"
	"github.com/claudioed/dock-scheduling/internal/application/usecases"
	"github.com/claudioed/dock-scheduling/internal/domain/dockappointment"
	"github.com/claudioed/dock-scheduling/internal/domain/dockcalendar"
	"github.com/claudioed/dock-scheduling/internal/domain/shared"
)

// errInvalidArgument marks an argument the adapter itself refuses before any
// use case runs (a timestamp that is not RFC 3339, an unknown enum value).
var errInvalidArgument = errors.New("invalid argument")

// argError builds an invalid-argument refusal.
func argError(format string, a ...any) error {
	return fmt.Errorf("%w: %s", errInvalidArgument, fmt.Sprintf(format, a...))
}

// errorCatalogue names each typed error a tool can meet with the same stable
// slug the REST adapter uses as the last segment of its RFC 7807 `type` URI
// (internal/adapters/inbound/http/errors.go; decision D-10), in the same match
// order, so a client sees one error vocabulary over both surfaces.
// errors_test.go pins the parity slug by slug.
var errorCatalogue = []struct {
	target error
	slug   string
}{
	{errInvalidArgument, "invalid-argument"},
	{usecases.ErrIdempotencyKeyRequired, "idempotency-key-required"},
	{usecases.ErrIdempotencyKeyInvalid, "idempotency-key-invalid"},
	{usecases.ErrIdempotencyKeyReused, "idempotency-key-reused"},
	{usecases.ErrInvalidInitiator, "invalid-initiator"},
	{usecases.ErrInvalidListQuery, "invalid-cursor"},
	{usecases.ErrInvalidAvailabilityQuery, "invalid-availability-query"},

	{ports.ErrNotFound, "appointment-not-found"},
	{usecases.ErrDoorNotFound, "door-not-found"},
	{usecases.ErrPreconditionFailed, "precondition-failed"},

	// 409 on REST: conflicts and illegal states.
	{dockappointment.ErrDoorDoubleBooked, "door-double-booked"},
	{dockcalendar.ErrDoorUnavailable, "door-unavailable"},
	{dockappointment.ErrAsnAlreadyScheduled, "asn-already-scheduled"},
	{dockappointment.ErrIllegalTransition, "illegal-transition"},
	{dockappointment.ErrConcurrentUpdate, "concurrent-update"},

	// 422 on REST: value-object and reference rules.
	{shared.ErrInvalidDoorCode, "invalid-door-code"},
	{shared.ErrInvalidSiteID, "invalid-site-id"},
	{shared.ErrInvalidCarrier, "invalid-carrier"},
	{shared.ErrInvalidDirection, "invalid-direction"},
	{dockappointment.ErrInvalidAppointmentID, "invalid-appointment-id"},
	{dockappointment.ErrInvalidWindow, "invalid-window"},
	{dockappointment.ErrWindowInPast, "window-in-past"},
	{dockappointment.ErrUnknownDoor, "unknown-door"},
	{dockappointment.ErrDoorFlowMismatch, "door-flow-mismatch"},
	{dockappointment.ErrUnknownAsn, "unknown-asn"},
	{dockappointment.ErrMissingOutboundRef, "missing-outbound-ref"},
	{dockappointment.ErrDuplicateAsn, "duplicate-asn"},
	{dockappointment.ErrInvalidRefs, "invalid-refs"},
	{dockappointment.ErrInvalidReason, "invalid-reason"},
	{dockappointment.ErrInvalidEta, "invalid-eta"},
}

// slugFor returns the slug of err; ok is false for an untyped error.
func slugFor(err error) (slug string, ok bool) {
	for _, entry := range errorCatalogue {
		if errors.Is(err, entry.target) {
			return entry.slug, true
		}
	}
	return "", false
}

// toolError builds a tool-level error "<slug>: <detail>". Returned from a
// handler it becomes an isError tool result, never a transport failure.
func toolError(slug, detail string) error {
	return fmt.Errorf("%s: %s", slug, detail)
}

// classify is the usecases.Failure of the idempotent writes: every typed
// (client-side) refusal is stored as the answer of the key, like REST stores
// its 4xx; anything else is never stored.
func classify(err error) (slug, detail string, ok bool) {
	if slug, ok := slugFor(err); ok {
		return slug, err.Error(), true
	}
	return "", "", false
}

// mapError turns a use-case/repository error into the tool error the model
// sees. Typed errors are prefixed with their REST slug and keep their message;
// anything else is logged and reported generically so infrastructure details
// (DSNs, SQL) never reach the model.
func mapError(err error) error {
	var replayed *usecases.ReplayedFailure
	if errors.As(err, &replayed) {
		return toolError(replayed.Slug, replayed.Detail)
	}
	if slug, ok := slugFor(err); ok {
		return toolError(slug, err.Error())
	}
	slog.Error("mcp tool failed with an unexpected error", "error", err)
	return toolError("internal-error", "an unexpected internal error occurred")
}
