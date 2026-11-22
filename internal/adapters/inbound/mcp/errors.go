package mcp

import (
	"errors"
	"fmt"
	"log/slog"

	"github.com/claudioed/dock-scheduling/internal/application/ports"
	"github.com/claudioed/dock-scheduling/internal/application/usecases"
	"github.com/claudioed/dock-scheduling/internal/domain/carrier"
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
	// replay is the replay class of the problem type (ADR 0014), the same
	// as the REST catalogue's for the same slug: a FINAL refusal is stored
	// under the key and replayed, a RETRYABLE one is answered but never
	// stored (the key is released and the same call runs again).
	replay replayClass
}{
	{errInvalidArgument, "invalid-argument", replayFinal},
	{usecases.ErrIdempotencyKeyRequired, "idempotency-key-required", replayFinal},
	{usecases.ErrIdempotencyKeyInvalid, "idempotency-key-invalid", replayFinal},
	{usecases.ErrIdempotencyKeyReused, "idempotency-key-reused", replayFinal},
	{usecases.ErrInvalidInitiator, "invalid-initiator", replayFinal},
	{usecases.ErrInvalidListQuery, "invalid-cursor", replayFinal},
	{usecases.ErrInvalidAvailabilityQuery, "invalid-availability-query", replayFinal},

	{ports.ErrNotFound, "appointment-not-found", replayFinal},
	{usecases.ErrDoorNotFound, "door-not-found", replayRetryable},
	{carrier.ErrCarrierNotFound, "carrier-not-found", replayRetryable},
	{usecases.ErrPreconditionFailed, "precondition-failed", replayFinal},

	// 409 on REST: conflicts and illegal states.
	{dockappointment.ErrDoorDoubleBooked, "door-double-booked", replayRetryable},
	{dockcalendar.ErrDoorUnavailable, "door-unavailable", replayRetryable},
	{dockappointment.ErrAsnAlreadyScheduled, "asn-already-scheduled", replayRetryable},
	{dockappointment.ErrIllegalTransition, "illegal-transition", replayFinal},
	{dockappointment.ErrConcurrentUpdate, "concurrent-update", replayRetryable},
	{carrier.ErrCarrierSuspended, "carrier-suspended", replayRetryable},
	{carrier.ErrCarrierAlreadyRegistered, "carrier-already-registered", replayRetryable},
	{carrier.ErrCarrierAlreadySuspended, "carrier-already-suspended", replayRetryable},
	{carrier.ErrCarrierNotSuspended, "carrier-not-suspended", replayRetryable},

	// 422 on REST: value-object and reference rules.
	{shared.ErrInvalidDoorCode, "invalid-door-code", replayFinal},
	{shared.ErrInvalidSiteID, "invalid-site-id", replayFinal},
	{shared.ErrInvalidCarrier, "invalid-carrier", replayFinal},
	{shared.ErrInvalidDirection, "invalid-direction", replayFinal},
	{dockappointment.ErrInvalidAppointmentID, "invalid-appointment-id", replayFinal},
	{dockappointment.ErrInvalidWindow, "invalid-window", replayFinal},
	{dockappointment.ErrWindowInPast, "window-in-past", replayRetryable},
	{dockappointment.ErrUnknownDoor, "unknown-door", replayRetryable},
	{dockappointment.ErrDoorFlowMismatch, "door-flow-mismatch", replayRetryable},
	{dockappointment.ErrUnknownAsn, "unknown-asn", replayRetryable},
	{dockappointment.ErrMissingOutboundRef, "missing-outbound-ref", replayFinal},
	{dockappointment.ErrDuplicateAsn, "duplicate-asn", replayFinal},
	{dockappointment.ErrInvalidRefs, "invalid-refs", replayFinal},
	{dockappointment.ErrInvalidReason, "invalid-reason", replayFinal},
	{dockappointment.ErrInvalidEta, "invalid-eta", replayFinal},
	{carrier.ErrInvalidScac, "invalid-scac", replayFinal},
	{carrier.ErrInvalidUSDOT, "invalid-usdot", replayFinal},
	{carrier.ErrInvalidMC, "invalid-mc", replayFinal},
	{carrier.ErrInvalidCarrierName, "invalid-carrier-name", replayFinal},
	{carrier.ErrInvalidSuspensionReason, "invalid-suspension-reason", replayFinal},
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

// classify is the usecases.Failure of the idempotent writes (ADR 0014): a
// typed refusal whose replay class is FINAL is stored as the answer of the
// key and replayed, like REST stores its FINAL 4xx; a RETRYABLE refusal is
// answered but never stored — ok=false releases the key, exactly as the REST
// middleware releases it — and anything untyped is never stored either.
func classify(err error) (slug, detail string, ok bool) {
	for _, entry := range errorCatalogue {
		if errors.Is(err, entry.target) {
			return entry.slug, err.Error(), entry.replay == replayFinal
		}
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
	slog.Error("mcp tool failed with an unexpected error", "error", logReplacer.Replace(err.Error()))
	return toolError("internal-error", "an unexpected internal error occurred")
}
