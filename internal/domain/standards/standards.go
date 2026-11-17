// Package standards is the ONE place that says which standard meaning a
// published dock-scheduling fact corresponds to (ADR 0013): the ANSI X12 214
// shipment status code and the GS1 EPCIS Core Business Vocabulary business
// step. Partners read those, not this context's private event names and enums.
//
// The rule is "only honest mappings": a pair (event, direction) maps to a code
// only when the standard's own definition says what the fact says. Everything
// else is decided NOT mapped, with the reason written next to it, and the
// published field is then omitted (never an invented or approximate code).
// Every published event type must have a decision here: the tests enumerate the
// types from the domain sources and the AsyncAPI contract, so a new type
// without a decision fails the build.
//
// Pure Go: no I/O, no clock, nothing outside the domain.
package standards

import (
	"strings"

	"github.com/claudioed/dock-scheduling/internal/domain/shared"
)

// ANSI X12 214 (Transportation Carrier Shipment Status Message) codes used by
// this mapping, with the official names of the code lists (stedi.com/edi/
// x12-004010/element/1652 and .../1650, cross-checked on x12-008040). The AT7
// segment carries them in two different elements:
//   - AA and AB are Shipment Appointment Status Codes (element 1652, AT7-03),
//   - X1 and X3 are Shipment Status Codes (element 1650, AT7-01).
const (
	// X12PickUpAppointment is AA, "Pick-up Appointment Date and/or Time".
	X12PickUpAppointment = "AA"
	// X12DeliveryAppointment is AB, "Delivery Appointment Date and/or Time".
	X12DeliveryAppointment = "AB"
	// X12ArrivedAtPickUp is X3, "Arrived at Pick-up Location".
	X12ArrivedAtPickUp = "X3"
	// X12ArrivedAtDelivery is X1, "Arrived at Delivery Location".
	X12ArrivedAtDelivery = "X1"
)

// CBVArriving is the GS1 Core Business Vocabulary 2.0 business step
// "arriving" in its URN form (CBV section 7.1; ref.gs1.org/cbv/BizStep-arriving):
// "a specific activity within a business process where an object arrives at a
// location". The URN form is the one and only form this context publishes.
const CBVArriving = "urn:epcglobal:cbv:bizstep:arriving"

// Semantics is the standard meaning of one fact. An empty field means "no
// honest mapping" and is omitted from the published payload.
type Semantics struct {
	// X12Status is the X12 214 code, or "".
	X12Status string
	// CBVBizStep is the CBV business step URN, or "".
	CBVBizStep string
}

// Decision is one row of the mapping table: the decision for an event, for one
// direction (or for both when Direction is empty).
type Decision struct {
	// Event is the event name, e.g. "DockAppointmentBooked".
	Event string
	// Direction is the appointment direction the row applies to; empty for a
	// decision that does not depend on it (and for events with no direction).
	Direction shared.Direction
	Semantics
	// Reason says why a field is NOT mapped. It is empty exactly when both
	// fields are mapped.
	Reason string
}

// The reasons a mapping is withheld, shared by several rows.
const (
	reasonNoStep     = "the CBV has no business step for scheduling a door window"
	reasonNotShip    = "this is a door-calendar fact, not a shipment status; X12 214 reports shipments"
	reasonCarrierReg = "a carrier registry change is not a shipment event in either standard"
)

// reasons joins the clauses of a reason.
func reasons(clauses ...string) string { return strings.Join(clauses, "; ") }

// decisions is the mapping table. Order is the order of the ADR 0013 table.
var decisions = []Decision{
	{Event: "DockAppointmentBooked", Direction: shared.Inbound, Semantics: Semantics{X12Status: X12DeliveryAppointment},
		Reason: reasonNoStep},
	{Event: "DockAppointmentBooked", Direction: shared.Outbound, Semantics: Semantics{X12Status: X12PickUpAppointment},
		Reason: reasonNoStep},
	{Event: "DockAppointmentRescheduled", Direction: shared.Inbound, Semantics: Semantics{X12Status: X12DeliveryAppointment},
		Reason: reasonNoStep},
	{Event: "DockAppointmentRescheduled", Direction: shared.Outbound, Semantics: Semantics{X12Status: X12PickUpAppointment},
		Reason: reasonNoStep},
	{Event: "DockAppointmentCancelled",
		Reason: reasons("X12 CA is Shipment Cancelled, but cancelling a door window does not cancel the shipment", reasonNoStep)},
	{Event: "DockAppointmentNoShow",
		Reason: reasons("X12 SN (Shipment Not Picked Up By Carrier) is absent from the 004010 code list and a lapsed window does not mean the pick-up cannot still happen",
			"X12 1651 AY and A1 are reason codes, not statuses", reasonNoStep)},
	{Event: "DockAppointmentCheckedIn", Direction: shared.Inbound, Semantics: Semantics{X12Status: X12ArrivedAtDelivery, CBVBizStep: CBVArriving}},
	{Event: "DockAppointmentCheckedIn", Direction: shared.Outbound, Semantics: Semantics{X12Status: X12ArrivedAtPickUp, CBVBizStep: CBVArriving}},
	{Event: "DockAppointmentDoorReassigned",
		Reason: reasons("an internal move to another door; the appointment date and time, which is what X12 AA and AB state, did not change", reasonNoStep)},
	{Event: "DockAppointmentCompleted",
		Reason: reasons("completion is the door release, which does not say the handling finished (an implicit arrival completes with no check-in)",
			"X12 D1 (Completed Unloading at Delivery Location) and CP (Completed Loading at Pick-up Location) and the CBV steps unloading and loading would overstate it")},
	{Event: "DockAppointmentAtRisk",
		Reason: reasons("an advisory this context derives from a reported ETA, not a carrier-reported status: X12 X2 (Estimated Date and/or Time of Arrival at Consignee's Location) and SD (Shipment Delayed) state different things",
			reasonNoStep)},
	{Event: "DoorClosureScheduled", Reason: reasons(reasonNotShip, reasonNoStep)},
	{Event: "DoorClosureLifted", Reason: reasons(reasonNotShip, reasonNoStep)},
	{Event: "DoorOperatingHoursDeclared", Reason: reasons(reasonNotShip, reasonNoStep)},
	{Event: "CarrierRegistered", Reason: reasonCarrierReg},
	{Event: "CarrierSuspended", Reason: reasonCarrierReg},
	{Event: "CarrierReinstated", Reason: reasonCarrierReg},
}

// Decisions returns a copy of the mapping table.
func Decisions() []Decision { return append([]Decision(nil), decisions...) }

// Events returns every event name the table decides, once each, in table order.
func Events() []string {
	var out []string
	for _, d := range decisions {
		if len(out) == 0 || out[len(out)-1] != d.Event {
			out = append(out, d.Event)
		}
	}
	return out
}

// Of returns the standard meaning of an event raised for an appointment of the
// given direction (use "" for a fact with no direction). known is false when the
// event has no decision at all: the publisher then publishes no standard field,
// and the exhaustiveness tests make that state unreachable in a build that
// passes.
func Of(event string, direction shared.Direction) (s Semantics, known bool) {
	for _, d := range decisions {
		if d.Event != event {
			continue
		}
		known = true
		if d.Direction == "" || d.Direction == direction {
			return d.Semantics, true
		}
	}
	return Semantics{}, known
}
