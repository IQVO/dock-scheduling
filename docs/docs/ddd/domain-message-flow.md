---
id: domain-message-flow
title: Domain Message Flow
sidebar_label: Domain Message Flow
description: "Four Dock Scheduling scenarios as ddd-crew domain message flows: booking, arrival to completion, late ETA and no-show, and closure with ASN cancellation."
---

# Domain Message Flow

Following ddd-crew [Domain Message Flow Modelling](https://github.com/ddd-crew/domain-message-flow-modelling).
Participants are contexts and actors. Every arrow is a message from
the yard design model, prefixed `cmd:`, `qry:` or `evt:`. Events are written
`entity.EventName`; the full type is
`com.warehouse.wms.<producer>.<entity>.<EventName>` (see
[Domain Events](/docs/ddd/domain-events)). Route
parameters are written without braces. Events marked `analytics only` go to
`warehouse.dock-scheduling.analytics` and have no named consumer; the
diagrams show them as notes.

## 1. Booking, rescheduling and cancelling

```mermaid
%%{init: {"sequence": {"wrap": true, "width": 120, "actorMargin": 24, "messageMargin": 28, "boxMargin": 6, "noteMargin": 6, "mirrorActors": false}}}%%
sequenceDiagram
    autonumber
    actor CS as Carrier<br/>scheduler
    participant DS as dock-<br/>scheduling
    participant YM as yard-<br/>management
    participant WP as warehouse-<br/>planning
    participant IR as inbound-<br/>receiving
    participant OS as outbound-<br/>shipping
    CS->>DS: cmd: BookAppointment<br/>POST /appointments
    Note over DS: DoorView, DockCalendar,<br/>AsnView or CutoffView,<br/>DoorSchedule.CheckNoOverlap
    alt a rule refuses the booking
        DS-->>CS: cmd: reply problem+json<br/>ErrDoorDoubleBooked,<br/>ErrDoorUnavailable,<br/>ErrUnknownAsn
    else booked
        DS->>YM: evt: dockappointment.<br/>DockAppointmentBooked
        DS->>WP: evt: dockappointment.<br/>DockAppointmentBooked
        DS->>IR: evt: dockappointment.<br/>DockAppointmentBooked
        DS->>OS: evt: dockappointment.<br/>DockAppointmentBooked
    end
    CS->>DS: cmd: RescheduleAppointment<br/>POST /appointments/<br/>appointmentId/reschedules
    DS->>YM: evt: dockappointment.<br/>DockAppointmentRescheduled
    DS->>WP: evt: dockappointment.<br/>DockAppointmentRescheduled
    DS->>IR: evt: dockappointment.<br/>DockAppointmentRescheduled
    CS->>DS: cmd: CancelAppointment<br/>POST /appointments/<br/>appointmentId/cancellations
    DS->>YM: evt: dockappointment.<br/>DockAppointmentCancelled
    DS->>WP: evt: dockappointment.<br/>DockAppointmentCancelled
    DS->>IR: evt: dockappointment.<br/>DockAppointmentCancelled
```

Source: `internal/domain/dockappointment/appointment.go`, `internal/domain/dockappointment/commands.go`, `internal/domain/dockappointment/events.go`, `internal/adapters/inbound/kafka/yard_consumer.go`, `internal/adapters/inbound/kafka/facility_consumer.go`, `internal/adapters/inbound/kafka/asn_consumer.go`, `internal/adapters/inbound/kafka/cutoff_consumer.go`, `internal/adapters/outbound/kafka/encoder.go`, `apis/asyncapi.yaml`.

Omitted: the three branches are shown in sequence for width; a real carrier
reschedules or cancels one appointment, not all three in a row. A reschedule
is allowed only from `BOOKED`, a cancel from `BOOKED` or `CHECKED_IN`
(otherwise `ErrIllegalTransition`, 409). `inbound-receiving` also consumes
`DockAppointmentRescheduled` in the code (its appointments copy, ADR 0019); `outbound-shipping`
reads `DockAppointmentBooked`. A dock planner may also reschedule and cancel. The `Err...` to
HTTP status mapping is decision D-10: 409 for conflicts, 422 for value rules.

## 2. From the gate to the released door

```mermaid
%%{init: {"sequence": {"wrap": true, "width": 120, "actorMargin": 24, "messageMargin": 28, "boxMargin": 6, "noteMargin": 6, "mirrorActors": false}}}%%
sequenceDiagram
    autonumber
    participant YM as yard-<br/>management
    participant DS as dock-<br/>scheduling
    participant IR as inbound-<br/>receiving
    YM->>DS: evt: yardvisit.VisitCheckedIn<br/>appointment_id, arrived_at
    Note over DS: ArrivalFromGate<br/>RecordArrival with<br/>arrival_status<br/>BOOKED to CHECKED_IN
    Note over DS: DockAppointmentCheckedIn<br/>to the analytics topic
    YM->>DS: evt: trailer.TrailerDocked<br/>door_code, appointment_id
    alt door differs from the booked door
        Note over DS: ActualDoorWins<br/>ReassignDoor without the<br/>overlap refusal
        DS->>IR: evt: dockappointment.<br/>DockAppointmentDoorReassigned<br/>integration and analytics topics
        opt a later appointment now overlaps on the new door
            Note over DS: DockAppointmentAtRisk<br/>cause DOOR_REASSIGNED_OVERLAP
        end
    end
    YM->>DS: evt: trailer.TrailerUndocked<br/>appointment_id, undocked_at
    Note over DS: CompleteOnDoorRelease<br/>RecordDoorRelease<br/>CHECKED_IN to COMPLETED
    Note over DS: DockAppointmentCompleted<br/>to the analytics topic
    opt release applied to a BOOKED appointment
        Note over DS: OutOfOrderYardFacts<br/>BOOKED to COMPLETED<br/>arrival recorded as implicit
    end
```

Source: `internal/application/usecases/facts.go`, `internal/application/usecases/yard.go`, `internal/application/usecases/change.go`, `internal/adapters/outbound/kafka/encoder.go`, `apis/asyncapi.yaml`.

Omitted: the yard's own steps between the three events (moves, door work),
which belong to `yard-management`. A yard fact for an unknown, terminal,
`CANCELLED` or `NO_SHOW` appointment is ignored and counted; a late
`VisitCheckedIn` for a `COMPLETED` appointment is ignored. One appointment covers exactly one trailer
(decision X3). A release applied to a `BOOKED` appointment completes it without a
`DockAppointmentCheckedIn`; the adherence report counts it as `ON_TIME` and the metric
`dock_scheduling_implicit_arrivals_total` counts it (decision X6).

## 3. A late ETA and a no-show

```mermaid
%%{init: {"sequence": {"wrap": true, "width": 104, "actorMargin": 16, "messageMargin": 28, "boxMargin": 6, "noteMargin": 6, "mirrorActors": false}}}%%
sequenceDiagram
    autonumber
    actor CS as Carrier<br/>scheduler
    participant CR as Scheduler<br/>cron
    participant DS as dock-<br/>scheduling
    participant YM as yard-<br/>management
    participant WP as warehouse-<br/>planning
    participant EM as exception-<br/>management
    CS->>DS: cmd: ReportEta<br/>POST /appointments/<br/>appointmentId/eta-reports
    opt eta_at later than window_start plus arrival tolerance
        Note over DS: EtaRisk<br/>DockAppointmentAtRisk cause<br/>LATE_ETA<br/>to the analytics topic<br/>nothing reschedules
    end
    CR->>DS: cmd: MarkNoShow<br/>POST /no-show-sweeps
    Note over DS: NoShowSweep<br/>BOOKED past window_end<br/>plus no-show grace
    DS->>YM: evt: dockappointment.<br/>DockAppointmentNoShow
    DS->>WP: evt: dockappointment.<br/>DockAppointmentNoShow
    DS->>EM: evt: dockappointment.<br/>DockAppointmentNoShow
    Note over YM: NoShowFreesHolding<br/>cancels the open TO_DOOR move
    Note over EM: opens a problem case
```

Source: `internal/application/usecases/facts.go`, `internal/application/usecases/yard.go`, `internal/application/usecases/change.go`, `internal/adapters/inbound/kafka/yard_consumer.go`, `internal/adapters/inbound/kafka/facility_consumer.go`, `internal/adapters/inbound/kafka/asn_consumer.go`, `internal/adapters/inbound/kafka/cutoff_consumer.go`, `internal/adapters/outbound/kafka/encoder.go`, `apis/asyncapi.yaml`, `internal/adapters/inbound/kafka/legacy_import_consumer.go`.

Omitted: the sweep is idempotent, so a second run marks nothing new. The
routing of the case inside `exception-management` is that context's and is
not in the design model beyond "open problem cases ... and `DockAppointmentNoShow`".
A carrier that missed its window books a new appointment: `NO_SHOW` is terminal (decision D-9).

## 4. A door closure and a cancelled ASN

```mermaid
%%{init: {"sequence": {"wrap": true, "width": 120, "actorMargin": 24, "messageMargin": 28, "boxMargin": 6, "noteMargin": 6, "mirrorActors": false}}}%%
sequenceDiagram
    autonumber
    actor DP as Dock<br/>planner
    participant IR as inbound-<br/>receiving
    participant DS as dock-<br/>scheduling
    participant YM as yard-<br/>management
    participant WP as warehouse-<br/>planning
    DP->>DS: cmd: ScheduleDoorClosure<br/>POST /sites/siteId/<br/>dock-calendar/closures
    alt closure covers an active appointment
        DS-->>DP: cmd: reply problem+json<br/>ErrClosureConflictsWithAppointments<br/>lists the appointment ids
    else accepted
        DS->>YM: evt: dockcalendar.<br/>DoorClosureScheduled
    end
    DP->>DS: cmd: LiftDoorClosure<br/>POST /sites/siteId/<br/>dock-<br/>calendar/closures/closureId/lifts
    DS->>YM: evt: dockcalendar.<br/>DoorClosureLifted
    IR->>DS: evt: ASNCancelled<br/>asn_number
    Note over DS: CancelWhenAllAsnsCancelled<br/>INBOUND BOOKED appointment<br/>with every ASN cancelled
    DS->>YM: evt: dockappointment.<br/>DockAppointmentCancelled<br/>reason ASN_CANCELLED
    DS->>WP: evt: dockappointment.<br/>DockAppointmentCancelled
    DS->>IR: evt: dockappointment.<br/>DockAppointmentCancelled
```

Source: `internal/domain/dockcalendar/calendar.go`, `internal/domain/dockcalendar/events.go`, `internal/application/usecases/facts.go`, `internal/application/usecases/yard.go`, `internal/application/usecases/change.go`, `internal/adapters/inbound/kafka/yard_consumer.go`, `internal/adapters/inbound/kafka/facility_consumer.go`, `internal/adapters/inbound/kafka/asn_consumer.go`, `internal/adapters/inbound/kafka/cutoff_consumer.go`, `internal/adapters/outbound/kafka/encoder.go`, `apis/asyncapi.yaml`.

Omitted: `DoorOperatingHoursDeclared` (analytics only) and
`DeclareOperatingHours`, which is refused with `ErrHoursConflictWithAppointments` when it would
leave an active appointment outside the hours (decision D-8); the strangler import and the phase-2 pickup appointments
(`LegacyImport`, `PickupFromCutoffs`), which are described on
[Domain Events](/docs/ddd/domain-events). The refusal that
lists the appointment ids is the design model's only help for a planner closing a
door; `ListAppointments` takes filters (for example `atRisk`), which are in the OpenAPI contract.
