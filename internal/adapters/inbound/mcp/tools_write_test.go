package mcp_test

import (
	"strings"
	"testing"
	"time"

	"github.com/claudioed/dock-scheduling/internal/application/usecases"
)

func TestBookAppointmentWritesWithAttribution(t *testing.T) {
	h := newHarness(t)
	var r writeResult
	h.ok("book_appointment", bookArgs("book-1", map[string]any{"proposalId": "prop-7", "approvedBy": "planner-1", "agentId": "ops-agent"}), &r)

	if r.Appointment.AppointmentID != apptN(1) || r.Appointment.State != "BOOKED" || r.Appointment.DoorCode != "D-IN" || r.Appointment.SiteID != "WH1" {
		t.Fatalf("book_appointment = %+v", r.Appointment)
	}
	if r.Replayed {
		t.Error("a first call must not be a replay")
	}
	got := r.InitiatedBy
	if got.Via != "mcp" || got.ProposalID != "prop-7" || got.ApprovedBy != "planner-1" || got.AgentID != "ops-agent" {
		t.Errorf("initiatedBy echo = %+v", got)
	}
	assertInitiator(t, h, "prop-7", "planner-1", "ops-agent")
	if names := h.eventNames(); len(names) != 1 || names[0] != "DockAppointmentBooked" {
		t.Errorf("events = %v", names)
	}
}

// assertInitiator checks that the one write ran under an mcp initiator with these
// labels: what the outbox publisher reads back from the use case context.
func assertInitiator(t *testing.T, h *harness, proposal, approver, agent string) {
	t.Helper()
	if len(h.svc.initiators) != 1 {
		t.Fatalf("initiators = %v", h.svc.initiators)
	}
	in := h.svc.initiators[0]
	if in.Via() != usecases.ViaMCP || in.ProposalID() != proposal || in.ApprovedBy() != approver || in.AgentID() != agent {
		t.Errorf("initiator in the use case context = %+v", in)
	}
}

func TestWritesWithoutLabelsStillRecordTheSurface(t *testing.T) {
	h := newHarness(t)
	var r writeResult
	h.ok("book_appointment", bookArgs("book-1", nil), &r)
	if r.InitiatedBy.Via != "mcp" || r.InitiatedBy.AgentID != "" {
		t.Fatalf("initiatedBy = %+v, want via mcp and no labels", r.InitiatedBy)
	}
	if in := h.svc.initiators[0]; in.IsZero() || in.Via() != usecases.ViaMCP {
		t.Fatalf("initiator = %+v, the adapter must always stamp via", in)
	}
}

func TestBookAppointmentReplaysTheSameKey(t *testing.T) {
	h := newHarness(t)
	var first, second writeResult
	h.ok("book_appointment", bookArgs("k-1", map[string]any{"agentId": "a1"}), &first)
	// Same key, same arguments (other labels do not matter): the stored answer.
	h.ok("book_appointment", bookArgs("k-1", map[string]any{"agentId": "a2"}), &second)

	if !second.Replayed || first.Replayed {
		t.Fatalf("replayed first=%v second=%v", first.Replayed, second.Replayed)
	}
	if second.Appointment.AppointmentID != first.Appointment.AppointmentID || second.InitiatedBy.AgentID != "a1" {
		t.Fatalf("replay = %+v, want the first answer with its original attribution", second)
	}
	if len(h.svc.initiators) != 1 || len(h.eventNames()) != 1 {
		t.Fatalf("the replay wrote again: initiators=%d events=%v", len(h.svc.initiators), h.eventNames())
	}
	var page pageOut
	h.ok("list_appointments", map[string]any{}, &page)
	if len(page.Items) != 1 {
		t.Fatalf("appointments = %d, want 1", len(page.Items))
	}
}

func TestIdempotencyKeyWithOtherArgumentsIsRefused(t *testing.T) {
	h := newHarness(t)
	h.ok("book_appointment", bookArgs("k-1", nil), &writeResult{})
	msg := h.fail("book_appointment", bookArgs("k-1", map[string]any{"windowStart": at10.Format(time.RFC3339), "windowEnd": at11.Format(time.RFC3339)}))
	if !strings.HasPrefix(msg, "idempotency-key-reused: ") {
		t.Fatalf("message = %q", msg)
	}
	// Another tool with the same key is another request too.
	msg = h.fail("cancel_appointment", map[string]any{"appointmentId": apptN(1), "reason": "SITE_REQUEST", "idempotencyKey": "k-1"})
	if !strings.HasPrefix(msg, "idempotency-key-reused: ") {
		t.Fatalf("message = %q", msg)
	}
	var page pageOut
	h.ok("list_appointments", map[string]any{"state": "CANCELLED"}, &page)
	if len(page.Items) != 0 {
		t.Fatalf("the refused call cancelled something: %+v", page)
	}
}

