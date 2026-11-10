---
id: eventstorming
title: EventStorming
sidebar_label: EventStorming
description: "Process-level EventStorming boards for Dock Scheduling (booking, yard facts, ETA risk and no-show, ASN cancellation, door calendar and pickups), with hotspots taken from the model's genuine gaps."
---

# EventStorming

Process-level EventStorming in the ddd-crew
[EventStorming glossary & cheat sheet](https://github.com/ddd-crew/eventstorming-glossary-cheat-sheet)
notation. Every sticky comes from the yard design model. Hotspots were the genuine open
questions or risks in the design model when this page was written; the source of each is in the
[hotspot table](#hotspots), whose last column says how the built service settled it. The boards
keep the hotspot stickies as they were drawn.

## Legend

| Colour | Sticky | Hex |
| --- | --- | --- |
| Blue | Command | `#a5d8ff` |
| Yellow | Aggregate | `#ffec99` |
| Orange | Domain Event | `#ffc078` |
| Lilac | Policy | `#d0bfff` |
| Green | Read Model | `#b2f2bb` |
| Pink | External System (a sibling context or an actor) | `#fcc2d7` |
| Red | Hotspot | `#ff8787` |

## 1. Booking, rescheduling and cancelling

```mermaid
flowchart TB
    classDef cmd fill:#a5d8ff,stroke:#1971c2,color:#000
    classDef agg fill:#ffec99,stroke:#f08c00,color:#000
    classDef evt fill:#ffc078,stroke:#e8590c,color:#000
    classDef pol fill:#d0bfff,stroke:#7048e8,color:#000
    classDef rm fill:#b2f2bb,stroke:#2f9e44,color:#000
    classDef ext fill:#fcc2d7,stroke:#c2255c,color:#000
    classDef hot fill:#ff8787,stroke:#c92a2a,color:#000
    CS["Carrier<br/>scheduler"]:::ext
    C1["BookAppointment"]:::cmd
    C2["Reschedule-<br/>Appointment<br/>CancelAppointment"]:::cmd
    RM1["DoorView<br/>AsnView<br/>CutoffView"]:::rm
    CAL["DockCalendar"]:::agg
    A1["DockAppointment<br/>DoorSchedule.<br/>CheckNoOverlap"]:::agg
    E1["DockAppointment-<br/>Booked"]:::evt
    E2["DockAppointment-<br/>Rescheduled<br/>DockAppointment-<br/>Cancelled"]:::evt
    X1["yard-management<br/>warehouse-planning<br/>inbound-receiving"]:::ext
    X2["yard-management<br/>warehouse-planning"]:::ext
    H1["H1 site-side cancel?<br/>H2 door change on<br/>reschedule?"]:::hot
    CS --> C1 --> A1
    CS --> C2 --> A1
    RM1 --> A1
    CAL --> A1
    A1 --> E1 --> X1
    A1 --> E2 --> X2
    C2 --- H1
```

Source: `internal/domain/dockappointment/appointment.go`, `internal/domain/dockappointment/commands.go`, `internal/domain/dockappointment/events.go`, `internal/adapters/inbound/kafka/yard_consumer.go`, `internal/adapters/inbound/kafka/facility_consumer.go`, `internal/adapters/inbound/kafka/asn_consumer.go`, `internal/adapters/inbound/kafka/cutoff_consumer.go`, `internal/adapters/outbound/kafka/encoder.go`, `apis/asyncapi.yaml`, `internal/application/ports/ports.go`, `migrations/0001_dock_scheduling.up.sql`.

Omitted: the refusals (`ErrInvalidDoorCode`, `ErrInvalidCarrier`,
`ErrInvalidWindow`, `ErrWindowInPast`, `ErrUnknownDoor`,
`ErrDoorFlowMismatch`, `ErrDoorDoubleBooked`, `ErrDoorUnavailable`,
`ErrUnknownAsn`, `ErrMissingOutboundRef`). `DockAppointmentCancelled` also
reaches `inbound-receiving`; `DockAppointmentRescheduled` does not.

## 2. Arrival at the gate and a different door

```mermaid
flowchart TB
    classDef cmd fill:#a5d8ff,stroke:#1971c2,color:#000
    classDef agg fill:#ffec99,stroke:#f08c00,color:#000
    classDef evt fill:#ffc078,stroke:#e8590c,color:#000
    classDef pol fill:#d0bfff,stroke:#7048e8,color:#000
    classDef rm fill:#b2f2bb,stroke:#2f9e44,color:#000
    classDef ext fill:#fcc2d7,stroke:#c2255c,color:#000
    classDef hot fill:#ff8787,stroke:#c92a2a,color:#000
    YM["yard-management"]:::ext
    E0["VisitCheckedIn"]:::evt
    E1["TrailerDocked"]:::evt
    P1["ArrivalFromGate"]:::pol
    P2["ActualDoorWins"]:::pol
    C1["RecordArrival"]:::cmd
    C2["ReassignDoor"]:::cmd
    A1["DockAppointment"]:::agg
    E3["DockAppointment-<br/>CheckedIn"]:::evt
    E4["DockAppointment-<br/>DoorReassigned"]:::evt
    E5["DockAppointment-<br/>AtRisk"]:::evt
    H4["H4 arrival tolerance<br/>not published"]:::hot
    YM --> E0 --> P1 --> C1 --> A1
    YM --> E1 --> P2 --> C2 --> A1
    A1 --> E3
    A1 --> E4
    A1 --> E5
    P1 --- H4
```

Source: `internal/domain/dockappointment/appointment.go`, `internal/domain/dockappointment/commands.go`, `internal/domain/dockappointment/events.go`, `internal/application/usecases/facts.go`, `internal/application/usecases/yard.go`, `internal/application/usecases/change.go`, `internal/adapters/outbound/kafka/encoder.go`, `apis/asyncapi.yaml`.

Omitted: `DockAppointmentCheckedIn` and `AtRisk` are `analytics only`; no context reacts to them.
`DoorReassigned` is also on the integration topic since service ADR 0010 (`inbound-receiving`
reads it). `E5` is raised for the
**overlapped** appointment, with cause `DOOR_REASSIGNED_OVERLAP`.

## 3. Door release and out-of-order yard facts

```mermaid
flowchart TB
    classDef cmd fill:#a5d8ff,stroke:#1971c2,color:#000
    classDef agg fill:#ffec99,stroke:#f08c00,color:#000
    classDef evt fill:#ffc078,stroke:#e8590c,color:#000
    classDef pol fill:#d0bfff,stroke:#7048e8,color:#000
    classDef rm fill:#b2f2bb,stroke:#2f9e44,color:#000
    classDef ext fill:#fcc2d7,stroke:#c2255c,color:#000
    classDef hot fill:#ff8787,stroke:#c92a2a,color:#000
    YM["yard-management"]:::ext
    E2["TrailerUndocked"]:::evt
    P3["CompleteOnDoor-<br/>Release"]:::pol
    P4["OutOfOrder-<br/>YardFacts"]:::pol
    C3["RecordDoorRelease"]:::cmd
    A1["DockAppointment"]:::agg
    E6["DockAppointment-<br/>Completed"]:::evt
    H3["H3 several trailers<br/>on one appointment?"]:::hot
    H5["H5 gaps in the<br/>out-of-order rules"]:::hot
    YM --> E2 --> P3 --> C3 --> A1
    P4 --> C3
    A1 --> E6
    P3 --- H3
    P4 --- H5
```

Source: `internal/domain/dockappointment/appointment.go`, `internal/domain/dockappointment/commands.go`, `internal/domain/dockappointment/events.go`, `internal/application/usecases/facts.go`, `internal/application/usecases/yard.go`, `internal/application/usecases/change.go`, `internal/adapters/outbound/kafka/encoder.go`, `apis/asyncapi.yaml`.

Omitted: `DockAppointmentCompleted` is `analytics only`.
`OutOfOrderYardFacts` is drawn into `RecordDoorRelease` because it defines
the `BOOKED` to `COMPLETED` path; it also says a late `VisitCheckedIn` for a
`COMPLETED` appointment is ignored and that any yard fact for a `CANCELLED`
or `NO_SHOW` appointment is ignored and counted.

## 4. ETA risk and no-show

```mermaid
flowchart TB
    classDef cmd fill:#a5d8ff,stroke:#1971c2,color:#000
    classDef agg fill:#ffec99,stroke:#f08c00,color:#000
    classDef evt fill:#ffc078,stroke:#e8590c,color:#000
    classDef pol fill:#d0bfff,stroke:#7048e8,color:#000
    classDef rm fill:#b2f2bb,stroke:#2f9e44,color:#000
    classDef ext fill:#fcc2d7,stroke:#c2255c,color:#000
    classDef hot fill:#ff8787,stroke:#c92a2a,color:#000
    CS["Carrier<br/>scheduler"]:::ext
    CR["Scheduler cron"]:::ext
    C1["ReportEta"]:::cmd
    P1["EtaRisk"]:::pol
    P2["NoShowSweep"]:::pol
    C2["MarkNoShow"]:::cmd
    A1["DockAppointment"]:::agg
    E1["DockAppointment-<br/>AtRisk"]:::evt
    E2["DockAppointment-<br/>NoShow"]:::evt
    X["yard-management<br/>warehouse-planning<br/>exception-management"]:::ext
    H6["H6 no read side for<br/>appointments at risk"]:::hot
    H7["H7 NO_SHOW is<br/>terminal"]:::hot
    CS --> C1 --> A1
    A1 --> P1 --> E1
    CR --> P2 --> C2 --> A1
    A1 --> E2 --> X
    E1 --- H6
    E2 --- H7
```

Source: `internal/domain/dockappointment/appointment.go`, `internal/domain/dockappointment/commands.go`, `internal/domain/dockappointment/events.go`, `internal/application/usecases/facts.go`, `internal/application/usecases/yard.go`, `internal/application/usecases/change.go`, `internal/adapters/outbound/kafka/encoder.go`, `apis/asyncapi.yaml`.

Omitted: `DockAppointmentNoShow` also triggers `NoShowFreesHolding` in
`yard-management`, drawn in
[Domain Message Flow](/docs/ddd/domain-message-flow).

## 5. ASN cancellation

```mermaid
flowchart TB
    classDef cmd fill:#a5d8ff,stroke:#1971c2,color:#000
    classDef agg fill:#ffec99,stroke:#f08c00,color:#000
    classDef evt fill:#ffc078,stroke:#e8590c,color:#000
    classDef pol fill:#d0bfff,stroke:#7048e8,color:#000
    classDef rm fill:#b2f2bb,stroke:#2f9e44,color:#000
    classDef ext fill:#fcc2d7,stroke:#c2255c,color:#000
    classDef hot fill:#ff8787,stroke:#c92a2a,color:#000
    IR["inbound-receiving"]:::ext
    E0["ASNCancelled"]:::evt
    RM["AsnView"]:::rm
    P1["CancelWhenAll-<br/>AsnsCancelled"]:::pol
    C1["CancelAppointment"]:::cmd
    A1["DockAppointment"]:::agg
    E1["DockAppointment-<br/>Cancelled"]:::evt
    X["yard-management<br/>warehouse-planning<br/>inbound-receiving"]:::ext
    H8["H8 VisitRejected not<br/>consumed, TURNED_AWAY<br/>has no producer"]:::hot
    IR --> E0 --> RM
    E0 --> P1 --> C1 --> A1 --> E1 --> X
    E1 --- H8
```

Source: `internal/application/usecases/facts.go`, `internal/application/usecases/yard.go`, `internal/application/usecases/change.go`, `internal/adapters/outbound/kafka/encoder.go`, `apis/asyncapi.yaml`, `internal/application/ports/ports.go`, `migrations/0001_dock_scheduling.up.sql`.

Omitted: only an `INBOUND` `BOOKED` appointment whose ASNs are **all**
cancelled is cancelled by the policy; the reason is `ASN_CANCELLED`.

## 6. The door calendar

```mermaid
flowchart TB
    classDef cmd fill:#a5d8ff,stroke:#1971c2,color:#000
    classDef agg fill:#ffec99,stroke:#f08c00,color:#000
    classDef evt fill:#ffc078,stroke:#e8590c,color:#000
    classDef pol fill:#d0bfff,stroke:#7048e8,color:#000
    classDef rm fill:#b2f2bb,stroke:#2f9e44,color:#000
    classDef ext fill:#fcc2d7,stroke:#c2255c,color:#000
    classDef hot fill:#ff8787,stroke:#c92a2a,color:#000
    DP["Dock planner"]:::ext
    FL["facility-layout"]:::ext
    C1["DeclareOperatingHours<br/>DeclareCalendarSettings"]:::cmd
    C2["ScheduleDoorClosure<br/>LiftDoorClosure"]:::cmd
    E0["LocationSlot-<br/>Registered<br/>LocationSlot-<br/>Decommissioned<br/>ZoneRegistered"]:::evt
    RM["DoorView"]:::rm
    A1["DockCalendar"]:::agg
    E1["DoorOperatingHours-<br/>Declared"]:::evt
    E2["DoorClosureScheduled<br/>DoorClosureLifted"]:::evt
    YM["yard-management"]:::ext
    H9["H9 hours change vs<br/>appointments and<br/>decommissioned doors"]:::hot
    H10["H10 settings never<br/>published, no default"]:::hot
    DP --> C1 --> A1
    DP --> C2 --> A1
    FL --> E0 --> RM
    A1 --> E1
    A1 --> E2 --> YM
    C1 --- H9
    C1 --- H10
```

Source: `internal/domain/dockcalendar/calendar.go`, `internal/domain/dockcalendar/events.go`, `internal/adapters/inbound/kafka/yard_consumer.go`, `internal/adapters/inbound/kafka/facility_consumer.go`, `internal/adapters/inbound/kafka/asn_consumer.go`, `internal/adapters/inbound/kafka/cutoff_consumer.go`, `internal/adapters/outbound/kafka/encoder.go`, `apis/asyncapi.yaml`, `internal/application/ports/ports.go`, `migrations/0001_dock_scheduling.up.sql`.

Omitted: `ErrInvalidOperatingHours`, `ErrOverlappingClosure` and
`ErrClosureConflictsWithAppointments` refusals. `DeclareCalendarSettings`
raises no event; `DoorOperatingHoursDeclared` is `analytics only`.

## 7. Phase-2 pickups

```mermaid
flowchart TB
    classDef cmd fill:#a5d8ff,stroke:#1971c2,color:#000
    classDef agg fill:#ffec99,stroke:#f08c00,color:#000
    classDef evt fill:#ffc078,stroke:#e8590c,color:#000
    classDef pol fill:#d0bfff,stroke:#7048e8,color:#000
    classDef rm fill:#b2f2bb,stroke:#2f9e44,color:#000
    classDef ext fill:#fcc2d7,stroke:#c2255c,color:#000
    classDef hot fill:#ff8787,stroke:#c92a2a,color:#000
    PP["process-path-<br/>management"]:::ext
    E1["CPTScheduleChanged"]:::evt
    P2["PickupFromCutoffs<br/>phase 2"]:::pol
    RM["CutoffView"]:::rm
    A1["DockAppointment"]:::agg
    H12["H12 PickupDoorRule and<br/>pickup_lead are undefined"]:::hot
    PP --> E1 --> RM
    E1 --> P2
    P2 -.-> A1
    P2 --- H12
```

Source: `internal/application/usecases/facts.go`, `internal/application/usecases/yard.go`, `internal/application/usecases/change.go`, `internal/adapters/inbound/kafka/yard_consumer.go`, `internal/adapters/inbound/kafka/facility_consumer.go`, `internal/adapters/inbound/kafka/asn_consumer.go`, `internal/adapters/inbound/kafka/cutoff_consumer.go`, `internal/application/ports/ports.go`, `migrations/0001_dock_scheduling.up.sql`.

Omitted: the dotted arrow from `PickupFromCutoffs` is the policy keeping one
`OUTBOUND` appointment per cutoff and operating day; the design model names no
command for it, and in the code it does not exist: the schedule is stored and no appointment is
created from it. The strangler import that this board once showed was removed (ADR 0011).

## Hotspots

| # | Hotspot | Where it comes from in the design model | Gap | In the code (2026-11-09) |
| --- | --- | --- | --- | --- |
| H1 | `CancellationReason` has `SITE_REQUEST`, but the only inbound row for cancel is the carrier scheduler. | `aggregates[DockAppointment]`, `reasons[CancellationReason]`, `inbound[0]` | `DS-G10` | Settled (D-5): the dock planner cancels with `CARRIER_REQUEST` or `SITE_REQUEST` through the same route. |
| H2 | `DockAppointmentRescheduled` carries `door_code` but no previous door; the design model does not say a reschedule may change the door. | `publishes[DockAppointmentRescheduled].data` | `DS-G06` | Settled (D-4, X1): a reschedule may change the door; the event carries `previous_door_code`. |
| H3 | An appointment is one carrier visit, but `VisitCheckedIn` carries `trailers`, and the first `TrailerUndocked` completes the appointment. | `policies[CompleteOnDoorRelease]`, `contexts[yard-management].publishes[VisitCheckedIn]` | `DS-G03` | Settled (X3): one appointment covers exactly one trailer. |
| H4 | The arrival tolerance is a `DockCalendar` setting; `yard-management`'s `DirectOnCheckIn` needs it and no event carries it. | `aggregates[DockCalendar].value_objects`, `contexts[yard-management].policies[DirectOnCheckIn]` | `DS-G01` | Settled (X1): `Booked` and `Rescheduled` carry `arrival_tolerance_minutes`. |
| H5 | The out-of-order rules cover four cases; a second `VisitCheckedIn`, a `TrailerDocked` after completion and an implicit arrival in the adherence report are not covered. | `policies[OutOfOrderYardFacts, ArrivalFromGate]` | `DS-G19` | Settled: the state machine decides; an already applied or terminal fact is ignored and counted, an implicit arrival is counted as `ON_TIME` (X6). |
| H6 | `DockAppointmentAtRisk` is `analytics only` and no read model or route exposes it, yet the console and the ops agent are meant to show at-risk appointments. | `publishes[DockAppointmentAtRisk]`, `fleet_changes` | `DS-G11` | Settled (D-6): the REST appointment exposes `atRisk` and `atRiskCause`, `GET /appointments` filters on `atRisk` and the MCP tool `list_at_risk_appointments` lists them. |
| H7 | `NO_SHOW` has no outgoing transition; a carrier that arrives after the sweep is ignored by this context. | `aggregates[DockAppointment].transitions`, `policies[OutOfOrderYardFacts]` | `DS-G16` | Settled (D-9): terminal, a late carrier books anew. |
| H8 | `TURNED_AWAY` has no producer, and `VisitRejected` is not consumed here. | `reasons[CancellationReason]`, `consumes`, `contexts[yard-management].publishes[VisitRejected]` | `DS-G02` | Settled (X2): `VisitRejected` is consumed (policy `CancelOnVisitRejected`), `TURNED_AWAY` is its cancellation reason. |
| H9 | A closure is refused if it covers an active appointment; a change of operating hours and a decommissioned door are not. | `aggregates[DockCalendar].invariants`, `consumes[facility-layout]` | `DS-G12`, `DS-G13` | Settled: new hours that strand an appointment are refused (D-8); a decommissioned door blocks new bookings only (D-7). |
| H10 | `DeclareCalendarSettings` raises no event, and no default is given for the arrival tolerance or the no-show grace. | `aggregates[DockCalendar]`, `publishes` | `DS-G07`, `DS-G01` | Settled (D-1): defaults 240, 15, 15 and 60 minutes, created lazily; the tolerance travels on the booking events. |
| H11 | `ImportLegacyAppointment` lists no invariants, and the effect of the legacy check-in, cancel and complete events is not stated. | `policies[LegacyImport]`, `consumes[inbound-receiving]` | `DS-G08` | Settled (D-2): no invariant is enforced; later legacy events move only `legacy-import` appointments. Moot since ADR 0011: the importer was removed. |
| H12 | `PickupFromCutoffs` names `PickupDoorRule` and `pickup_lead`, defined nowhere, and creates an appointment with no carrier. | `policies[PickupFromCutoffs]`, `aggregates[DockAppointment].invariants` | `DS-G04` | Open by decision (D-3): phase 2. The schedule is consumed and stored; no appointment is created. |
