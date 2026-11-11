package mcp_test

import (
	"fmt"
	"strings"
	"testing"
)

type carrierResult struct {
	SCAC             string  `json:"scac"`
	Name             string  `json:"name"`
	USDOT            *string `json:"usdot"`
	MC               *string `json:"mc"`
	Status           string  `json:"status"`
	SuspensionReason *string `json:"suspensionReason"`
	SuspendedAt      *string `json:"suspendedAt"`
	Version          int64   `json:"version"`
}

type carrierPage struct {
	Items      []carrierResult `json:"items"`
	NextCursor *string         `json:"nextCursor"`
}

type carrierWrite struct {
	Carrier     carrierResult `json:"carrier"`
	Created     bool          `json:"created"`
	Replayed    bool          `json:"replayed"`
	InitiatedBy struct {
		Via        string `json:"via"`
		ProposalID string `json:"proposalId"`
		ApprovedBy string `json:"approvedBy"`
		AgentID    string `json:"agentId"`
	} `json:"initiatedBy"`
}

func registerArgs(key string, extra map[string]any) map[string]any {
	return merge(map[string]any{"scac": "ACMF", "name": "ACME Freight", "usdot": "1234567", "mc": "654321", "idempotencyKey": key}, extra)
}

func TestRegisterCarrierCreatesWithAttribution(t *testing.T) {
	h := newHarness(t)
	var r carrierWrite
	h.ok("register_carrier", registerArgs("reg-1", map[string]any{"proposalId": "prop-3", "approvedBy": "ops-lead", "agentId": "onboarding-agent"}), &r)
	c := r.Carrier
	if !r.Created || c.SCAC != "ACMF" || c.Name != "ACME Freight" || c.USDOT == nil || *c.USDOT != "1234567" || c.MC == nil || *c.MC != "MC654321" {
		t.Fatalf("register_carrier identity = %+v", r)
	}
	if c.Status != "ACTIVE" || c.Version != 1 || c.SuspensionReason != nil || c.SuspendedAt != nil || r.Replayed {
		t.Fatalf("register_carrier state = %+v", r)
	}
	assertRegistrationAttribution(t, h, r)
}

func assertRegistrationAttribution(t *testing.T, h *harness, r carrierWrite) {
	t.Helper()
	if got := r.InitiatedBy; got.Via != "mcp" || got.ProposalID != "prop-3" || got.ApprovedBy != "ops-lead" || got.AgentID != "onboarding-agent" {
		t.Errorf("initiatedBy echo = %+v", got)
	}
	assertInitiator(t, h, "prop-3", "ops-lead", "onboarding-agent")
	if names := h.eventNames(); len(names) != 1 || names[0] != "CarrierRegistered" {
		t.Fatalf("events = %v", names)
	}
}

func TestRegisterCarrierOfTheSameDataUnderAnotherKeyIsANoOp(t *testing.T) {
	h := newHarness(t)
	var first, again carrierWrite
	h.ok("register_carrier", registerArgs("reg-1", nil), &first)
	h.ok("register_carrier", registerArgs("reg-2", nil), &again)
	if again.Created || again.Carrier.Version != 1 || len(h.eventNames()) != 1 {
		t.Fatalf("second registration = %+v, events %v", again, h.eventNames())
	}
}

func TestRegisterCarrierReplaysTheSameKey(t *testing.T) {
	h := newHarness(t)
	var first, replay carrierWrite
	h.ok("register_carrier", registerArgs("reg-1", map[string]any{"agentId": "onboarding-agent"}), &first)
	h.ok("register_carrier", registerArgs("reg-1", map[string]any{"agentId": "someone-else"}), &replay)
	if !replay.Replayed || !replay.Created || replay.InitiatedBy.AgentID != "onboarding-agent" || len(h.eventNames()) != 1 {
		t.Fatalf("replay = %+v", replay)
	}
}

func TestRegisterCarrierRefusals(t *testing.T) {
	h := newHarness(t)
	for name, tc := range map[string]struct {
		args map[string]any
		want string
	}{
		"invalid scac":   {registerArgs("k1", map[string]any{"scac": "acmf"}), "invalid-scac:"},
		"blank scac":     {registerArgs("k2", map[string]any{"scac": ""}), "invalid-argument:"},
		"blank name":     {registerArgs("k3", map[string]any{"name": " "}), "invalid-carrier-name:"},
		"invalid usdot":  {registerArgs("k4", map[string]any{"usdot": "123456789"}), "invalid-usdot:"},
		"invalid mc":     {registerArgs("k5", map[string]any{"mc": "FF1"}), "invalid-mc:"},
		"no idempotency": {merge(registerArgs("", nil), nil), "idempotency-key-required:"},
		"label too long": {registerArgs("k6", map[string]any{"agentId": strings.Repeat("a", 129)}), "invalid-initiator:"},
	} {
		if msg := h.fail("register_carrier", tc.args); !strings.HasPrefix(msg, tc.want) {
			t.Errorf("%s: %q, want prefix %q", name, msg, tc.want)
		}
	}
	var ok carrierWrite
	h.ok("register_carrier", registerArgs("fine", nil), &ok)
	if msg := h.fail("register_carrier", registerArgs("other", map[string]any{"name": "Another Name"})); !strings.HasPrefix(msg, "carrier-already-registered:") || !strings.Contains(msg, "ACMF") {
		t.Errorf("another data on a registered SCAC: %q", msg)
	}
	if len(h.eventNames()) != 1 {
		t.Fatalf("refusals publish nothing: %v", h.eventNames())
	}
}