func TestClientErrorsAreStoredAndReplayedLikeRest(t *testing.T) {
	h := newHarness(t)
	occupied := h.seedBooking(usecases.BookCommand{DoorCode: "D-IN", Direction: "INBOUND", Carrier: "ACME Freight", WindowStart: at8, WindowEnd: at9, ASNNumbers: []string{"ASN-2"}})

	msg := h.fail("book_appointment", bookArgs("k-conflict", nil))
	if !strings.HasPrefix(msg, "door-double-booked: ") {
		t.Fatalf("first message = %q", msg)
	}
	// The door frees up, but the key already has its (4xx) answer: replayed verbatim.
	h.ok("cancel_appointment", map[string]any{"appointmentId": occupied, "reason": "SITE_REQUEST", "idempotencyKey": "k-cancel"}, &writeResult{})
	if again := h.fail("book_appointment", bookArgs("k-conflict", nil)); again != msg {
		t.Fatalf("replayed message = %q, want %q", again, msg)
	}
	// A new key now books.
	h.ok("book_appointment", bookArgs("k-retry", nil), &writeResult{})
}

// A typed refusal is stored under its OWN key only: another key still works.
// (That an untyped failure is never stored is proved in usecases/idempotent_test.go.)
func TestARefusedKeyDoesNotPoisonAnotherKey(t *testing.T) {
	h := newHarness(t)
	msg := h.fail("cancel_appointment", map[string]any{"appointmentId": apptN(5), "reason": "SITE_REQUEST", "idempotencyKey": "k-a"})
	if !strings.HasPrefix(msg, "appointment-not-found: ") {
		t.Fatalf("message = %q", msg)
	}
	h.seedBooking(usecases.BookCommand{DoorCode: "D-IN", Direction: "INBOUND", Carrier: "ACME Freight", WindowStart: at8, WindowEnd: at9, ASNNumbers: []string{"ASN-1"}})
	h.ok("cancel_appointment", map[string]any{"appointmentId": apptN(1), "reason": "SITE_REQUEST", "idempotencyKey": "k-b"}, &writeResult{})
}

func TestWriteToolsRefuseMissingOrBadKeys(t *testing.T) {
	h := newHarness(t)
	noKey := bookArgs("", nil)
	delete(noKey, "idempotencyKey")
	if msg := h.fail("book_appointment", noKey); !strings.Contains(msg, `["idempotencyKey"]`) {
		t.Fatalf("missing key message = %q", msg)
	}
	for name, tc := range map[string]struct{ key, want string }{
		"blank":     {"", "idempotency-key-required"},
		"non-ascii": {"clé", "idempotency-key-invalid"},
		"control":   {"a\tb", "idempotency-key-invalid"},
		"too long":  {strings.Repeat("k", 129), "idempotency-key-invalid"},
	} {
		t.Run(name, func(t *testing.T) {
			if msg := h.fail("book_appointment", bookArgs(tc.key, nil)); !strings.HasPrefix(msg, tc.want+": ") {
				t.Fatalf("message = %q, want prefix %s", msg, tc.want)
			}
		})
	}
	var page pageOut
	h.ok("list_appointments", map[string]any{}, &page)
	if len(page.Items) != 0 {
		t.Fatalf("a refused write booked something: %+v", page)
	}
}

func TestBookAppointmentRefusals(t *testing.T) {
	h := newHarness(t)
	for name, tc := range map[string]struct {
		extra map[string]any
		want  string
	}{
		"unknown door":      {map[string]any{"doorCode": "NOPE"}, "unknown-door"},
		"flow mismatch":     {map[string]any{"doorCode": "D-OUT"}, "door-flow-mismatch"},
		"unknown asn":       {map[string]any{"asnNumbers": []string{"ASN-404"}}, "unknown-asn"},
		"no asn":            {map[string]any{"asnNumbers": []string{}}, "unknown-asn"},
		"bad window":        {map[string]any{"windowEnd": at8.Format(time.RFC3339)}, "invalid-window"},
		"off granularity":   {map[string]any{"windowStart": at8.Add(7 * time.Minute).Format(time.RFC3339)}, "invalid-window"},
		"in the past":       {map[string]any{"windowStart": t0.Add(-2 * time.Hour).Format(time.RFC3339), "windowEnd": t0.Add(-time.Hour).Format(time.RFC3339)}, "window-in-past"},
		"bad direction":     {map[string]any{"direction": "SIDEWAYS"}, "invalid-direction"},
		"bad carrier":       {map[string]any{"carrier": " "}, "invalid-carrier"},
		"malformed start":   {map[string]any{"windowStart": "8am"}, "invalid-argument"},
		"initiator too big": {map[string]any{"agentId": strings.Repeat("a", 129)}, "invalid-initiator"},
	} {
		t.Run(name, func(t *testing.T) {
			msg := h.fail("book_appointment", bookArgs("k-"+name, tc.extra))
			if !strings.HasPrefix(msg, tc.want+": ") {
				t.Fatalf("message = %q, want prefix %s", msg, tc.want)
			}
		})
	}
	if len(h.eventNames()) != 0 {
		t.Fatalf("refusals published events: %v", h.eventNames())
	}
}

