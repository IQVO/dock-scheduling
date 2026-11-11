---
paths:
  - "internal/domain/**"
  - "internal/application/**"
  - "features/**"
---

# Domain model: ubiquitous language, aggregates, events, use cases

Source of truth: `docs/adr/0002-aggregates-invariants-and-assumptions.md` (states, invariants, error names,
numbered assumptions). This file is the skimmable summary; when they differ, the ADR wins.

## Ubiquitous Language (use these exact names)

- **DockAppointment**: one booked door window for one carrier visit and ONE trailer. Aggregate root, id
  `appt-<uuid>`. States `BOOKED`, `CHECKED_IN`, `COMPLETED`, `CANCELLED`, `NO_SHOW`; the last three are terminal.
  Active = `BOOKED` or `CHECKED_IN`.
- **DockCalendar**: the per-site settings every booking is checked against (operating hours, closures,
  `max_window_minutes` 240, `slot_granularity_minutes` 15, `arrival_tolerance_minutes` 15,
  `no_show_grace_minutes` 60). Aggregate root, id `siteId` (= facility-layout `siteCode`), created lazily.
- **Window**: half-open `[start, end)` in UTC, so `[10:00, 12:00)` and `[12:00, 14:00)` are compatible.
- **Direction**: `INBOUND` (covers >= 1 ASN) or `OUTBOUND` (a known `cpt_id` or >= 1 `shipment_ref`); the door's
  dockFlow must allow it.
- **ArrivalStatus**: `EARLY` / `ON_TIME` / `LATE` against `window_start` plus or minus the arrival tolerance.
- **Door**: a local copy of a facility-layout location slot with role `Dock`; never looked up live.

## Aggregates

- **DockAppointment** (`internal/domain/dockappointment`): legal transitions (`ErrIllegalTransition`), window
  shape (`ErrInvalidWindow`, `ErrWindowInPast`), carrier, refs. `DoorSchedule.CheckDoor` (`ErrUnknownDoor`,
  `ErrDoorFlowMismatch`) and `DoorSchedule.CheckNoOverlap` (`ErrDoorDoubleBooked`, 409) run under a per-door
  Postgres advisory lock with an exclusion constraint as backstop. One ASN sits on one active appointment
  (`ErrAsnAlreadyScheduled`). `version` starts at 1, +1 per accepted change, is the weak ETag.
- **DockCalendar** (`internal/domain/dockcalendar`): `CheckWindow` (`ErrDoorUnavailable`); closures do not
  overlap (`ErrOverlappingClosure`) or cover an active appointment
  (`ErrClosureConflictsWithAppointments`); new hours may not strand one (`ErrHoursConflictWithAppointments`).

Problem slug = kebab-case of the error name without `Err` (`ErrDoorDoubleBooked` -> `door-double-booked`).

## Domain events

Booked, Rescheduled, Cancelled, NoShow, DoorReassigned (integration + analytics, ADR 0010) for DockAppointment;
CheckedIn, Completed, AtRisk are analytics-only; DoorClosureScheduled, DoorClosureLifted,
DoorOperatingHoursDeclared for DockCalendar; CarrierRegistered, CarrierSuspended, CarrierReinstated for Carrier (the
carrier registry keyed by SCAC, integration + analytics, ADR 0012). Catalogue: `docs/adr/0004-cloudevents-catalogue.md`.

## Key use cases (`internal/application/usecases`)

- `BookAppointment`, `RescheduleAppointment`, `CancelAppointment`, `ReportEta`: operator and MCP writes.
- `DeclareOperatingHours`, `DeclareCalendarSettings`, `ScheduleDoorClosure`, `LiftDoorClosure`: calendar (REST only).
- `RunNoShowSweep`: BOOKED with `window_end + no_show_grace <= now` becomes `NO_SHOW` (driven by `cmd/sweep`).
- `Consume*`: event-fed policies. Arrival and door release are yard-management facts (ArrivalFromGate,
  ActualDoorWins, CompleteOnDoorRelease, CancelOnVisitRejected); nothing in this service's API records them.
  CPTScheduleChanged is stored only (no appointment is created from it).