func TestSuspendCarrierRefusesLaterBookings(t *testing.T) {
	h := newHarness(t)
	var reg, sus carrierWrite
	h.ok("register_carrier", registerArgs("reg", nil), &reg)
	h.ok("suspend_carrier", map[string]any{"scac": "ACMF", "reason": "insurance lapsed", "expectedVersion": 1, "idempotencyKey": "sus", "agentId": "risk-agent", "approvedBy": "ops-lead"}, &sus)
	c := sus.Carrier
	if c.Status != "SUSPENDED" || c.SuspensionReason == nil || *c.SuspensionReason != "insurance lapsed" || c.SuspendedAt == nil || c.Version != 2 {
		t.Fatalf("suspend_carrier = %+v", sus)
	}
	if sus.InitiatedBy.AgentID != "risk-agent" || sus.InitiatedBy.ApprovedBy != "ops-lead" || sus.Created {
		t.Fatalf("suspend_carrier attribution = %+v", sus)
	}
	if msg := h.fail("book_appointment", bookArgs("book-1", nil)); !strings.HasPrefix(msg, "carrier-suspended:") || !strings.Contains(msg, "insurance lapsed") {
		t.Fatalf("a booking naming the suspended SCAC: %q", msg)
	}
	if msg := h.fail("suspend_carrier", map[string]any{"scac": "ACMF", "reason": "again", "idempotencyKey": "sus-2"}); !strings.HasPrefix(msg, "carrier-already-suspended:") {
		t.Fatalf("a second suspension: %q", msg)
	}
}

