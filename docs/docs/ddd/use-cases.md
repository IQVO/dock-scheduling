---
id: use-cases
title: Use Cases
sidebar_label: Use Cases
description: "One section per Dock Scheduling command use case, with its trigger, inbound adapter, preconditions, main flow, errors mapped to HTTP problems, events and idempotency."
---

# Use Cases

One section per command use case of the `Service` in `internal/application/usecases`
(16 commands: eight on `DockAppointment`, four on `DockCalendar`, four on `Carrier`). Source: `book.go`, `change.go`,
`calendar.go`, `facts.go`, `yard.go`, `carriers.go`. The runtime sequence of each is on
[Sequence Diagrams](/docs/ddd/sequence-diagrams). Reads
(`GET /appointments`, `GET /appointments/{appointmentId}`,
`GET /doors/{doorCode}/availability`, `GET /sites/{siteId}/dock-calendar`,
`GET /reports/schedule-adherence`, `GET /reports/freshness`) are queries, not commands: the five query methods of the `Service` (`queries.go`) serve the first four, and the two report
routes are served by the separate `reports` binary from the analytical database.

## Conventions for every use case

- **One transaction.** The aggregate change, its outbox rows and (for a
  Kafka-driven use case) the inbox row commit together.
- **Errors** are `application/problem+json` with `type = https://errors.dock-scheduling.warehouse-systems.dev/<slug>`
  (`internal/adapters/inbound/http/errors.go`, `problemCatalogue`). The status follows decision D-10 of [ADR 0002](/docs/adr/0002-aggregates-invariants-and-assumptions):
  409 for a conflict with state, 422 for a value or reference rule, 404 for an unknown resource. The slug is the kebab-case of the
  `Err` name, except the two 404s of an unknown appointment (`appointment-not-found`) or door (`door-not-found`). The use-case sections below
  name the `Err...` variables; this table is the complete catalogue.

| Error | HTTP | Problem slug |
| --- | --- | --- |
| `ports.ErrNotFound` (appointment) | 404 | `appointment-not-found` |
| `ErrDoorNotFound` | 404 | `door-not-found` |
| `ErrUnknownClosure` | 404 | `unknown-closure` |
| `ErrPreconditionFailed` (`If-Match` or `expectedVersion` stale) | 412 | `precondition-failed` |
| `ErrIllegalTransition` | 409 | `illegal-transition` |
| `ErrConcurrentUpdate` | 409 | `concurrent-update` |
| `ErrDoorDoubleBooked`, `ErrDoorUnavailable`, `ErrOverlappingClosure`, `ErrClosureConflictsWithAppointments` | 409 | `door-double-booked`, `door-unavailable`, `overlapping-closure`, `closure-conflicts-with-appointments` |
| `ErrAsnAlreadyScheduled`, `ErrHoursConflictWithAppointments`, `ErrNoShowTooEarly`, `ErrClosureAlreadyLifted` | 409 | `asn-already-scheduled`, `hours-conflict-with-appointments`, `no-show-too-early`, `closure-already-lifted` |
| `ErrInvalidDoorCode`, `ErrInvalidSiteID`, `ErrInvalidCarrier`, `ErrInvalidDirection` | 422 | `invalid-door-code`, `invalid-site-id`, `invalid-carrier`, `invalid-direction` |
| `ErrInvalidWindow`, `ErrWindowInPast`, `ErrUnknownDoor`, `ErrDoorFlowMismatch` | 422 | `invalid-window`, `window-in-past`, `unknown-door`, `door-flow-mismatch` |
| `ErrUnknownAsn`, `ErrMissingOutboundRef`, `ErrDuplicateAsn`, `ErrInvalidRefs` | 422 | `unknown-asn`, `missing-outbound-ref`, `duplicate-asn`, `invalid-refs` |
| `ErrCarrierSuspended`, `ErrCarrierAlreadyRegistered`, `ErrCarrierAlreadySuspended`, `ErrCarrierNotSuspended` (ADR 0012) | 409 | `carrier-suspended`, `carrier-already-registered`, `carrier-already-suspended`, `carrier-not-suspended` |
| `ErrCarrierNotFound` (ADR 0012) | 404 | `carrier-not-found` |
| `ErrInvalidScac`, `ErrInvalidUSDOT`, `ErrInvalidMC`, `ErrInvalidCarrierName`, `ErrInvalidSuspensionReason` (ADR 0012) | 422 | `invalid-scac`, `invalid-usdot`, `invalid-mc`, `invalid-carrier-name`, `invalid-suspension-reason` |
| `ErrInvalidReason`, `ErrInvalidEta`, `ErrInvalidOperatingHours`, `ErrInvalidSettings`, `ErrInvalidClosure` | 422 | `invalid-reason`, `invalid-eta`, `invalid-operating-hours`, `invalid-settings`, `invalid-closure` |
| Adapter-level | 400 | `idempotency-key-required`, `idempotency-key-invalid`, `malformed-request-body`, `unknown-query-parameter`, `invalid-limit`, `invalid-cursor`, `invalid-availability-query`, `invalid-query-parameter`, `invalid-path-parameter`, `invalid-if-match`, `invalid-report-range` |
| Adapter-level | 409 | `idempotency-key-reused` |
| Readiness | 503 | `dependency-unavailable` |
| Anything unmapped | 500 | `internal-error` (the detail is generic; the cause is only in the log) |

