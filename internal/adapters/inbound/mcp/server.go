// Package mcp is the inbound Model Context Protocol adapter of dock-scheduling:
// a second driving adapter over the SAME application-layer use cases the REST
// adapter uses, built on the official MCP Go SDK and served over Streamable
// HTTP only (separate binary, cmd/mcp; docs/adr/0007).
//
// Fourteen intent-level tools: seven reads (list_appointments, get_appointment,
// get_door_availability, get_dock_calendar, list_at_risk_appointments,
// list_carriers, get_carrier, all L1) and seven writes (book_appointment,
// reschedule_appointment, cancel_appointment, report_eta, register_carrier,
// suspend_carrier, reinstate_carrier, all L3) that carry optional agent attribution
// (proposalId, approvedBy, agentId -> initiated_by) and a mandatory
// idempotencyKey with the REST Idempotency-Key semantics. Door calendar
// settings, closures, hours, the no-show sweep, a registered carrier's data
// update and every yard fact stay on REST or on Kafka; no tool widens what the
// domain guards (ADR 0007, ADR 0012).
//
// This package depends inward on the application layer and the domain only,
// never on an outbound adapter or the REST adapter, and nothing depends on it
// (internal/architecture TestMCPAdapterDependencyRule). The composition root
// cmd/mcp wires concrete adapters into the use cases. There is no auth of any
// kind (fleet-wide revert 2026-09-11).
package mcp

import (
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// serverName and serverVersion identify this server in the MCP handshake.
const (
	serverName    = "dock-scheduling-mcp"
	serverVersion = "1.0.0"
)

// instructions is what an MCP client is told about this server at initialize.
const instructions = "Door calendar of the warehouse: every booked time window on every dock door, inbound and outbound. " +
	"Read it with list_appointments (filters siteId, doorCode, state, direction, carrierScac, atRisk, from, to), get_appointment, " +
	"get_door_availability (free windows of one door inside a range), get_dock_calendar (a site's hours, closures and settings) and " +
	"list_at_risk_appointments (booked appointments whose latest ETA threatens the window), and the carrier registry with list_carriers and " +
	"get_carrier (a booking naming a SUSPENDED carrier's SCAC is refused; an unregistered SCAC is accepted). " +
	"Change it with book_appointment, reschedule_appointment, cancel_appointment, report_eta, register_carrier, suspend_carrier and reinstate_carrier: each needs an idempotencyKey " +
	"(retry with the SAME key and arguments to get the same answer, never a second booking) and takes optional proposalId, " +
	"approvedBy and agentId so the change is attributable to the agent that made it. Arrival, check-in and door release are " +
	"facts of yard-management and are never written here. Start with the plan_dock_booking prompt."

// NewServer builds the MCP server for this bounded context with every tool,
// resource and workflow prompt registered.
func NewServer(deps Deps) *mcp.Server {
	server := mcp.NewServer(
		&mcp.Implementation{Name: serverName, Version: serverVersion},
		&mcp.ServerOptions{Instructions: instructions},
	)
	deps.registerTools(server)
	deps.registerResources(server)
	deps.registerPrompts(server)
	return server
}

// Handler returns the Streamable HTTP handler for the MCP server.
func Handler(server *mcp.Server) http.Handler {
	// Stateless (Agentic Standard v2, AS-4; docs/adr/0007): no Mcp-Session-Id
	// is issued, so the server also answers the 2026-07-28 revision's
	// per-request handshake (server/discover) while legacy initialize clients
	// keep working. Nothing here relies on cross-call session state; every
	// write is made safe to retry by its idempotencyKey instead.
	return mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{Stateless: true},
	)
}
