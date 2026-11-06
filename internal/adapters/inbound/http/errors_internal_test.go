package http

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/claudioed/dock-scheduling/internal/application/ports"
	"github.com/claudioed/dock-scheduling/internal/application/usecases"
	"github.com/claudioed/dock-scheduling/internal/domain/dockappointment"
	"github.com/claudioed/dock-scheduling/internal/domain/dockcalendar"
	"github.com/claudioed/dock-scheduling/internal/domain/shared"
)

// D-10: 409 for conflicts and illegal states, 422 for value-object and
// reference rules, slug = kebab-case of the Err name without the Err prefix.
func TestEveryDomainErrorFollowsDecisionD10(t *testing.T) {
	conflicts := map[string]error{
		"ErrDoorDoubleBooked": dockappointment.ErrDoorDoubleBooked, "ErrDoorUnavailable": dockcalendar.ErrDoorUnavailable,
		"ErrAsnAlreadyScheduled": dockappointment.ErrAsnAlreadyScheduled, "ErrClosureConflictsWithAppointments": dockcalendar.ErrClosureConflictsWithAppointments,
		"ErrHoursConflictWithAppointments": dockcalendar.ErrHoursConflictWithAppointments, "ErrOverlappingClosure": dockcalendar.ErrOverlappingClosure,
		"ErrIllegalTransition": dockappointment.ErrIllegalTransition, "ErrConcurrentUpdate": dockappointment.ErrConcurrentUpdate,
		"ErrClosureAlreadyLifted": dockcalendar.ErrClosureAlreadyLifted, "ErrNoShowTooEarly": dockappointment.ErrNoShowTooEarly,
	}
	unprocessable := map[string]error{
		"ErrInvalidDoorCode": shared.ErrInvalidDoorCode, "ErrInvalidSiteID": shared.ErrInvalidSiteID, "ErrInvalidCarrier": shared.ErrInvalidCarrier,
		"ErrInvalidDirection": shared.ErrInvalidDirection, "ErrInvalidWindow": dockappointment.ErrInvalidWindow, "ErrWindowInPast": dockappointment.ErrWindowInPast,
		"ErrUnknownDoor": dockappointment.ErrUnknownDoor, "ErrDoorFlowMismatch": dockappointment.ErrDoorFlowMismatch, "ErrUnknownAsn": dockappointment.ErrUnknownAsn,
		"ErrMissingOutboundRef": dockappointment.ErrMissingOutboundRef, "ErrDuplicateAsn": dockappointment.ErrDuplicateAsn, "ErrInvalidRefs": dockappointment.ErrInvalidRefs,
		"ErrInvalidReason": dockappointment.ErrInvalidReason, "ErrInvalidEta": dockappointment.ErrInvalidEta,
		"ErrInvalidOperatingHours": dockcalendar.ErrInvalidOperatingHours, "ErrInvalidSettings": dockcalendar.ErrInvalidSettings, "ErrInvalidClosure": dockcalendar.ErrInvalidClosure,
	}
	check := func(table map[string]error, status int) {
		for name, err := range table {
			p := problemFor(fmt.Errorf("wrapped: %w", err))
			if p.status != status || p.slug != kebab(name) {
				t.Errorf("%s -> %d %s, want %d %s", name, p.status, p.slug, status, kebab(name))
			}
		}
	}
	check(conflicts, http.StatusConflict)
	check(unprocessable, http.StatusUnprocessableEntity)
}

func TestReferenceAndPreconditionErrors(t *testing.T) {
	for err, want := range map[error]problem{
		ports.ErrNotFound:                       {404, "appointment-not-found", ""},
		usecases.ErrDoorNotFound:                {404, "door-not-found", ""},
		dockcalendar.ErrUnknownClosure:          {404, "unknown-closure", ""},
		usecases.ErrPreconditionFailed:          {412, "precondition-failed", ""},
		errors.New("something unmapped"):        {500, "internal-error", ""},
		dockappointment.ErrInvalidState:         {500, "internal-error", ""}, // a corrupt row is ours, not the caller's
		dockappointment.ErrInvalidAppointmentID: {500, "internal-error", ""},
	} {
		if got := problemFor(err); got.status != want.status || got.slug != want.slug {
			t.Errorf("%v -> %d %s, want %d %s", err, got.status, got.slug, want.status, want.slug)
		}
	}
}

// kebab turns ErrDoorDoubleBooked into door-double-booked.
func kebab(errName string) string {
	name := strings.TrimPrefix(errName, "Err")
	upper := func(b byte) bool { return b >= 'A' && b <= 'Z' }
	var b strings.Builder
	for i := 0; i < len(name); i++ {
		if i > 0 && upper(name[i]) && !upper(name[i-1]) {
			b.WriteByte('-')
		}
		b.WriteByte(name[i])
	}
	return strings.ToLower(b.String())
}

func TestNULScanner(t *testing.T) {
	for body, want := range map[string]bool{
		"{}": false, "a\x00b": true, `"\u0000"`: true, `"\\u0000"`: false, `"\\\u0000"`: true, `"\\\\u0000"`: false, `"\u0000 \\u0000"`: true, `"\\u0000 \u0000"`: true,
	} {
		if got := containsNUL([]byte(body)); got != want {
			t.Errorf("containsNUL(%q) = %v, want %v", body, got, want)
		}
	}
}