func TestOutboundBookingAndAsnUniqueness(t *testing.T) {
	h := newHarness(t)
	var out writeResult
	h.ok("book_appointment", map[string]any{
		"direction": "OUTBOUND", "doorCode": "D-OUT", "carrier": "ACME Freight", "windowStart": at8.Format(time.RFC3339), "windowEnd": at9.Format(time.RFC3339),
		"cptId": "WH1-1500", "expectedSealNumber": "SEAL-9", "idempotencyKey": "k-out",
	}, &out)
	if out.Appointment.Direction != "OUTBOUND" || out.Appointment.CPTID == nil || *out.Appointment.CPTID != "WH1-1500" {
		t.Fatalf("outbound = %+v", out.Appointment)
	}
	h.ok("book_appointment", bookArgs("k-in", nil), &writeResult{})
	msg := h.fail("book_appointment", bookArgs("k-in-2", map[string]any{"doorCode": "D-BOTH", "windowStart": at10.Format(time.RFC3339), "windowEnd": at11.Format(time.RFC3339)}))
	if !strings.HasPrefix(msg, "asn-already-scheduled: ") {
		t.Fatalf("message = %q", msg)
	}
}

func TestRescheduleAppointment(t *testing.T) {
	h := newHarness(t)
	var booked writeResult
	h.ok("book_appointment", bookArgs("k-1", nil), &booked)

	// A stale expectedVersion is refused and changes nothing.
	msg := h.fail("reschedule_appointment", map[string]any{
		"appointmentId": booked.Appointment.AppointmentID, "windowStart": at10.Format(time.RFC3339), "windowEnd": at11.Format(time.RFC3339),
		"expectedVersion": 7, "idempotencyKey": "k-stale",
	})
	if !strings.HasPrefix(msg, "precondition-failed: ") {
		t.Fatalf("stale message = %q", msg)
	}

	var moved writeResult
	h.ok("reschedule_appointment", map[string]any{
		"appointmentId": booked.Appointment.AppointmentID, "doorCode": "D-BOTH", "windowStart": at10.Format(time.RFC3339), "windowEnd": at11.Format(time.RFC3339),
		"expectedVersion": booked.Appointment.Version, "idempotencyKey": "k-move", "agentId": "ops-agent",
	}, &moved)
	a := moved.Appointment
	if a.DoorCode != "D-BOTH" || !a.WindowStart.Equal(at10) || a.Version != booked.Appointment.Version+1 || a.State != "BOOKED" {
		t.Fatalf("moved = %+v", a)
	}
	if moved.InitiatedBy.Via != "mcp" || moved.InitiatedBy.AgentID != "ops-agent" {
		t.Errorf("initiatedBy = %+v", moved.InitiatedBy)
	}
	if names := h.eventNames(); len(names) != 2 || names[1] != "DockAppointmentRescheduled" {
		t.Errorf("events = %v", names)
	}

	// The same checks as a booking apply to the new window.
	h.seedBooking(usecases.BookCommand{DoorCode: "D-BOTH", Direction: "OUTBOUND", Carrier: "ACME Freight", WindowStart: at11, WindowEnd: at12, CPTID: "WH1-1500"})
	clash := h.fail("reschedule_appointment", map[string]any{
		"appointmentId": booked.Appointment.AppointmentID, "windowStart": at11.Format(time.RFC3339), "windowEnd": at12.Format(time.RFC3339), "idempotencyKey": "k-clash",
	})
	if !strings.HasPrefix(clash, "door-double-booked: ") {
		t.Fatalf("clash message = %q", clash)
	}
	if neg := h.fail("reschedule_appointment", map[string]any{
		"appointmentId": booked.Appointment.AppointmentID, "windowStart": at8.Format(time.RFC3339), "windowEnd": at9.Format(time.RFC3339), "expectedVersion": -1, "idempotencyKey": "k-neg",
	}); !strings.HasPrefix(neg, "invalid-argument: ") {
		t.Fatalf("negative version message = %q", neg)
	}
}

