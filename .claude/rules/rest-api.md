---
paths:
  - "internal/adapters/inbound/http/**"
  - "apis/openapi*.yaml"
  - "apis/openapi/**"
---

# REST API (inbound adapter)

`internal/adapters/inbound/http` serves every route of `apis/openapi.yaml` on a chi router (`NewRouter` in `server.go`). Each handler
decodes, calls ONE method of `usecases.Service` and maps the view to a DTO (`dto.go`); it never touches a domain aggregate.

- `POST /appointments` -> `BookAppointment` (`appointments.go`; 201, `Location`, `ETag`)
- `GET /appointments` -> `ListAppointments` (keyset `limit`/`cursor`, filters siteId, doorCode, state, direction, carrierScac, atRisk, from, to)
- `GET /appointments/{appointmentId}` -> `GetAppointment` (`ETag` = `W/"<version>"`; exposes `atRisk` and `atRiskCause`, D-6)
- `POST /appointments/{appointmentId}/reschedules` -> `RescheduleAppointment` (carrier or dock planner, D-5; `If-Match`, 412 on mismatch)
- `POST /appointments/{appointmentId}/cancellations` -> `CancelAppointment` (CARRIER_REQUEST or SITE_REQUEST only; `If-Match`)
- `POST /appointments/{appointmentId}/eta-reports` -> `ReportEta`
- `GET /doors/{doorCode}/availability` -> `GetDoorAvailability`
- `GET /sites/{siteId}/dock-calendar` -> `GetDockCalendar` (`calendar.go`)
- `PUT /sites/{siteId}/dock-calendar/operating-hours` -> `DeclareOperatingHours`
- `PUT /sites/{siteId}/dock-calendar/settings` -> `DeclareCalendarSettings`
- `POST /sites/{siteId}/dock-calendar/closures` -> `ScheduleDoorClosure`
- `POST /sites/{siteId}/dock-calendar/closures/{closureId}/lifts` -> `LiftDoorClosure`
- `POST /no-show-sweeps` -> `RunNoShowSweep` (cron; one transaction per appointment, so the key middleware runs it DETACHED)
- The reports are NOT routes of this router: `GET /reports/schedule-adherence` and `GET /reports/freshness` are served by the separate `cmd/reports` binary over the analytical database, specified in `apis/openapi-reports.yaml` (ADR 0006; the shared handlers live in `reports.go` / `reports_router.go`).
- `GET /healthz` (liveness), `GET /readyz` (`readiness.go`: drain gate + Postgres ping + Kafka metadata), `GET /metrics` (Prometheus, built in `cmd/api`)

Arrival and door release are NOT REST routes: they are yard facts the Kafka consumers apply (ADR 0003).

## Conventions

- Error shape: RFC 7807 `application/problem+json`, `type = https://errors.dock-scheduling.warehouse-systems.dev/<slug>` (`errors.go`
  holds the whole catalogue). Decision D-10: 409 for conflicts and illegal states, 422 for value-object and reference rules, the slug is
  the kebab-case of the `Err` name without the prefix; the adapter's own refusals are 400 (malformed body, unknown or invalid query or
  path parameter, `idempotency-key-*`), 404 (`appointment-not-found`, `door-not-found`, `unknown-closure`, `not-found`), 412
  (`precondition-failed`). An unmapped error is a 500 `internal-error` whose detail never leaks the cause.
- Idempotency: every POST needs an `Idempotency-Key` (`idempotency.go`). Every answer below 500 is stored (4xx too) and replays with
  `Idempotency-Replayed: true`; the same key with another method, path or body is 409 `idempotency-key-reused`. State-changing POSTs run in
  ONE unit of work with the key row; the sweep runs detached. PUTs are idempotent by method.
- `rejectNUL` (`nul.go`) refuses a NUL in the path (404), the query or a body (400) before the key middleware records anything;
  `rejectBadQuery` refuses a query string that does not percent-decode.
- Pagination: `page/` is the fleet keyset package copied verbatim; `limit` 1..500 (default 100), an undeclared query parameter is 400.
- Auth: none. Fleet-wide REST+MCP auth was deliberately reverted 2026-09-11; `internal/architecture/fitness_test.go`'s
  `TestNoAuthMiddlewareReintroduced` keeps it that way until a fresh decision.
- A file in this package holds at most 300 lines of code (the `file-length` CI job); split by resource, never raise the limit.
