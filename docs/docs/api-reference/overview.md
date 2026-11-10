---
id: overview
title: API reference
sidebar_label: Overview
slug: /api-reference
description: "The interfaces of dock-scheduling (REST, events, MCP, reports), every endpoint, and the contract conventions: problems, idempotency, ETags, paging."
---

# API reference

Source: `apis/openapi.yaml`, `apis/openapi-reports.yaml`, `apis/asyncapi.yaml`, `internal/adapters/inbound/http/server.go`, `reports_router.go`, `internal/adapters/inbound/mcp/`.

All interfaces are **unauthenticated** (fleet-wide; `TestNoAuthMiddlewareReintroduced` guards REST and MCP). Behind Kong the context's paths live under `/api/dock-scheduling`.

## Interfaces

| Interface | Binary | Address | Contract |
| --- | --- | --- | --- |
| REST (OLTP) | `cmd/api` | `:8080`; Kong `/api/dock-scheduling` | `apis/openapi.yaml`; generated pages under [REST reference](/docs/api-reference/rest/dock-scheduling-api) |
| Reports (REST, read-only) | `cmd/reports` | `:8092`; Kong `/api/dock-scheduling/reports` rewritten to `/reports` | `apis/openapi-reports.yaml` |
| Events (Kafka, CloudEvents 1.0) | outbox relay in `cmd/api` or `cmd/relay` | `warehouse.dock-scheduling.events`, `.analytics` | `apis/asyncapi.yaml`, [Events](/docs/api-reference/events) |
| MCP (Streamable HTTP) | `cmd/mcp` | `:8090`, `/mcp` and `/`; Kong `/api/mcp/dock-scheduling` | [MCP tools](/docs/mcp/tools) |
| A2A | none | none | This service has no A2A server (`cmd/a2a` does not exist). |

## REST endpoints (`cmd/api`)

| Method and path | operationId | Purpose | Idempotency-Key |
| --- | --- | --- | --- |
| `POST /appointments` | `bookAppointment` | Book a door window (201, `Location`, `ETag`). | required |
| `GET /appointments` | `listAppointments` | Keyset-paged list; filters site, door, state, direction, carrier SCAC, at-risk, time range. | |
| `GET /appointments/{appointmentId}` | `getAppointment` | One appointment with its `ETag`. | |
| `POST /appointments/{appointmentId}/reschedules` | `rescheduleAppointment` | Move a `BOOKED` appointment (window and/or door); `If-Match` honoured. | required |
| `POST /appointments/{appointmentId}/cancellations` | `cancelAppointment` | Cancel (`CARRIER_REQUEST`, `SITE_REQUEST`); `If-Match` honoured. | required |
| `POST /appointments/{appointmentId}/eta-reports` | `reportEta` | Record the carrier's ETA; a late one flags the appointment at risk. | required |
| `GET /doors/{doorCode}/availability` | `getDoorAvailability` | Free windows and busy spans in `[from, to)` (at most 14 days). | |
| `GET /sites/{siteId}/dock-calendar` | `getDockCalendar` | Settings, operating hours, closures. | |
| `PUT /sites/{siteId}/dock-calendar/operating-hours` | `declareOperatingHours` | Replace the site's operating-hours rows. | idempotent by method |
| `PUT /sites/{siteId}/dock-calendar/settings` | `declareCalendarSettings` | Replace booking settings. | idempotent by method |
| `POST /sites/{siteId}/dock-calendar/closures` | `scheduleDoorClosure` | Schedule a closure (201). | required |
| `POST /sites/{siteId}/dock-calendar/closures/{closureId}/lifts` | `liftDoorClosure` | Lift a closure. | required |
| `POST /no-show-sweeps` | `runNoShowSweep` | Mark `BOOKED` appointments past window end plus grace as `NO_SHOW`. | required (the CronJob sends one) |
| `GET /healthz`, `GET /readyz`, `GET /metrics` | `healthz`, `readyz`, `metrics` | Liveness, readiness, Prometheus scrape. | |

There is no route to record an arrival, a check-in or a door release: those are applied from yard events.

## Reports endpoints (`cmd/reports`)

| Method and path | operationId | Purpose |
| --- | --- | --- |
| `GET /reports/schedule-adherence` | `getScheduleAdherence` | On-time, early, late and no-show share per site and carrier; filters `siteId`, `carrierScac`, `from`, `to` (an invalid range is 400 `invalid-report-range`). |
| `GET /reports/freshness` | `getReportsFreshness` | `asOf` and `lagSeconds` of the projection (both `null` before the first event). |
| `GET /healthz`, `GET /readyz`, `GET /metrics` | | Probes and scrape. |

## Contract conventions

- **JSON** is camelCase on REST and MCP, times are UTC RFC 3339; event payloads are snake_case.
- **Errors** are RFC 7807 `application/problem+json`, `type = https://errors.dock-scheduling.warehouse-systems.dev/<slug>`. The slug catalogue is on [Use cases](/docs/ddd/use-cases) and [Troubleshooting](/docs/operations/troubleshooting). 409 is a conflict with state, 422 a value or reference rule, 404 an unknown resource, 412 a failed precondition. A 500 never leaks the internal message.
- **Idempotency.** Every state-changing `POST` requires `Idempotency-Key` (1 to 128 printable ASCII): missing is 400 `idempotency-key-required`, malformed 400 `idempotency-key-invalid`. A replay with the same method, path and body returns the stored answer (any status below 500, 4xx included) with `Idempotency-Replayed: true`; the same key with something else is 409 `idempotency-key-reused`. A 5xx is never stored. The aggregate, its outbox rows and the key commit together.
- **Concurrency.** An appointment carries a `version`, exposed as the weak `ETag` `W/"<version>"`; `If-Match` is honoured, optionally, on reschedule and cancel (412 `precondition-failed` on a mismatch). The MCP equivalent is `expectedVersion`.
- **Paging.** Keyset paging with `limit` (1 to 500, default 100) and `cursor`; the answer is `{items, nextCursor}`. An unsupported query parameter is 400 `unknown-query-parameter`.
- **Windows** are half-open `[windowStart, windowEnd)`: `[10:00, 12:00)` and `[12:00, 14:00)` are compatible. They start and end on the site's slot granularity and last at most the site's maximum (default 4 h).
- **Appointment state** runs `BOOKED`, `CHECKED_IN`, `COMPLETED`; or `BOOKED` or `CHECKED_IN` to `CANCELLED`; or `BOOKED` to `NO_SHOW`. The last three are terminal.
- **Probes.** `/readyz` is 503 while Postgres or Kafka is unreachable (reports: the analytical database only), and while draining.
- **Defaults** of a site's settings: maximum window 240 minutes, slot granularity 15, arrival tolerance 15, no-show grace 60. A door with no declared hours is open 24x7.
