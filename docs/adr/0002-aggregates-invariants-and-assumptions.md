# 2. Aggregates, invariants, error names and assumptions

Status: Accepted. Source of truth: the yard model (`yard/model/model.yaml`, context `dock-scheduling`) plus the orchestrator's final
cross-context decisions X1-X7 and dock-scheduling decisions D-1..D-13 (they override the model wording where they differ). Where
both are silent the numbered assumptions below decide.

## Aggregates

### DockAppointment (package `internal/domain/dockappointment`, root id `appointmentId`, `appt-<uuid>`)

One booked door window for one carrier visit and ONE trailer (X3). States `BOOKED`, `CHECKED_IN`, `COMPLETED`, `CANCELLED`, `NO_SHOW`.

| From | To | Command | Raised event |
|---|---|---|---|
| (new) | BOOKED | BookAppointment, ImportLegacyAppointment | DockAppointmentBooked |
| BOOKED | BOOKED | RescheduleAppointment (window and/or door) | DockAppointmentRescheduled |
| BOOKED | CHECKED_IN | RecordArrival | DockAppointmentCheckedIn (analytics) |
| CHECKED_IN | COMPLETED | RecordDoorRelease | DockAppointmentCompleted (analytics) |
| BOOKED | COMPLETED | RecordDoorRelease (arrival implicit) | DockAppointmentCompleted (analytics) |
| BOOKED, CHECKED_IN | CANCELLED | CancelAppointment | DockAppointmentCancelled |
| BOOKED | NO_SHOW | MarkNoShow | DockAppointmentNoShow |
| BOOKED, CHECKED_IN | same | ReassignDoor | DockAppointmentDoorReassigned (analytics) |
| BOOKED | BOOKED | ReportEta | DockAppointmentAtRisk (analytics, only when late) |

`COMPLETED`, `CANCELLED` and `NO_SHOW` are terminal (D-9: a late carrier books anew). `version` starts at 1 and grows by one per
accepted change; it is the ETag and the `version` of every event except `DockAppointmentAtRisk`. Active = `BOOKED` or `CHECKED_IN`.

Value objects: `Direction`, `Window` (half-open `[start, end)`, UTC), `Carrier{name, scac?}`, `InboundRefs{asn_numbers[]}`,
`OutboundRefs{cpt_id?, shipment_refs[]}`, `ArrivalStatus` (EARLY|ON_TIME|LATE), `Eta{eta_at, reported_at, source}`,
`ExpectedSeal{seal_number?}`, `AtRisk{cause}` (D-6: the aggregate exposes `at_risk` and `at_risk_cause`).

Invariants. Each is a sentinel error in the package that raises it; the HTTP status follows D-10 and the problem slug is the
kebab-case of the error name without `Err` (`ErrDoorDoubleBooked` -> `door-double-booked`):

| Rule | Error | HTTP | Where |
|---|---|---|---|
| door code 1..64 characters, no whitespace, control characters or `/` | ErrInvalidDoorCode | 422 | domain |
| carrier name non-blank, <= 100 characters; scac 2..4 upper-case letters when given | ErrInvalidCarrier | 422 | domain |
| end > start, end - start <= the site's max window (default and hard cap 4 h), both multiples of the slot granularity (D-11) | ErrInvalidWindow | 422 | domain |
| a booked or rescheduled window does not start before now | ErrWindowInPast | 422 | domain |
| the door is a known, active Dock slot of the site | ErrUnknownDoor | 422 | `DoorSchedule.CheckDoor` over the DoorView |
| the door's dockFlow allows the direction | ErrDoorFlowMismatch | 422 | `DoorSchedule.CheckDoor` |
| no overlap with another BOOKED or CHECKED_IN appointment on the same door | ErrDoorDoubleBooked | 409 | `DoorSchedule.CheckNoOverlap` under a per-door advisory lock, plus an exclusion constraint |
| the window is inside the door's operating hours and outside its closures | ErrDoorUnavailable | 409 | `DockCalendar.CheckWindow` |
| INBOUND covers >= 1 ASN, each known and not cancelled in the local ASN copy | ErrUnknownAsn | 422 | `References.Check` |
| an ASN sits on only ONE active appointment (D-12) | ErrAsnAlreadyScheduled | 409 | application, in the booking transaction (unique partial index backstop) |
| OUTBOUND names a cpt_id known in the local CPT copy or >= 1 shipment_ref | ErrMissingOutboundRef | 422 | `References.Check` |
| the command is legal in the current state | ErrIllegalTransition | 409 | domain |
| the version the caller loaded is still current | ErrConcurrentUpdate | 409 | repository (optimistic version), `If-Match` mismatch is 412 |