- **Idempotency.** Every state-changing `POST` and `PUT` takes an
  `Idempotency-Key` header; a replay with the same key returns the first
  response. Every Kafka consumer is idempotent on the CloudEvents `id`
  (inbox). Mutable aggregates carry a `version` exposed as an ETag and
  checked with `If-Match` (`ErrConcurrentUpdate` on a lost race, `412` on an `If-Match` mismatch). The ETag is the
  weak `W/"<version>"`, `If-Match` is honoured on reschedule and cancel and is optional (no `428`),
  creating `POST`s require the `Idempotency-Key` and a replay answers byte-identically with
  `Idempotency-Replayed: true`; `PUT` is idempotent by method.
- **Kafka-driven use cases** have no HTTP status. A fact the policies deliberately do not apply is counted
  (`dock_scheduling_ignored_facts_total`) and the event is marked processed; an infrastructure failure rolls back and the
  message is retried.
- **Order of checks** in a booking (`book.go`): the per-door advisory lock is taken first (so a closure or booking committed by
  the previous holder is visible), then the door is looked up and its site calendar loaded, then the aggregate factory
  validates the value objects (carrier, window, start not in the past), then `checkNew` runs the door's flow, the
  calendar (hours and closures), the references (ASNs, cutoff) and ASN uniqueness, and the overlap check last.

- **Request and response bodies** are in `apis/openapi.yaml` (see the [API reference](/docs/api-reference)); the fields named below come
  from the event payloads and value objects.

## BookAppointment

- **Actor / trigger:** carrier scheduler (carrier portal).
- **Inbound adapter:** REST `POST /appointments`.
- **Preconditions:** none on the aggregate; the door, ASN and CPT copies
  must already hold the facts the booking cites.
