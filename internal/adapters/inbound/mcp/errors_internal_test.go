package mcp

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/claudioed/dock-scheduling/internal/application/ports"
	"github.com/claudioed/dock-scheduling/internal/application/usecases"
	"github.com/claudioed/dock-scheduling/internal/domain/carrier"
	"github.com/claudioed/dock-scheduling/internal/domain/dockappointment"
	"github.com/claudioed/dock-scheduling/internal/domain/dockcalendar"
	"github.com/claudioed/dock-scheduling/internal/domain/shared"
)

// kebab turns ErrDoorDoubleBooked into door-double-booked (decision D-10: the
// slug is the kebab-case of the Err name without the Err prefix).
var upperRun = regexp.MustCompile(`([a-z0-9])([A-Z])`)

func kebab(errName string) string {
	return strings.ToLower(upperRun.ReplaceAllString(strings.TrimPrefix(errName, "Err"), "${1}-${2}"))
}

// TestSlugsFollowTheRestRule pins the slug of every domain error to the D-10
// rule the REST adapter applies, so both surfaces speak one vocabulary. The
// exceptions are the ones the REST adapter reaches through a different route
// (a malformed id is a 400 invalid-path-parameter there, a tool argument here;
// the list/page and ETag errors have their own REST names).
func TestSlugsFollowTheRestRule(t *testing.T) {
	byRule := map[string]error{
		"ErrDoorDoubleBooked":         dockappointment.ErrDoorDoubleBooked,
		"ErrDoorUnavailable":          dockcalendar.ErrDoorUnavailable,
		"ErrAsnAlreadyScheduled":      dockappointment.ErrAsnAlreadyScheduled,
		"ErrIllegalTransition":        dockappointment.ErrIllegalTransition,
		"ErrConcurrentUpdate":         dockappointment.ErrConcurrentUpdate,
		"ErrInvalidDoorCode":          shared.ErrInvalidDoorCode,
		"ErrInvalidSiteID":            shared.ErrInvalidSiteID,
		"ErrInvalidCarrier":           shared.ErrInvalidCarrier,
		"ErrInvalidDirection":         shared.ErrInvalidDirection,
		"ErrInvalidWindow":            dockappointment.ErrInvalidWindow,
		"ErrWindowInPast":             dockappointment.ErrWindowInPast,
		"ErrUnknownDoor":              dockappointment.ErrUnknownDoor,
		"ErrDoorFlowMismatch":         dockappointment.ErrDoorFlowMismatch,
		"ErrUnknownAsn":               dockappointment.ErrUnknownAsn,
		"ErrMissingOutboundRef":       dockappointment.ErrMissingOutboundRef,
		"ErrDuplicateAsn":             dockappointment.ErrDuplicateAsn,
		"ErrInvalidRefs":              dockappointment.ErrInvalidRefs,
		"ErrInvalidReason":            dockappointment.ErrInvalidReason,
		"ErrInvalidEta":               dockappointment.ErrInvalidEta,
		"ErrDoorNotFound":             usecases.ErrDoorNotFound,
		"ErrPreconditionFailed":       usecases.ErrPreconditionFailed,
		"ErrInvalidAvailabilityQuery": usecases.ErrInvalidAvailabilityQuery,
		"ErrIdempotencyKeyRequired":   usecases.ErrIdempotencyKeyRequired,
		"ErrIdempotencyKeyInvalid":    usecases.ErrIdempotencyKeyInvalid,
		"ErrIdempotencyKeyReused":     usecases.ErrIdempotencyKeyReused,
		"ErrInvalidInitiator":         usecases.ErrInvalidInitiator,
		"ErrInvalidAppointmentID":     dockappointment.ErrInvalidAppointmentID,
		"ErrInvalidArgument":          errInvalidArgument,
		"ErrNotFound":                 ports.ErrNotFound, // REST: appointment-not-found
		"ErrInvalidListQuery":         usecases.ErrInvalidListQuery,
		// The carrier registry (ADR 0012): the slug is the REST rule too.
		"ErrCarrierSuspended":         carrier.ErrCarrierSuspended,
		"ErrCarrierAlreadyRegistered": carrier.ErrCarrierAlreadyRegistered,
		"ErrCarrierAlreadySuspended":  carrier.ErrCarrierAlreadySuspended,
		"ErrCarrierNotSuspended":      carrier.ErrCarrierNotSuspended,
		"ErrInvalidScac":              carrier.ErrInvalidScac,
		"ErrInvalidUSDOT":             carrier.ErrInvalidUSDOT,
		"ErrInvalidMC":                carrier.ErrInvalidMC,
		"ErrInvalidCarrierName":       carrier.ErrInvalidCarrierName,
		"ErrInvalidSuspensionReason":  carrier.ErrInvalidSuspensionReason,
		"ErrCarrierNotFound":          carrier.ErrCarrierNotFound,
	}
	special := map[string]string{"ErrNotFound": "appointment-not-found", "ErrInvalidListQuery": "invalid-cursor"}
	for name, err := range byRule {
		want := kebab(name)
		if s, ok := special[name]; ok {
			want = s
		}
		got, ok := slugFor(err)
		if !ok || got != want {
			t.Errorf("%s: slug = %q (found %v), want %q", name, got, ok, want)
		}
	}
	// Every catalogue entry is covered above (no slug without a pinned rule).
	if len(byRule) != len(errorCatalogue) {
		t.Errorf("the table pins %d errors, the catalogue holds %d", len(byRule), len(errorCatalogue))
	}
}

func TestSlugsAreUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, e := range errorCatalogue {
		if seen[e.slug] {
			t.Errorf("slug %q appears twice", e.slug)
		}
		seen[e.slug] = true
	}
}

func TestMapErrorNeverEchoesInfrastructureDetail(t *testing.T) {
	err := mapError(errors.New(`pq: password authentication failed for user "dock" (postgres://dock:secret@db:5432/dock)`))
	if got := err.Error(); got != "internal-error: an unexpected internal error occurred" {
		t.Fatalf("message = %q", got)
	}
	typed := mapError(fmt.Errorf("%w: on appt-1", dockappointment.ErrAsnAlreadyScheduled))
	if !strings.HasPrefix(typed.Error(), "asn-already-scheduled: ") || !strings.Contains(typed.Error(), "appt-1") {
		t.Fatalf("typed message = %q", typed)
	}
	replayed := mapError(&usecases.ReplayedFailure{Slug: "door-unavailable", Detail: "closed"})
	if replayed.Error() != "door-unavailable: closed" {
		t.Fatalf("replayed message = %q", replayed)
	}
}

func TestClassifyStoresOnlyTypedErrors(t *testing.T) {
	if slug, detail, ok := classify(dockappointment.ErrWindowInPast); !ok || slug != "window-in-past" || detail != dockappointment.ErrWindowInPast.Error() {
		t.Fatalf("typed: %q %q %v", slug, detail, ok)
	}
	if _, _, ok := classify(errors.New("connection reset")); ok {
		t.Fatal("an untyped (server-side) error must not be stored as the answer of a key")
	}
}

func TestResourceSegmentRejectsMalformedURIs(t *testing.T) {
	const tmpl = "dock://sites/{siteId}/calendar"
	if got, err := resourceSegment("dock://sites/WH1/calendar", "dock://sites/", "/calendar", tmpl); err != nil || got != "WH1" {
		t.Fatalf("valid: %q, %v", got, err)
	}
	for _, uri := range []string{
		"dock://sites//calendar", "dock://sites/WH1", "dock://sites/WH1/at-risk", "dock://sites/a/b/calendar",
		"other://sites/WH1/calendar", "dock://sites/WH1/calendar/x", "",
	} {
		if got, err := resourceSegment(uri, "dock://sites/", "/calendar", tmpl); err == nil {
			t.Errorf("%q accepted as %q", uri, got)
		}
	}
	if got, err := resourceSegment("dock://appointments/appt-1", "dock://appointments/", "", "dock://appointments/{appointmentId}"); err != nil || got != "appt-1" {
		t.Fatalf("no-suffix: %q, %v", got, err)
	}
	if _, err := resourceSegment("dock://appointments/", "dock://appointments/", "", "t"); err == nil {
		t.Error("an empty segment is rejected")
	}
}