Also in the aggregate: `ErrInvalidAppointmentID`, `ErrInvalidDirection`, `ErrDuplicateAsn`, `ErrInvalidRefs`, `ErrInvalidState`,
`ErrInvalidVersion`, `ErrNoShowTooEarly` (409), `ErrInvalidReason`, `ErrInvalidEta` (all 422 unless noted).

### DockCalendar (package `internal/domain/dockcalendar`, root id `siteId` = facility-layout `siteCode`)

The per-site settings every booking is checked against. Single state `ACTIVE`. Created lazily with the defaults (D-1). Value objects
`OperatingHours{door_code or site default, weekday, opens_at, closes_at, timezone}`, `Closure{closure_id, door_code, from, to, reason}`,
`Settings{max_window_minutes 240, slot_granularity_minutes 15, arrival_tolerance_minutes 15, no_show_grace_minutes 60}`.

| Rule | Error | HTTP |
|---|---|---|
| opens_at < closes_at for every operating-hours row | ErrInvalidOperatingHours | 422 |
| closures do not overlap on one door | ErrOverlappingClosure | 409 |
| a closure may not cover an active appointment of that door (the refusal lists the appointment ids) | ErrClosureConflictsWithAppointments | 409 |
| new operating hours may not leave an active appointment of a door outside them (D-8, lists the ids) | ErrHoursConflictWithAppointments | 409 |

Also: `ErrInvalidSettings` (422), `ErrInvalidClosure` (422), `ErrUnknownClosure` (404), `ErrClosureAlreadyLifted` (409), `ErrInvalidSiteID` (422).

## Policies (application layer, one use case each)

ArrivalFromGate, ActualDoorWins, CompleteOnDoorRelease, OutOfOrderYardFacts, CancelOnVisitRejected (X2), NoShowSweep, EtaRisk,
CancelWhenAllAsnsCancelled, LegacyImport (stage A). PickupFromCutoffs is phase 2 (D-3): CPTScheduleChanged is consumed and stored
(CutoffView), no appointment is created from it and there is no PickupDoorRule.

## Assumptions

- **A1** AsyncAPI is written as 2.6.0, the version of every sibling contract (the catalogue test, the orphan-consumer policy and the
  Spectral ruleset `.spectral.asyncapi.yaml` are built for it) instead of 3.0. One message per published event; consumed messages as
  `publish` operations on the producers' channels (the 2.x way to say "we receive").
- **A2** The site of a door is `ZoneRegistered(zoneId).siteCode` joined to `LocationSlotRegistered(zoneId)` at use time (X5). Door codes
  are globally unique, REST door paths use the door code only, and a booking names the door; the site is derived. A door whose zone has
  not arrived is held pending and cannot be booked (ErrUnknownDoor) until it does.
- **A3** A door is bookable when its local copy exists with `role = Dock`; a decommission deletes the row, blocks NEW bookings and
  leaves existing appointments untouched (D-7).
- **A4** RescheduleAppointment may change the door and the window (D-4); the same checks as a booking apply to the new door and window,
  ignoring the appointment's own current window. The Rescheduled event carries `previous_door_code` (X1).
- **A5** ArrivalStatus (X6): EARLY when `arrival < window_start - tolerance`, LATE when `arrival > window_start + tolerance`, else
  ON_TIME. An implicit arrival (door release on a BOOKED appointment) completes without a CheckedIn event and enters the adherence
  report as ON_TIME, counted in the metric `dock_scheduling_implicit_arrivals_total`. A legacy CheckedIn is classified with the
  site's tolerance like any gate arrival.