- **Main flow:** (all in one unit of work)
  1. Validate the door code (`ErrInvalidDoorCode`), take the **per-door advisory lock**, look the door up in `DoorView`: a known,
     active dock slot (`ErrUnknownDoor`); the door's zone gives the `site_id`. Load the site's `DockCalendar` (defaults when none is stored).
  2. `dockappointment.Book` validates the value objects: carrier (`ErrInvalidCarrier`), window on the site's slot grid and at most
     `max_window_minutes` (`ErrInvalidWindow`), start not before now (`ErrWindowInPast`), distinct ASNs (`ErrDuplicateAsn`),
     references that fit the direction (`ErrInvalidRefs`).
  3. `checkNew`: the direction against the door's `dockFlow` (`ErrDoorFlowMismatch`); the window inside the door's operating hours and
     outside its closures (`ErrDoorUnavailable`); `INBOUND`: every ASN is in `AsnView` and not cancelled (`ErrUnknownAsn`) and sits on no other
     active appointment (`ErrAsnAlreadyScheduled`, the detail lists the ids); `OUTBOUND`: the `cpt_id` is in `CutoffView`, or at least one
     `shipment_ref` is given (`ErrMissingOutboundRef`).
  4. `DoorSchedule.CheckNoOverlap` against the door's `BOOKED` and `CHECKED_IN` appointments (`ErrDoorDoubleBooked`).
  4a. When the booking names a `carrier_scac` (ADR 0012, before `checkNew`): a registered `SUSPENDED` carrier is refused
     (`ErrCarrierSuspended`); an `ACTIVE` carrier and a SCAC that is not in the registry are accepted as before; a booking with no SCAC never reads the registry.
  5. Save the appointment in `BOOKED` and enqueue `DockAppointmentBooked` in the same transaction; return the appointment with its ETag.
- **Errors:** `ErrInvalidDoorCode`, `ErrUnknownDoor`, `ErrInvalidCarrier`, `ErrInvalidWindow`, `ErrWindowInPast`, `ErrDuplicateAsn`,
  `ErrInvalidRefs`, `ErrDoorFlowMismatch`, `ErrDoorUnavailable`, `ErrUnknownAsn`, `ErrAsnAlreadyScheduled`, `ErrMissingOutboundRef`,
  `ErrDoorDoubleBooked`, `ErrCarrierSuspended`.
- **Events emitted:** `DockAppointmentBooked`.
- **Idempotency:** `Idempotency-Key`; the overlap check and the insert run
  under the same per-door lock.

## RescheduleAppointment

- **Actor / trigger:** carrier scheduler.
- **Inbound adapter:** REST `POST /appointments/{appointmentId}/reschedules`,
  `If-Match` with the appointment `version`.
- **Preconditions:** the appointment is `BOOKED`.
- **Main flow:**
  1. Load the appointment under a row lock; refuse a stale `If-Match` / `expectedVersion` (`ErrPreconditionFailed`, 412).
  2. Take the per-door lock of the target door (the current door when none is given), which must belong to the appointment's site
     (`ErrUnknownDoor`); refuse if the appointment is not `BOOKED` (`ErrIllegalTransition`). A reschedule may change the door and the window (decision D-4).
  2a. The carrier registry rule of `BookAppointment` applies (ADR 0012): a registered `SUSPENDED` carrier cannot move an appointment (`ErrCarrierSuspended`).
  3. Run the same door-flow, hours and closures checks as `BookAppointment`, then `DoorSchedule.CheckNoOverlap` for the new
     window, ignoring this appointment (`ErrDoorDoubleBooked`). The ASN checks are not repeated.
  4. Keep the state `BOOKED`; store the new window and the previous one;
     save with the version check and write `DockAppointmentRescheduled`
     (`previous_window_start`, `previous_window_end`, `previous_door_code`,
     `arrival_tolerance_minutes`), one transaction.
- **Errors:** `ErrNotFound`, `ErrPreconditionFailed`, `ErrIllegalTransition`, `ErrConcurrentUpdate`, `ErrUnknownDoor`,
  `ErrInvalidWindow`, `ErrWindowInPast`, `ErrDoorFlowMismatch`,
  `ErrDoorUnavailable`, `ErrDoorDoubleBooked`, `ErrCarrierSuspended`.
- **Events emitted:** `DockAppointmentRescheduled`.
- **Idempotency:** `Idempotency-Key` and `If-Match`.

## CancelAppointment

