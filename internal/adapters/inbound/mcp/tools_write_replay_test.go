package mcp_test

// The replay classes on the agent write surface (ADR 0014), end to end over
// the tools: a RETRYABLE refusal is not stored, so the same key and arguments
// run again once the state changed; a FINAL refusal is stored and replayed.

import (
	"strings"
	"testing"
)

// carrier-suspended is RETRYABLE here (a plain pre-check, ADR 0014 J1): the
// refused booking is answered but not stored, so after the reinstatement the
// SAME key and arguments execute and book.
func TestARetryableRefusalIsNotStoredOnTheToolSurface(t *testing.T) {
	h := newHarness(t)
	var reg, sus, rein carrierWrite
	h.ok("register_carrier", registerArgs("reg", nil), &reg)
	h.ok("suspend_carrier", map[string]any{"scac": "ACMF", "reason": "insurance lapsed", "idempotencyKey": "sus"}, &sus)

	if msg := h.fail("book_appointment", bookArgs("book-1", nil)); !strings.HasPrefix(msg, "carrier-suspended:") {
		t.Fatalf("a booking naming the suspended SCAC: %q", msg)
	}
	// The refusal was not stored: the same call again is NOT the stored
	// refusal replayed, and once the carrier is reinstated it books.
	h.ok("reinstate_carrier", map[string]any{"scac": "ACMF", "expectedVersion": 2, "idempotencyKey": "rein"}, &rein)
	var booked writeResult
	h.ok("book_appointment", bookArgs("book-1", nil), &booked)
	if booked.Replayed || booked.Appointment.State != "BOOKED" {
		t.Fatalf("the retry under the released key = %+v, want a fresh BOOKED", booked)
	}
	// The 2xx is stored: one more replay.
	var again writeResult
	h.ok("book_appointment", bookArgs("book-1", nil), &again)
	if !again.Replayed || again.Appointment.AppointmentID != booked.Appointment.AppointmentID {
		t.Fatalf("the stored success must replay: %+v", again)
	}
}

// A FINAL refusal is stored under the key and replayed to the same call.
func TestAFinalRefusalIsStoredAndReplayedOnTheToolSurface(t *testing.T) {
	h := newHarness(t)
	args := bookArgs("fin-1", map[string]any{"windowStart": "2030-01-07T09:30:00Z"})
	first := h.fail("book_appointment", args)
	if !strings.Contains(first, "invalid-window") {
		t.Fatalf("a window off the slot granularity: %q", first)
	}
	second := h.fail("book_appointment", args)
	if first != second {
		t.Fatalf("the FINAL refusal must replay byte-identically:\n%s\n%s", first, second)
	}
}