- **A6** ReportEta is accepted on a BOOKED appointment, bumps the version, stores the latest ETA and raises DockAppointmentAtRisk
  (cause LATE_ETA) once per report when `eta_at > window_start + arrival_tolerance`; the aggregate then reports `at_risk = true`. A
  later on-time ETA report or any reschedule clears the flag. DOOR_REASSIGNED_OVERLAP is raised by ActualDoorWins for each later
  BOOKED appointment of the new door that now overlaps the physical occupant's window (the flag is set on those appointments).
- **A7** Operating hours: PUT replaces the whole set of rows of the site. A door with rows of its own uses only those; a door without
  rows uses the site-default rows (door_code null); no rows at all means 24x7 (D-1). Once a row applies, a weekday with no row is
  closed. At most one row per (door, weekday). `closes_at` may be `24:00`. A window may span local days and must be covered by the
  union of the day intervals (wall-clock construction, DST safe). A declaration that would leave an active appointment outside the
  hours is refused (D-8).
- **A8** `slot_granularity_minutes` forces window start and end onto a grid of whole minutes since the Unix epoch (seconds zero);
  `max_window_minutes` lowers the 4 h hard cap per site, never raises it; there is no booking horizon (D-11). The availability answer
  is cut on the same grid.
- **A9** A closure always names a door (`door_code` is not null in DoorClosureScheduled).
- **A10** REST cancel reasons accepted: CARRIER_REQUEST, SITE_REQUEST (dock planner, D-5). TURNED_AWAY is set only by the
  CancelOnVisitRejected policy and ASN_CANCELLED only by CancelWhenAllAsnsCancelled.
- **A11** ETag = weak `W/"<version>"`; `If-Match` is honoured on reschedule and cancel (412 on mismatch) and optional (no 428).
- **A12** Legacy import (stage A, D-2): a legacy Booked event imports an appointment (provenance `legacy-import`, direction INBOUND,
  `carrier_scac` null, version 1, state BOOKED, site from the local door join; an unknown door skips the event and counts it) and
  raises DockAppointmentBooked. It enforces none of window-in-past, hours, closures, overlap or known-ASN. Importing is idempotent by
  appointment id. Later legacy CheckedIn / Cancelled (reason CARRIER_REQUEST) / Completed events move only appointments whose
  provenance is `legacy-import` and raise this service's normal events. The door exclusion constraint does not cover legacy imports
  (ADR 0005, A19), so an overlapping import is stored as the legacy system had it.
- **A13** NoShowSweep: `POST /no-show-sweeps` marks every BOOKED appointment with `window_end + no_show_grace <= now` (grace per site,
  default 60 min), each in its own transaction; the answer lists the ids. Naturally idempotent; also takes an Idempotency-Key.
- **A14** The adherence report is folded from this service's own analytics topic (ADR 0006): per site and carrier the on-time, early,
  late and no-show counts and shares over a mandatory `from`/`to` range.
- **A15** MCP authority (agentic standard v2): read tools L1 (list_appointments, get_appointment, get_door_availability,
  get_dock_calendar, list_at_risk_appointments); book/reschedule/cancel/report_eta L3 (the fleet rule reserves L1/L2 for read tools; D-6 said L2, corrected by ADR 0007) with a mandatory `initiated_by`. Calendar
  changes and the sweep are not exposed over MCP. No tool records an arrival or a door release (physical facts of yard-management).
- **A16** Idempotency-Key is required on every creating POST and replays answer byte-identically with `Idempotency-Replayed: true`;
  PUT is idempotent by method.
- **A17** Booked and Rescheduled carry `arrival_tolerance_minutes`, the DockCalendar setting at the time of the event (X1).
- **A18** CancelOnVisitRejected cancels a BOOKED appointment only; any other state, an unknown id or a site mismatch is ignored and
  counted.
