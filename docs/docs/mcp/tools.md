---
id: tools
title: MCP tools
sidebar_label: MCP tools
description: "Every tool, resource and prompt of the dock-scheduling MCP server with input fields, read-only or write annotation and authority level."
---

# MCP tools

`cmd/mcp` is a Model Context Protocol server over **Streamable HTTP only** (no stdio, no SSE endpoint of its own), served at `/mcp` and `/` on `MCP_ADDR` (default `:8090`). It drives the same use cases as the REST API. It is **unauthenticated** like the rest of the fleet (`TestNoAuthMiddlewareReintroduced` guards it): access control is the in-cluster boundary. The Kong route, when enabled, is `/api/mcp/dock-scheduling` rewritten to `/mcp`. The server is disabled by default in the chart (`mcp.enabled=false`).

Source: `internal/adapters/inbound/mcp/tools.go`, `tools_read.go`, `tools_write.go`, `resources.go`, `prompts.go`, `authority.go`, [ADR 0007](/docs/adr/0007-mcp-server).

This page is written from the tool registrations in the code, not from a captured `tools/list` response; the speed rules for this site forbid running the binary. `TestMCPSurfaceSensorBites` and `TestEveryMCPToolDeclaresAuthority` in `internal/architecture` guard the registrations.

Every tool declares an authority level in its `_meta` under `com.warehouse/authority`: **L1** reads facts and changes nothing; **L3** changes state, with a human confirmation held by the client and an attributable change (`TestMCPWriteToolsAreAtLeastL3`). Calendar settings, closures, operating hours and the no-show sweep are planner or cron operations and stay on REST: they are not tools.

Outputs mirror the REST bodies (camelCase, UTC RFC 3339 times, `null` for an absent optional value). A failing tool answers with the REST problem slug and detail (for example `door-double-booked`); infrastructure detail is never echoed.

## Read tools (L1, `readOnlyHint: true`)

| Tool | Input fields | Returns |
|---|---|---|
| `list_appointments` | all optional: `siteId`, `doorCode`, `state` (`BOOKED`, `CHECKED_IN`, `COMPLETED`, `CANCELLED`, `NO_SHOW`), `direction` (`INBOUND`, `OUTBOUND`), `carrierScac`, `atRisk` (true lists only at-risk), `from`, `to` (RFC 3339; windows overlapping `[from, to)`), `limit` (1..500, default 100), `cursor` | A page `{items, nextCursor}` ordered by window start. |
| `get_appointment` | `appointmentId` (`appt-<uuid>`, required) | One appointment: door, window, carrier, ASNs or CPT/shipment references, state, latest ETA, arrival, `atRisk` with cause, and the `version` to pass as `expectedVersion`. |
| `get_door_availability` | `doorCode`, `from`, `to` (required; at most 14 days), `minutes` (optional, 1..240, default the site's slot granularity) | `freeWindows` and `busy` spans of the door; free windows respect operating hours and closures. |
| `get_dock_calendar` | `siteId` (required) | Settings with defaults applied, operating hours (none declared = open 24x7) and active closures. |
| `list_at_risk_appointments` | optional `siteId`, `limit`, `cursor` | `BOOKED` appointments flagged at risk (cause `LATE_ETA` or `DOOR_REASSIGNED_OVERLAP`), paged. Advisory: nothing reschedules automatically. |

## Write tools (L3, `readOnlyHint: false`, idempotent)

Every write tool takes `idempotencyKey` (required, 1..128 printable ASCII; retry with the same key and arguments to get the stored answer, never a second write) and the optional attribution fields `proposalId`, `approvedBy`, `agentId` (each at most 128 characters), recorded on the write and logged as `mcp write`. Every answer is `{appointment, atRisk, initiatedBy, replayed}`: `replayed` is true when the stored answer of an earlier call was returned; `atRisk` is meaningful for `report_eta` only.

| Tool | Annotations | Input fields | Effect |
|---|---|---|---|
| `book_appointment` | `destructiveHint: false`, `idempotentHint: true` | `direction`, `doorCode`, `carrier`, `windowStart`, `windowEnd`, `idempotencyKey` (required); `carrierScac`, `asnNumbers`, `cptId`, `shipmentRefs`, `expectedSealNumber` (optional) | Books a door window. INBOUND needs at least one known, not cancelled ASN; OUTBOUND a known `cptId` or `shipmentRefs`. Same checks as `POST /appointments`. |
| `reschedule_appointment` | `destructiveHint: true`, `idempotentHint: true` | `appointmentId`, `windowStart`, `windowEnd`, `idempotencyKey` (required); `doorCode` (omit to keep the door), `expectedVersion` | Moves a `BOOKED` appointment; refused with `precondition-failed` when `expectedVersion` is stale. |
| `cancel_appointment` | `destructiveHint: true`, `idempotentHint: true` | `appointmentId`, `reason` (`CARRIER_REQUEST` or `SITE_REQUEST`), `idempotencyKey` (required); `expectedVersion` | Cancels a `BOOKED` or `CHECKED_IN` appointment and frees the window. Other reasons are set by policies, never by a caller. |
| `report_eta` | `destructiveHint: false`, `idempotentHint: true` | `appointmentId`, `etaAt`, `idempotencyKey` (required); `source` (at most 64 characters, default `mcp`) | Records the carrier's latest ETA on a `BOOKED` appointment; a late ETA flags it at risk once for this report. Nothing is rescheduled. |

An appointment's arrival, check-in and door release are never tools: they are facts applied from the yard.

## Resources

Scoped read models (L1 by nature, JSON), each backed by the same use case as the matching tool:

| URI template | Content |
|---|---|
| `dock://sites/{siteId}/calendar` | One site's dock calendar. |
| `dock://sites/{siteId}/at-risk` | The first page (100) of the site's at-risk appointments. |
| `dock://appointments/{appointmentId}` | One appointment. |

## Prompts

| Prompt | Purpose |
|---|---|
| `plan_dock_booking` | Procedure for booking or moving a carrier visit: read the calendar rules, find a free door window, check the ASN is on no other appointment, confirm with a person, book with an idempotency key, read the typed refusals. |
| `triage_at_risk_appointments` | Procedure for at-risk appointments: read the cause, compare the ETA with the window, propose a concrete reschedule, wait for confirmation, escalate when no ETA was reported. |

## Operating notes

- `cmd/mcp` starts no outbox relay and no consumer. Its writes enqueue outbox rows that the relay of `cmd/api` (or `cmd/relay`) drains; keep one running.
- It needs `DATABASE_URL` and `KAFKA_BROKERS` (for `/readyz`) and applies the embedded migrations at boot. See [Configuration](/docs/operations/configuration).
- The MCP server is not HPA-scalable: the Streamable HTTP handler keeps per-process session state.
- Tool calls are wrapped in an `mcp.tool <name>` span through the global OTel tracer; no provider is installed, so spans are not exported (see [Observability](/docs/operations/observability)).