func TestReinstateCarrierLetsItBookAgain(t *testing.T) {
	h := newHarness(t)
	var reg, sus, rein carrierWrite
	h.ok("register_carrier", registerArgs("reg", nil), &reg)
	h.ok("suspend_carrier", map[string]any{"scac": "ACMF", "reason": "insurance lapsed", "idempotencyKey": "sus"}, &sus)
	if msg := h.fail("reinstate_carrier", map[string]any{"scac": "ACMF", "expectedVersion": 1, "idempotencyKey": "rein-0"}); !strings.HasPrefix(msg, "precondition-failed:") {
		t.Fatalf("a stale expectedVersion: %q", msg)
	}
	h.ok("reinstate_carrier", map[string]any{"scac": "ACMF", "expectedVersion": 2, "idempotencyKey": "rein"}, &rein)
	if rein.Carrier.Status != "ACTIVE" || rein.Carrier.SuspensionReason != nil || rein.Carrier.SuspendedAt != nil || rein.Carrier.Version != 3 {
		t.Fatalf("reinstate_carrier = %+v", rein)
	}
	var booked writeResult
	h.ok("book_appointment", bookArgs("book-2", nil), &booked)
	if booked.Appointment.State != "BOOKED" {
		t.Fatalf("a reinstated carrier books: %+v", booked)
	}
	want := []string{"CarrierRegistered", "CarrierSuspended", "CarrierReinstated", "DockAppointmentBooked"}
	if got := h.eventNames(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
}

func TestCarrierWriteRefusals(t *testing.T) {
	h := newHarness(t)
	var reg carrierWrite
	h.ok("register_carrier", registerArgs("reg", nil), &reg)
	for name, tc := range map[string]struct {
		tool string
		args map[string]any
		want string
	}{
		"suspend unknown":     {"suspend_carrier", map[string]any{"scac": "NOPE", "reason": "r", "idempotencyKey": "a"}, "carrier-not-found:"},
		"suspend bad scac":    {"suspend_carrier", map[string]any{"scac": "nope", "reason": "r", "idempotencyKey": "b"}, "invalid-scac:"},
		"suspend blank scac":  {"suspend_carrier", map[string]any{"scac": "", "reason": "r", "idempotencyKey": "c"}, "invalid-argument:"},
		"suspend no reason":   {"suspend_carrier", map[string]any{"scac": "ACMF", "reason": " ", "idempotencyKey": "d"}, "invalid-suspension-reason:"},
		"suspend neg version": {"suspend_carrier", map[string]any{"scac": "ACMF", "reason": "r", "expectedVersion": -1, "idempotencyKey": "e"}, "invalid-argument:"},
		"reinstate unknown":   {"reinstate_carrier", map[string]any{"scac": "NOPE", "idempotencyKey": "f"}, "carrier-not-found:"},
		"reinstate active":    {"reinstate_carrier", map[string]any{"scac": "ACMF", "idempotencyKey": "g"}, "carrier-not-suspended:"},
		"reinstate blank":     {"reinstate_carrier", map[string]any{"scac": "", "idempotencyKey": "h"}, "invalid-argument:"},
		"reinstate neg":       {"reinstate_carrier", map[string]any{"scac": "ACMF", "expectedVersion": -2, "idempotencyKey": "i"}, "invalid-argument:"},
	} {
		if msg := h.fail(tc.tool, tc.args); !strings.HasPrefix(msg, tc.want) {
			t.Errorf("%s: %q, want prefix %q", name, msg, tc.want)
		}
	}
}

func registerThree(t *testing.T, h *harness) {
	t.Helper()
	for i, s := range []string{"CCCC", "AAAA", "BBBB"} {
		var r carrierWrite
		h.ok("register_carrier", map[string]any{"scac": s, "name": "Carrier " + s, "idempotencyKey": fmt.Sprintf("r%d", i)}, &r)
	}
	var s carrierWrite
	h.ok("suspend_carrier", map[string]any{"scac": "BBBB", "reason": "r", "idempotencyKey": "s"}, &s)
}

func TestGetCarrier(t *testing.T) {
	h := newHarness(t)
	registerThree(t, h)
	var one carrierResult
	h.ok("get_carrier", map[string]any{"scac": "BBBB"}, &one)
	if one.SCAC != "BBBB" || one.Status != "SUSPENDED" || one.Version != 2 || one.USDOT != nil || one.MC != nil {
		t.Fatalf("get_carrier = %+v", one)
	}
	for scac, want := range map[string]string{"ZZZZ": "carrier-not-found:", "": "invalid-argument:", "bbbb": "invalid-scac:"} {
		if msg := h.fail("get_carrier", map[string]any{"scac": scac}); !strings.HasPrefix(msg, want) {
			t.Errorf("get_carrier(%q) = %q, want prefix %q", scac, msg, want)
		}
	}
}

func TestListCarriersPages(t *testing.T) {
	h := newHarness(t)
	registerThree(t, h)
	var p1, p2 carrierPage
	h.ok("list_carriers", map[string]any{"limit": 2}, &p1)
	if len(p1.Items) != 2 || p1.Items[0].SCAC != "AAAA" || p1.Items[1].SCAC != "BBBB" || p1.NextCursor == nil {
		t.Fatalf("page 1 = %+v", p1)
	}
	h.ok("list_carriers", map[string]any{"limit": 2, "cursor": *p1.NextCursor}, &p2)
	if len(p2.Items) != 1 || p2.Items[0].SCAC != "CCCC" || p2.NextCursor != nil {
		t.Fatalf("page 2 = %+v", p2)
	}
}

func TestListCarriersFiltersByStatus(t *testing.T) {
	h := newHarness(t)
	registerThree(t, h)
	var sus, act carrierPage
	h.ok("list_carriers", map[string]any{"status": "SUSPENDED"}, &sus)
	h.ok("list_carriers", map[string]any{"status": "ACTIVE"}, &act)
	if len(sus.Items) != 1 || sus.Items[0].SCAC != "BBBB" || len(act.Items) != 2 {
		t.Fatalf("by status: suspended %+v active %+v", sus, act)
	}
}

func TestListCarriersRefusals(t *testing.T) {
	h := newHarness(t)
	for name, tc := range map[string]struct {
		args map[string]any
		want string
	}{
		"bad status": {map[string]any{"status": "GONE"}, "invalid-argument:"},
		"bad limit":  {map[string]any{"limit": 501}, "invalid-argument:"},
		"bad cursor": {map[string]any{"cursor": "!!"}, "invalid-cursor:"},
	} {
		if msg := h.fail("list_carriers", tc.args); !strings.HasPrefix(msg, tc.want) {
			t.Errorf("%s: %q, want prefix %q", name, msg, tc.want)
		}
	}
}

func TestReschedulingASuspendedCarriersAppointmentIsRefused(t *testing.T) {
	h := newHarness(t)
	var booked writeResult
	h.ok("book_appointment", bookArgs("b1", nil), &booked) // the carrier is not registered yet: accepted
	var reg, sus carrierWrite
	h.ok("register_carrier", registerArgs("reg", nil), &reg)
	h.ok("suspend_carrier", map[string]any{"scac": "ACMF", "reason": "insurance lapsed", "idempotencyKey": "s"}, &sus)
	msg := h.fail("reschedule_appointment", map[string]any{"appointmentId": booked.Appointment.AppointmentID, "windowStart": at10.Format("2006-01-02T15:04:05Z"), "windowEnd": at11.Format("2006-01-02T15:04:05Z"), "idempotencyKey": "r1"})
	if !strings.HasPrefix(msg, "carrier-suspended:") {
		t.Fatalf("reschedule: %q", msg)
	}
	var got apptOut
	h.ok("get_appointment", map[string]any{"appointmentId": booked.Appointment.AppointmentID}, &got)
	if got.State != "BOOKED" {
		t.Fatalf("suspending a carrier cancels nothing: %+v", got)
	}
}