- **Actor / trigger:** carrier scheduler; policy `CancelWhenAllAsnsCancelled`
  on `ASNCancelled` (reason `ASN_CANCELLED`); policy `CancelOnVisitRejected` on `VisitRejected`
  (reason `TURNED_AWAY`). The REST route accepts `CARRIER_REQUEST` and `SITE_REQUEST`, the latter
  for the dock planner (decisions D-5, X2).
- **Inbound adapter:** REST `POST /appointments/{appointmentId}/cancellations`
  with a `CancellationReason`; Kafka consumer of
  `warehouse.inbound-receiving.events` for the policy.
- **Preconditions:** the appointment is `BOOKED` or `CHECKED_IN`.
- **Main flow:**
  1. Load the appointment; refuse if not `BOOKED` or `CHECKED_IN`
     (`ErrIllegalTransition`).
  2. Transition to `CANCELLED` with the reason.
  3. Save with the version check and write `DockAppointmentCancelled`
     (`reason`), one transaction.
  4. Policy path: for each `INBOUND` `BOOKED` appointment of the cancelled ASN
     whose ASNs are now all cancelled, run steps 2 and 3.
- **Errors:** `ErrNotFound`, `ErrIllegalTransition`, `ErrConcurrentUpdate`,
  `ErrInvalidReason` (a reason outside the list).
- **Events emitted:** `DockAppointmentCancelled`.
- **Idempotency:** `Idempotency-Key` and `If-Match`; inbox on the Kafka path.

## ReportEta

- **Actor / trigger:** carrier scheduler (later, possibly a
  `transport-visibility` feed).
- **Inbound adapter:** REST `POST /appointments/{appointmentId}/eta-reports`.
- **Preconditions:** the appointment is `BOOKED` (any other state is `ErrIllegalTransition`).
- **Main flow:**
  1. Load the appointment with its `version`.
  2. Store `Eta` (`eta_at`, `reported_at`, `source`, default `api`, at most 64 characters).
  3. If `eta_at` is later than `window_start` plus the arrival tolerance,
     flag the appointment `LATE_ETA` and write `DockAppointmentAtRisk` (policy `EtaRisk`),
     once per ETA report. An ETA inside the tolerance writes no event and clears an earlier `LATE_ETA` flag.
  4. Save with the version check, one transaction. Nothing is rescheduled.
- **Errors:** `ErrNotFound` (404 `appointment-not-found`), `ErrIllegalTransition`, `ErrConcurrentUpdate`,
  `ErrInvalidEta`.
- **Events emitted:** `DockAppointmentAtRisk` when the ETA is late; none
  otherwise.
- **Idempotency:** `Idempotency-Key`; one `AtRisk` per ETA report.

## RecordArrival

- **Actor / trigger:** policy `ArrivalFromGate` on `VisitCheckedIn` naming an
  `appointment_id`. There is no REST route: arrival is a physical fact owned
  by `yard-management`.
- **Inbound adapter:** Kafka consumer of `warehouse.yard-management.events`
  (type `com.warehouse.wms.yard-management.yardvisit.VisitCheckedIn`).
- **Preconditions:** the appointment is `BOOKED`.
- **Main flow:**
  1. Insert the CloudEvents `id` into the inbox; if present, stop.
  2. Load the appointment. Unknown or terminal (`COMPLETED`, `CANCELLED`,
     `NO_SHOW`): ignore and count. A late `VisitCheckedIn` for a `COMPLETED`
     appointment is also ignored.
  3. Compute `ArrivalStatus` from `arrived_at`, the window and the site's
     arrival tolerance (EARLY before `window_start` minus the tolerance, LATE after `window_start` plus the
     tolerance, else ON_TIME; decision X6).
  4. Transition `BOOKED` to `CHECKED_IN`; save with the version check and
     write `DockAppointmentCheckedIn` (`checked_in_at`, `arrival_status`,
     `visit_id`) to the analytics topic, one transaction.
- **Errors:** none returned; refusals are counted. An appointment already
  `CHECKED_IN` is left unchanged and counted.
