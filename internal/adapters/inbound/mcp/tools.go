package mcp

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// tracerName is the OTel instrumentation scope for MCP tool spans.
const tracerName = "github.com/claudioed/dock-scheduling/internal/adapters/inbound/mcp"

// registerTools adds every tool to the server, each wrapped so its handler
// runs inside an OTel span named "mcp.tool <name>".
//
// The charter asks for intent-level tools, not one per endpoint: the first nine
// are exactly the read and booking verbs of decision D-6 (calendar settings,
// closures, hours and the no-show sweep are planner/cron operations and stay
// on REST); the carrier registry adds five (ADR 0012): list_carriers and
// get_carrier (L1), register_carrier, suspend_carrier and reinstate_carrier (L3).
// Changing a registered carrier's data (UpdateCarrier) stays on REST.
func (d Deps) registerTools(server *mcp.Server) {
	d.registerReadTools(server)
	d.registerWriteTools(server)
	d.registerCarrierTools(server)
}

func (d Deps) registerReadTools(server *mcp.Server) {
	readOnly := true

	addTool(server, &mcp.Tool{
		Name: "list_appointments",
		Description: "Page through dock appointments ordered by window start (at most 500 per page; follow nextCursor). Every filter is optional: " +
			"siteId, doorCode, state (BOOKED, CHECKED_IN, COMPLETED, CANCELLED, NO_SHOW), direction (INBOUND, OUTBOUND), carrierScac, " +
			"atRisk=true (only appointments flagged at risk) and from/to (RFC 3339; appointments whose window overlaps [from, to)).",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: readOnly},
		Meta:        authorityMeta(authorityL1),
	}, d.listAppointments)

	addTool(server, &mcp.Tool{
		Name: "get_appointment",
		Description: "Read one dock appointment by id (appt-<uuid>): door, window, carrier, ASNs or CPT/shipment references, state, " +
			"latest ETA, recorded arrival, atRisk with its cause, and the version to pass as expectedVersion on a change.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: readOnly},
		Meta:        authorityMeta(authorityL1),
	}, d.getAppointment)

	addTool(server, &mcp.Tool{
		Name: "get_door_availability",
		Description: "Free and busy windows of one dock door inside [from, to) (RFC 3339, at most 14 days). Free windows respect the site's " +
			"operating hours and closures and are cut to `minutes` (1..240; default the site's slot granularity). Look here before booking.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: readOnly},
		Meta:        authorityMeta(authorityL1),
	}, d.getDoorAvailability)

	addTool(server, &mcp.Tool{
		Name: "get_dock_calendar",
		Description: "A site's door calendar with defaults applied: booking settings (max window, slot granularity, arrival tolerance, " +
			"no-show grace), door operating hours (none declared = open 24x7) and the active closures.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: readOnly},
		Meta:        authorityMeta(authorityL1),
	}, d.getDockCalendar)

	addTool(server, &mcp.Tool{
		Name: "list_at_risk_appointments",
		Description: "BOOKED appointments flagged at risk: the carrier's latest ETA is later than window start plus the arrival tolerance " +
			"(cause LATE_ETA) or the physical door overlaps a later booking (DOOR_REASSIGNED_OVERLAP). Advisory: nothing reschedules automatically. " +
			"Optionally narrowed to one site; paged like list_appointments.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: readOnly},
		Meta:        authorityMeta(authorityL1),
	}, d.listAtRisk)
}

func (d Deps) registerWriteTools(server *mcp.Server) {
	additive := false
	destructive := true
	idempotent := true

	addTool(server, &mcp.Tool{
		Name: "book_appointment",
		Description: "Book a dock door window for one carrier visit. INBOUND needs at least one known, not cancelled asnNumbers entry; OUTBOUND needs a known cptId " +
			"or shipmentRefs. The window is half-open [windowStart, windowEnd), on the site's slot granularity, not in the past, inside the door's " +
			"operating hours and outside its closures, and may not overlap another BOOKED or CHECKED_IN appointment of the door. Refused with a typed " +
			"error otherwise (door-double-booked, door-unavailable, unknown-asn, asn-already-scheduled, window-in-past, carrier-suspended, ...). Check get_door_availability first. " +
			"A carrierScac that is in the carrier registry and SUSPENDED is refused (carrier-suspended); an ACTIVE or unregistered SCAC, or none, is accepted as before. " +
			"Needs an idempotencyKey: retry with the same key and arguments to get the same answer, never a second booking.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &additive, IdempotentHint: idempotent},
		Meta:        authorityMeta(authorityL3),
	}, d.bookAppointment)

	addTool(server, &mcp.Tool{
		Name: "reschedule_appointment",
		Description: "Move a BOOKED appointment to a new window and/or door (omit doorCode to keep the door). The same checks as a booking apply to the new " +
			"door and window (a suspended carrier's appointment cannot be moved: carrier-suspended). Pass expectedVersion (from get_appointment) to refuse the change if someone else changed the appointment meanwhile " +
			"(precondition-failed). Needs an idempotencyKey.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &destructive, IdempotentHint: idempotent},
		Meta:        authorityMeta(authorityL3),
	}, d.rescheduleAppointment)

	addTool(server, &mcp.Tool{
		Name: "cancel_appointment",
		Description: "Cancel a BOOKED or CHECKED_IN appointment and free its door window. reason is CARRIER_REQUEST or SITE_REQUEST (the other reasons are set " +
			"by policies, never by a caller). Pass expectedVersion to guard against a concurrent change. Needs an idempotencyKey.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &destructive, IdempotentHint: idempotent},
		Meta:        authorityMeta(authorityL3),
	}, d.cancelAppointment)

	addTool(server, &mcp.Tool{
		Name: "report_eta",
		Description: "Record the carrier's latest estimated arrival on a BOOKED appointment. An etaAt later than the window start plus the site's arrival " +
			"tolerance flags the appointment at risk once for this report (atRisk=true in the answer); nothing is rescheduled. Needs an idempotencyKey.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &additive, IdempotentHint: idempotent},
		Meta:        authorityMeta(authorityL3),
	}, d.reportEta)
}

// addTool registers one tool. It centralises the cross-cutting concerns every
// tool shares: a span per call carrying the tool's authority level, and
// mapping a handler error onto the span and onto the tool error the model sees
// (REST slug + detail; infrastructure detail is never echoed).
func addTool[In, Out any](server *mcp.Server, tool *mcp.Tool, handle func(context.Context, In) (Out, error)) {
	level, _ := tool.Meta[authorityMetaKey].(string)
	mcp.AddTool(server, tool, func(ctx context.Context, _ *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error) {
		var zero Out
		ctx, span := otel.Tracer(tracerName).Start(ctx, "mcp.tool "+tool.Name, trace.WithAttributes(
			attribute.String("mcp.tool.name", tool.Name),
			attribute.String("warehouse.authority", level),
		))
		defer span.End()

		out, err := handle(ctx, in)
		if err != nil {
			mapped := mapError(err)
			span.RecordError(mapped)
			span.SetStatus(codes.Error, mapped.Error())
			return nil, zero, mapped
		}
		return nil, out, nil
	})
}

// requireText rejects a blank required text argument instead of defaulting it.
func requireText(name, v string) error {
	if v == "" {
		return argError("%s is required", name)
	}
	return nil
}
