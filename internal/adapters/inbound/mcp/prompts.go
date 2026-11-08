package mcp

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// planDockBookingSOP is the procedure the plan_dock_booking prompt hands to the
// model (Agentic Standard v2, AS-3). Prompts encode how to interpret results,
// when to escalate and what "done" means.
const planDockBookingSOP = `You are helping a dock planner or carrier scheduler put a carrier visit on the door calendar. The tools of this context read facts (L1) and change the calendar (L3: book_appointment, reschedule_appointment, cancel_appointment, report_eta). A change is made only after a person has confirmed the exact window and door; you propose, the client confirms, then you call.

Procedure:
1. Understand the visit: direction (INBOUND needs its ASN numbers, OUTBOUND a cptId or shipmentRefs), carrier, the wanted time range and the site.
2. Read the rules: get_dock_calendar(siteId) gives the slot granularity (windows start and end on it), the maximum window, the arrival tolerance and the declared hours and closures (no hours declared = open 24x7).
3. Find room: get_door_availability(doorCode, from, to, minutes) per candidate door whose flow allows the direction. Free windows already respect hours and closures. Prefer the door and window the person asked for; offer at most three alternatives.
4. Check for an existing booking of the same ASNs: list_appointments(state: BOOKED) and match on asnNumbers. An ASN may sit on only one active appointment (asn-already-scheduled): reschedule that appointment instead of booking a second.
5. After the person confirms, call book_appointment (or reschedule_appointment with the expectedVersion from get_appointment). ALWAYS send a fresh idempotencyKey for a new request, and the SAME key when you retry the same request after a timeout. Pass agentId and, when a person approved it, approvedBy (and proposalId if you hold one).

Interpretation of refusals (the error text starts with the slug): door-double-booked or door-unavailable -> re-read availability and offer another window; window-in-past or invalid-window -> fix the window, do not guess; unknown-asn -> the ASN is not registered or was cancelled: tell the person, do not book without it; precondition-failed or concurrent-update -> re-read the appointment and start over; illegal-transition -> the appointment is no longer BOOKED (the carrier may already be on site): stop and escalate.

Never try to record an arrival, a check-in or a door release: those are facts from the yard and are applied from there. Never cancel with a reason other than CARRIER_REQUEST or SITE_REQUEST.

Done means: you reported the appointment id, door, window and state as the tool returned them (state BOOKED), or the typed reason the booking was refused and the alternatives you offered.`

// triageAtRiskSOP is the procedure of the triage_at_risk_appointments prompt.
const triageAtRiskSOP = `You are helping a dock planner decide what to do about appointments that may miss their window. Use list_at_risk_appointments(siteId) and get_appointment; nothing is rescheduled automatically.

Procedure:
1. List the at-risk appointments of the site. atRiskCause LATE_ETA means the carrier's latest ETA is later than window start plus the site's arrival tolerance; DOOR_REASSIGNED_OVERLAP means a truck docked at another door than booked and a later booking on that door now overlaps.
2. For each, read eta.etaAt against windowStart and windowEnd. An ETA still inside the window is a warning only; an ETA past windowEnd means the visit will become a no-show once the site's no-show grace passes.
3. For a late ETA, check get_door_availability for the same door from the new ETA: if the door is free, propose reschedule_appointment to the first free window; if not, propose another door that allows the direction.
4. For an overlap, name both appointments and propose moving the later one; the truck already at the door wins.

Escalate to a person when the carrier has not reported an ETA at all and the window is about to end, or when the proposal cancels a visit. reschedule_appointment and cancel_appointment are L3 changes: describe the exact change and wait for confirmation before calling, with a fresh idempotencyKey and your agentId (and approvedBy).

Done means: every at-risk appointment of the site has a stated cause, the gap between ETA and window, and either no action needed or one concrete proposed change.`

// registerPrompts adds the workflow prompts (operational SOPs). Prompts are L1
// by nature and, per the charter, need no tool Meta.
func (d Deps) registerPrompts(server *mcp.Server) {
	addSOP(server, "plan_dock_booking",
		"Standard operating procedure for booking or moving a carrier visit on the door calendar: read the rules, find a free door window, confirm with a person, book with an idempotency key and read the refusals.",
		"How to plan a dock booking: calendar rules, door availability, ASN uniqueness, a confirmed change with an idempotency key, and typed refusals.",
		planDockBookingSOP)
	addSOP(server, "triage_at_risk_appointments",
		"Standard operating procedure for appointments that may miss their window: read the cause, compare ETA with window, propose a concrete reschedule and wait for confirmation.",
		"How to triage at-risk dock appointments: cause, ETA against window, a concrete reschedule proposal, escalation.",
		triageAtRiskSOP)
}

func addSOP(server *mcp.Server, name, description, resultDescription, text string) {
	server.AddPrompt(&mcp.Prompt{Name: name, Description: description},
		func(context.Context, *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
			return &mcp.GetPromptResult{
				Description: resultDescription,
				Messages:    []*mcp.PromptMessage{{Role: "user", Content: &mcp.TextContent{Text: text}}},
			}, nil
		})
}