- **Events emitted:** `DockAppointmentCheckedIn` (analytics only).
- **Idempotency:** inbox on the CloudEvents `id`.

## ReassignDoor

- **Actor / trigger:** policy `ActualDoorWins` on `TrailerDocked` whose
  `door_code` differs from the booked door. No REST route.
- **Inbound adapter:** Kafka consumer of `warehouse.yard-management.events`
  (type `com.warehouse.wms.yard-management.trailer.TrailerDocked`).
- **Preconditions:** the appointment is active: `BOOKED` and `CHECKED_IN` accept
  the command.
- **Main flow:**
  1. Inbox check on the CloudEvents `id`.
  2. Load the appointment by `appointment_id`; if the door equals the booked
     door, stop.
  3. Change the door **without** the overlap refusal: the physical fact wins.
  4. Find later appointments that now overlap on the new door; each gets
     `DockAppointmentAtRisk` with cause `DOOR_REASSIGNED_OVERLAP`.
  5. Save with the version check and write `DockAppointmentDoorReassigned`
     (`from_door_code`, `to_door_code`), one transaction.
- **Errors:** none returned; a `CANCELLED` or `NO_SHOW` appointment is
  ignored and counted.
- **Events emitted:** `DockAppointmentDoorReassigned` (integration and analytics, service
  ADR 0010: `inbound-receiving` reads it) and, per overlapped appointment,
  `DockAppointmentAtRisk` (analytics only).
- **Idempotency:** inbox; a second `TrailerDocked` for the same door changes
  nothing.

## RecordDoorRelease

- **Actor / trigger:** policy `CompleteOnDoorRelease` on `TrailerUndocked`
  naming an `appointment_id`. No REST route.
- **Inbound adapter:** Kafka consumer of `warehouse.yard-management.events`
  (type `com.warehouse.wms.yard-management.trailer.TrailerUndocked`).
- **Preconditions:** the appointment is `CHECKED_IN`, or `BOOKED` (policy
  `OutOfOrderYardFacts`).
- **Main flow:**
  1. Inbox check on the CloudEvents `id`.
  2. Load the appointment. `CANCELLED` or `NO_SHOW`: ignore and count.
  3. From `BOOKED`, record the arrival as implicit and complete it
     (`BOOKED` to `COMPLETED`); from `CHECKED_IN`, complete it.
  4. Save with the version check and write `DockAppointmentCompleted`
     (`completed_at`), one transaction.
- **Errors:** none returned.
- **Events emitted:** `DockAppointmentCompleted` (analytics only). No
  `DockAppointmentCheckedIn` is raised for an implicit arrival; the adherence report counts it as
  `ON_TIME` and the metric `dock_scheduling_implicit_arrivals_total` counts it.
- **Idempotency:** inbox; an appointment already `COMPLETED` is not completed
  twice. One appointment covers exactly one trailer (decision X3).

## MarkNoShow

- **Actor / trigger:** scheduler (cron), policy `NoShowSweep`.
- **Inbound adapter:** REST `POST /no-show-sweeps`.
- **Preconditions:** none; the sweep selects what qualifies.
- **Main flow:**
  1. Select `BOOKED` appointments with `window_end` plus the site's no-show
     grace before now.
     At most 500 candidates are loaded per pass (`sweepBatch`).
  2. For each, under a row lock, transition `BOOKED` to `NO_SHOW`, save with the version check
     and write `DockAppointmentNoShow`, one transaction per appointment. An appointment whose
     grace is not over (`ErrNoShowTooEarly`) or that moved on since the listing (`ErrIllegalTransition`) is skipped silently.
  3. Return `{count, appointmentIds, ranAt}`. Any other failure on one appointment does not stop the pass; the first such
     error is returned after the pass (a 5xx the CronJob retries).