func TestCancelAppointment(t *testing.T) {
	h := newHarness(t)
	var booked writeResult
	h.ok("book_appointment", bookArgs("k-1", nil), &booked)
	id := booked.Appointment.AppointmentID

	for _, reason := range []string{"TURNED_AWAY", "ASN_CANCELLED", "whatever", ""} {
		msg := h.fail("cancel_appointment", map[string]any{"appointmentId": id, "reason": reason, "idempotencyKey": "k-r-" + reason})
		if !strings.HasPrefix(msg, "invalid-reason: ") {
			t.Fatalf("reason %q: message = %q", reason, msg)
		}
	}
	var cancelled writeResult
	h.ok("cancel_appointment", map[string]any{"appointmentId": id, "reason": "CARRIER_REQUEST", "expectedVersion": 1, "idempotencyKey": "k-c", "agentId": "ops-agent", "approvedBy": "p1"}, &cancelled)
	a := cancelled.Appointment
	if a.State != "CANCELLED" || a.CancelReason == nil || *a.CancelReason != "CARRIER_REQUEST" {
		t.Fatalf("cancelled = %+v", a)
	}
	again := h.fail("cancel_appointment", map[string]any{"appointmentId": id, "reason": "SITE_REQUEST", "idempotencyKey": "k-c2"})
	if !strings.HasPrefix(again, "illegal-transition: ") {
		t.Fatalf("second cancel message = %q", again)
	}
	// The door window is free again.
	h.ok("book_appointment", bookArgs("k-2", nil), &writeResult{})
}

func TestReportEta(t *testing.T) {
	h := newHarness(t)
	var booked writeResult
	h.ok("book_appointment", bookArgs("k-1", nil), &booked)
	id := booked.Appointment.AppointmentID

	var onTime writeResult
	h.ok("report_eta", map[string]any{"appointmentId": id, "etaAt": at8.Add(5 * time.Minute).Format(time.RFC3339), "source": "carrier-app", "idempotencyKey": "e-1"}, &onTime)
	if onTime.AtRisk || onTime.Appointment.AtRisk || onTime.Appointment.Eta == nil || onTime.Appointment.Eta.Source != "carrier-app" {
		t.Fatalf("on-time eta = %+v", onTime)
	}
	var late writeResult
	h.ok("report_eta", map[string]any{"appointmentId": id, "etaAt": at10.Format(time.RFC3339), "idempotencyKey": "e-2", "agentId": "ops-agent"}, &late)
	if !late.AtRisk || !late.Appointment.AtRisk || late.Appointment.Eta == nil || late.Appointment.Eta.Source != "mcp" {
		t.Fatalf("late eta = %+v", late)
	}
	if msg := h.fail("report_eta", map[string]any{"appointmentId": id, "etaAt": "soon", "idempotencyKey": "e-3"}); !strings.HasPrefix(msg, "invalid-argument: ") {
		t.Fatalf("bad eta message = %q", msg)
	}
	if msg := h.fail("report_eta", map[string]any{"appointmentId": id, "etaAt": at10.Format(time.RFC3339), "source": strings.Repeat("s", 65), "idempotencyKey": "e-4"}); !strings.HasPrefix(msg, "invalid-eta: ") {
		t.Fatalf("long source message = %q", msg)
	}
}

func TestOptionalWriteArgumentsAreOptionalInTheSchema(t *testing.T) {
	h := newHarness(t)
	tools, err := h.session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	optional := map[string][]string{
		"book_appointment":       {"carrierScac", "asnNumbers", "cptId", "shipmentRefs", "expectedSealNumber", "proposalId", "approvedBy", "agentId"},
		"reschedule_appointment": {"doorCode", "expectedVersion", "proposalId", "approvedBy", "agentId"},
		"cancel_appointment":     {"expectedVersion", "proposalId", "approvedBy", "agentId"},
		"report_eta":             {"source", "proposalId", "approvedBy", "agentId"},
	}
	for _, tool := range tools.Tools {
		want, isWrite := optional[tool.Name]
		if !isWrite {
			continue
		}
		schema, _ := tool.InputSchema.(map[string]any)
		required := map[string]bool{}
		for _, r := range schema["required"].([]any) {
			required[r.(string)] = true
		}
		if !required["idempotencyKey"] {
			t.Errorf("%s: idempotencyKey must be required", tool.Name)
		}
		for _, name := range want {
			if required[name] {
				t.Errorf("%s: %s is optional but the schema requires it", tool.Name, name)
			}
		}
	}
}