- **Errors:** none for a normal pass; a store failure is the first error of the pass.
- **Events emitted:** `DockAppointmentNoShow`, one per appointment.
- **Idempotency:** `Idempotency-Key`; a rerun marks only what still
  qualifies, so it is idempotent by construction.

## DeclareOperatingHours

- **Actor / trigger:** dock planner.
- **Inbound adapter:** REST `PUT /sites/{siteId}/dock-calendar/operating-hours`.
- **Preconditions:** none.
- **Main flow:**
  1. Validate every row: `opens_at < closes_at` (`ErrInvalidOperatingHours`).
  2. Store the hours for the door, or as the site default, with weekday and
     time zone; `PUT` replaces the whole set of rows of the site (assumption A7: a door with rows of
     its own uses only those, a door with none uses the site default rows, no rows at all means open
     24x7; at most one row per door and weekday; `closes_at` may be `24:00`).
  3. Save with the version check and write `DoorOperatingHoursDeclared`
     (`site_id`, `door_code`, `operating_hours`, `version`), one transaction.
- **Errors:** `ErrInvalidOperatingHours`, `ErrConcurrentUpdate`,
  `ErrHoursConflictWithAppointments` (409, lists the ids) when an active appointment
  would fall outside the new hours (decision D-8).
- **Events emitted:** `DoorOperatingHoursDeclared` (analytics only).
- **Idempotency:** `Idempotency-Key`; `PUT` with the same body is a no-op.

## DeclareCalendarSettings

- **Actor / trigger:** dock planner.
- **Inbound adapter:** REST `PUT /sites/{siteId}/dock-calendar/settings`.
- **Preconditions:** none.
- **Main flow:**
  1. Validate `max_window_minutes`, `slot_granularity_minutes`,
     `arrival_tolerance_minutes` and `no_show_grace_minutes` (`ErrInvalidSettings`;
     the defaults are 240, 15, 15 and 60; `max_window_minutes` can only lower the 4 h hard cap; the
     window grid is whole minutes since the Unix epoch).
  2. Store the settings and save with the version check.
- **Errors:** `ErrInvalidSettings`, `ErrConcurrentUpdate`.
- **Events emitted:** none; the new tolerance reaches `yard-management` on the next `Booked` or
  `Rescheduled` event (`arrival_tolerance_minutes`, decision X1).
- **Idempotency:** `Idempotency-Key`; `PUT` with the same body is a no-op.

## ScheduleDoorClosure

- **Actor / trigger:** dock planner.
- **Inbound adapter:** REST `POST /sites/{siteId}/dock-calendar/closures`.
- **Preconditions:** none on the calendar.
- **Main flow:**
  1. Validate the closure (`door_code`, `from`, `to`, `reason`).
  2. Refuse if it overlaps another closure of the same door
     (`ErrOverlappingClosure`).
  3. Refuse if it covers an active appointment of the door; the refusal lists
     the appointment ids (`ErrClosureConflictsWithAppointments`).
  4. Add the closure with a new `closure_id`; save with the version check and
     write `DoorClosureScheduled`, one transaction.
- **Errors:** `ErrOverlappingClosure`, `ErrClosureConflictsWithAppointments`,
  `ErrInvalidClosure`, `ErrUnknownDoor`, `ErrConcurrentUpdate`.
- **Events emitted:** `DoorClosureScheduled` (consumed by
  `yard-management`).
- **Idempotency:** `Idempotency-Key`. The site's calendar row is locked (`GetForUpdate`) and the door's advisory lock is taken before
  the active appointments are read, so a booking on that door cannot slip in between the check and the commit.

## LiftDoorClosure

- **Actor / trigger:** dock planner.
- **Inbound adapter:** REST
  `POST /sites/{siteId}/dock-calendar/closures/{closureId}/lifts`.
- **Preconditions:** the closure exists on the site's calendar.
- **Main flow:**
  1. Lock the site's calendar and find the closure by `closure_id` (`ErrUnknownClosure`, 404, if absent).
  2. Mark it lifted (`ErrClosureAlreadyLifted`, 409, if it already is); save with the version check and write `DoorClosureLifted`,
     one transaction.
- **Errors:** `ErrUnknownClosure`, `ErrClosureAlreadyLifted`, `ErrConcurrentUpdate`.
- **Events emitted:** `DoorClosureLifted` (consumed by `yard-management`).
- **Idempotency:** `Idempotency-Key`; a replay returns the first response.

## RegisterCarrier

- **Actor / trigger:** dock planner or compliance process (an agent through MCP `register_carrier`).
- **Inbound adapter:** REST `PUT /carriers/{scac}`; MCP `register_carrier`.
- **Preconditions:** none. The carrier registry is opt-in ([ADR 0012](/docs/adr/0012-carrier-registry)).
- **Main flow:** (one unit of work)
  1. Validate the SCAC (`ErrInvalidScac`, 2..4 upper-case letters) and the identity: name (`ErrInvalidCarrierName`), optional USDOT
     (`ErrInvalidUSDOT`), optional MC or MX (`ErrInvalidMC`, normalised to `MC<digits>` or `MX<digits>`).
  2. Lock the carrier row (`GetForUpdate`). Absent: `carrier.Register` creates it `ACTIVE` at version 1, save and enqueue
     `CarrierRegistered` (REST `201`). Present with exactly the same data: a no-op (`200`, nothing written). Present with other data:
     `ErrCarrierAlreadyRegistered`.
- **Errors:** `ErrInvalidScac`, `ErrInvalidCarrierName`, `ErrInvalidUSDOT`, `ErrInvalidMC`, `ErrCarrierAlreadyRegistered`.
- **Events emitted:** `CarrierRegistered` (first registration only).
- **Idempotency:** idempotent by SCAC and by method; MCP takes an `idempotencyKey`.

## UpdateCarrier

- **Actor / trigger:** dock planner or compliance process.
- **Inbound adapter:** REST `PATCH /carriers/{scac}` (a JSON merge patch; `Idempotency-Key` required, `If-Match` honoured). Not an MCP tool.
- **Main flow:** lock and load the carrier (`ErrCarrierNotFound`), compare the version with `If-Match` (`ErrPreconditionFailed`), apply the
  named fields (`null` or `""` clears USDOT or MC); a change that leaves the data as it is writes nothing.
- **Errors:** `ErrCarrierNotFound`, `ErrPreconditionFailed`, `ErrInvalidCarrierName`, `ErrInvalidUSDOT`, `ErrInvalidMC`.
- **Events emitted:** `CarrierRegistered` with the new full snapshot and version.

## SuspendCarrier

- **Actor / trigger:** dock planner or compliance process (MCP `suspend_carrier`).
- **Inbound adapter:** REST `POST /carriers/{scac}/suspensions`; MCP `suspend_carrier`.
- **Main flow:** lock and load the carrier, check `If-Match`, `carrier.Suspend(reason)` (`ErrInvalidSuspensionReason` for a blank or over
  200 character reason, `ErrCarrierAlreadySuspended`), save and enqueue `CarrierSuspended`. From now on `BookAppointment` and
  `RescheduleAppointment` refuse the SCAC with `ErrCarrierSuspended`. Appointments the carrier already holds are not touched.
- **Events emitted:** `CarrierSuspended`.
- **Idempotency:** `Idempotency-Key`.

## ReinstateCarrier

- **Actor / trigger:** dock planner or compliance process (MCP `reinstate_carrier`).
- **Inbound adapter:** REST `POST /carriers/{scac}/reinstatements`; MCP `reinstate_carrier`.
- **Main flow:** lock and load the carrier, check `If-Match`, `carrier.Reinstate` (`ErrCarrierNotSuspended`), save and enqueue `CarrierReinstated`.
- **Events emitted:** `CarrierReinstated`.
- **Idempotency:** `Idempotency-Key`.
