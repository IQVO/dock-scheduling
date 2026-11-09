---
id: sequence-diagrams
title: Sequence Diagrams
sidebar_label: Sequence Diagrams
description: "One UML sequence diagram per Dock Scheduling command use case, from the inbound adapter through the aggregate, repository and outbox to Kafka."
---

# Sequence Diagrams

One UML sequence diagram per command use case of
[Use Cases](/docs/ddd/use-cases), in the
hexagonal layout of the built service: inbound adapter → use case → aggregate → repositories and
views → outbox (same transaction) → relay → Kafka. The outbox, the relay and Kafka
share one lifeline ("outbox and relay to Kafka") to keep each diagram at five
participants.
`alt` blocks show error branches, optimistic-concurrency `version` checks and
idempotency (REST `Idempotency-Key`, Kafka inbox on the CloudEvents `id`).
Every `Err...` has the status of decision D-10 (409 conflicts, 422 value rules, 404 unknown resources; the complete table is on [Use cases](/docs/ddd/use-cases)). Route parameters are written
without braces. The relay publishes to `warehouse.dock-scheduling.events` for
integration events and, as a twin, to `warehouse.dock-scheduling.analytics`; events
marked `analytics only` go to the analytics topic alone. Both are keyed by the aggregate root id.

## 1. BookAppointment

Carrier scheduler, REST `POST /appointments`.

```mermaid
%%{init: {"sequence": {"wrap": true, "width": 120, "actorMargin": 24, "messageMargin": 28, "boxMargin": 6, "noteMargin": 6, "mirrorActors": false}}}%%
sequenceDiagram
    autonumber
    participant H as http<br/>handler
    participant U as use case
    participant A as Dock<br/>Appointment
    participant R as repositories<br/>and views
    participant O as outbox and<br/>relay to Kafka
    H->>U: BookAppointment door,<br/>direction, window,<br/>carrier, refs,<br/>Idempotency-Key
    U->>R: Idempotency-Key seen?
    alt replay
        R-->>U: first response
        U-->>H: first response
    else new key
        U->>A: build door code, carrier,<br/>window
        alt value object invalid or window in the past
            A-->>U: ErrInvalidDoorCode,<br/>ErrInvalidCarrier,<br/>ErrInvalidWindow or<br/>ErrWindowInPast
            U-->>H: 422 problem+json
        else valid
            U->>R: DoorView, DockCalendar of<br/>the site,<br/>AsnView or CutoffView
            alt unknown door or flow mismatch
                R-->>U: ErrUnknownDoor or<br/>ErrDoorFlowMismatch
                U-->>H: 422 problem+json
            else outside hours or inside a closure
                R-->>U: ErrDoorUnavailable
                U-->>H: 409 problem+json
            else ASN or CPT reference not known
                R-->>U: ErrUnknownAsn or<br/>ErrMissingOutboundRef
                U-->>H: 422 problem+json
            else references fine
                U->>R: per-door advisory lock,<br/>DoorSchedule.CheckNoOverlap
                alt overlaps a BOOKED or CHECKED_IN appointment
                    R-->>U: ErrDoorDoubleBooked
                    U-->>H: 409 problem+json
                else door free in the window
                    U->>A: BookAppointment
                    A-->>U: BOOKED, version 1
                    Note over U,O: one transaction
                    U->>R: insert appointment, ASNs,<br/>shipment refs
                    U->>O: insert<br/>DockAppointmentBooked
                    U->>R: store Idempotency-Key<br/>response
                    U-->>H: 201 appointment and ETag
                end
            end
        end
    end
    Note over O: after commit the relay<br/>publishes to warehouse.<br/>dock-scheduling.events

```

Source: `internal/domain/dockappointment/appointment.go`, `internal/domain/dockappointment/commands.go`, `internal/domain/dockappointment/events.go`, `internal/adapters/outbound/kafka/encoder.go`, `apis/asyncapi.yaml`, `internal/adapters/inbound/http/errors.go`, `internal/application/usecases/facts.go`.

Omitted: the per-door lock is released at commit (not drawn). The per-door lock is taken first (`internal/application/usecases/book.go`); the HTTP statuses are decision D-10. `slot_granularity_minutes` forces the window start and end onto a grid of whole minutes since the Unix epoch (`ErrInvalidWindow`, decision D-11), and an ASN already on another active appointment is refused (`ErrAsnAlreadyScheduled`).

## 2. RescheduleAppointment

Carrier scheduler, REST `POST /appointments/appointmentId/reschedules`.

```mermaid
%%{init: {"sequence": {"wrap": true, "width": 120, "actorMargin": 24, "messageMargin": 28, "boxMargin": 6, "noteMargin": 6, "mirrorActors": false}}}%%
sequenceDiagram
    autonumber
    participant H as http<br/>handler
    participant U as use case
    participant A as Dock<br/>Appointment
    participant R as repositories<br/>and views
    participant O as outbox and<br/>relay to Kafka
    H->>U: RescheduleAppointment<br/>window, If-Match version,<br/>Idempotency-Key
    U->>R: Get appointment
    alt not found
        U-->>H: 404 appointment-not-found
    else found
        U->>A: RescheduleAppointment new<br/>window, now
        alt state not BOOKED
            A-->>U: ErrIllegalTransition
            U-->>H: 409 illegal-transition
        else window invalid or in the past
            A-->>U: ErrInvalidWindow or<br/>ErrWindowInPast
            U-->>H: 422 problem+json
        else BOOKED
            U->>R: DoorView flow,<br/>DockCalendar hours and<br/>closures
            alt flow mismatch or door unavailable
                R-->>U: ErrDoorFlowMismatch or<br/>ErrDoorUnavailable
                U-->>H: 422 or 409 problem+json
            else allowed
                U->>R: per-door advisory lock,<br/>CheckNoOverlap ignoring<br/>this appointment
                alt overlap
                    R-->>U: ErrDoorDoubleBooked
                    U-->>H: 409 problem+json
                else free
                    A-->>U: BOOKED, new window,<br/>previous window
                    Note over U,O: one transaction
                    U->>R: update where version<br/>matches
                    alt version changed
                        R-->>U: conflict
                        U-->>H: 409 concurrent-update
                    else saved
                        U->>O: insert<br/>DockAppointmentRescheduled
                        U-->>H: 200 appointment and ETag
                    end
                end
            end
        end
    end
    Note over O: after commit the relay<br/>publishes to warehouse.<br/>dock-scheduling.events

```

Source: `internal/domain/dockappointment/appointment.go`, `internal/domain/dockappointment/commands.go`, `internal/domain/dockappointment/events.go`, `internal/adapters/outbound/kafka/encoder.go`, `apis/asyncapi.yaml`.

Omitted: the idempotency replay branch (drawn in diagram 1); a reschedule may also change the door (decision D-4); the event then carries `previous_door_code`.

## 3. CancelAppointment

Carrier scheduler, REST `POST /appointments/appointmentId/cancellations`. The policy `CancelWhenAllAsnsCancelled` calls the same use case with reason `ASN_CANCELLED` from the Kafka consumer of `ASNCancelled`.

```mermaid
%%{init: {"sequence": {"wrap": true, "width": 120, "actorMargin": 24, "messageMargin": 28, "boxMargin": 6, "noteMargin": 6, "mirrorActors": false}}}%%
sequenceDiagram
    autonumber
    participant H as http handler<br/>or consumer
    participant U as use case
    participant A as Dock<br/>Appointment
    participant R as repositories<br/>and views
    participant O as outbox and<br/>relay to Kafka
    H->>U: CancelAppointment reason,<br/>If-Match version
    U->>R: Get appointment
    alt not found
        U-->>H: 404 appointment-not-found
    else found
        U->>A: CancelAppointment reason
        alt state not BOOKED or CHECKED_IN
            A-->>U: ErrIllegalTransition
            U-->>H: 409 illegal-transition
        else reason outside CancellationReason
            A-->>U: ErrInvalidReason
            U-->>H: 422 invalid-reason
        else cancellable
            A-->>U: CANCELLED
            Note over U,O: one transaction
            U->>R: update where version<br/>matches
            alt version changed
                R-->>U: conflict
                U-->>H: 409 concurrent-update
            else saved
                U->>O: insert<br/>DockAppointmentCancelled
                U-->>H: 200 appointment
            end
        end
    end
    Note over O: after commit the relay<br/>publishes to warehouse.<br/>dock-scheduling.events

```

Source: `internal/domain/dockappointment/appointment.go`, `internal/domain/dockappointment/commands.go`, `internal/domain/dockappointment/events.go`, `internal/application/usecases/facts.go`, `internal/application/usecases/yard.go`, `internal/application/usecases/change.go`.

Omitted: the inbox check on the policy path (drawn in diagram 5) and the idempotency replay on the REST path (diagram 1). On the policy path there is no HTTP reply: a non-cancellable appointment is skipped.

## 4. ReportEta

Carrier scheduler, REST `POST /appointments/appointmentId/eta-reports`. Policy `EtaRisk` decides whether the report raises an advisory.

```mermaid
%%{init: {"sequence": {"wrap": true, "width": 120, "actorMargin": 24, "messageMargin": 28, "boxMargin": 6, "noteMargin": 6, "mirrorActors": false}}}%%
sequenceDiagram
    autonumber
    participant H as http<br/>handler
    participant U as use case
    participant A as Dock<br/>Appointment
    participant R as repositories<br/>and views
    participant O as outbox and<br/>relay to Kafka
    H->>U: ReportEta eta_at, source,<br/>Idempotency-Key
    U->>R: Get appointment and<br/>arrival tolerance
    alt not found
        U-->>H: 404 appointment-not-found
    else found
        U->>A: ReportEta eta_at,<br/>tolerance
        alt state not BOOKED
            A-->>U: ErrIllegalTransition
            U-->>H: 409 illegal-transition
        else BOOKED
            A-->>U: Eta stored, late or not
            Note over U,O: one transaction
            U->>R: update where version<br/>matches
            alt version changed
                R-->>U: conflict
                U-->>H: 409 concurrent-update
            else saved
                opt eta_at later than window_start plus tolerance
                    U->>O: insert<br/>DockAppointmentAtRisk<br/>cause LATE_ETA
                end
                U-->>H: 200 appointment
            end
        end
    end
    Note over O: after commit the relay<br/>publishes to warehouse.<br/>dock-scheduling.analytics

```

Source: `internal/domain/dockappointment/appointment.go`, `internal/domain/dockappointment/commands.go`, `internal/domain/dockappointment/events.go`, `internal/application/usecases/facts.go`, `internal/application/usecases/yard.go`, `internal/application/usecases/change.go`, `internal/adapters/outbound/kafka/encoder.go`, `apis/asyncapi.yaml`.

Omitted: the relay step publishes only when an `AtRisk` row exists; nothing is rescheduled. `BOOKED` is the only state that accepts an ETA (assumption A6); a later on-time ETA or a reschedule clears the at-risk flag.

## 5. RecordArrival

Policy `ArrivalFromGate` on `VisitCheckedIn` naming an `appointment_id`.

```mermaid
%%{init: {"sequence": {"wrap": true, "width": 120, "actorMargin": 24, "messageMargin": 28, "boxMargin": 6, "noteMargin": 6, "mirrorActors": false}}}%%
sequenceDiagram
    autonumber
    participant H as kafka<br/>consumer
    participant U as use case
    participant A as Dock<br/>Appointment
    participant R as repositories<br/>and views
    participant O as outbox and<br/>relay to Kafka
    H->>U: VisitCheckedIn event id,<br/>appointment_id, arrived_at
    U->>O: processed_events contains<br/>event id?
    alt already processed
        U-->>H: ack, no effect
    else new
        U->>R: Get appointment by<br/>appointment_id
        alt unknown or terminal
            R-->>U: not found, COMPLETED,<br/>CANCELLED or NO_SHOW
            U->>O: insert processed_events<br/>row
            U-->>H: ack, ignored and counted
        else found and active
            U->>A: RecordArrival arrived_at,<br/>tolerance
            A-->>U: CHECKED_IN with<br/>arrival_status
            Note over U,O: one transaction
            U->>R: update where version<br/>matches
            alt version changed
                R-->>U: conflict
                U-->>H: rollback, redeliver
            else saved
                U->>O: insert<br/>DockAppointmentCheckedIn
                U->>O: insert processed_events<br/>row
                U-->>H: ack
            end
        end
    end
    Note over O: after commit the relay<br/>publishes to warehouse.<br/>dock-scheduling.analytics

```

Source: `internal/application/usecases/facts.go`, `internal/application/usecases/yard.go`, `internal/application/usecases/change.go`, `internal/adapters/outbound/kafka/encoder.go`, `apis/asyncapi.yaml`.

Omitted: an appointment already `CHECKED_IN` is left unchanged and counted, so only `BOOKED` reaches the aggregate. The `arrival_status` formula is decision X6: EARLY before `window_start` minus the tolerance, LATE after `window_start` plus the tolerance, else ON_TIME.

## 6. ReassignDoor

Policy `ActualDoorWins` on `TrailerDocked` whose door differs from the booked door.

```mermaid
%%{init: {"sequence": {"wrap": true, "width": 120, "actorMargin": 24, "messageMargin": 28, "boxMargin": 6, "noteMargin": 6, "mirrorActors": false}}}%%
sequenceDiagram
    autonumber
    participant H as kafka<br/>consumer
    participant U as use case
    participant A as Dock<br/>Appointment
    participant R as repositories<br/>and views
    participant O as outbox and<br/>relay to Kafka
    H->>U: TrailerDocked event id,<br/>appointment_id, door_code
    U->>O: processed_events contains<br/>event id?
    alt already processed
        U-->>H: ack, no effect
    else new
        U->>R: Get appointment
        alt unknown, CANCELLED or NO_SHOW
            U->>O: insert processed_events<br/>row
            U-->>H: ack, ignored and counted
        else door equals the booked door
            U->>O: insert processed_events<br/>row
            U-->>H: ack, no change
        else door differs
            U->>A: ReassignDoor to door_code,<br/>no overlap refusal
            A-->>U: door changed
            U->>R: later appointments<br/>overlapping on the new<br/>door
            Note over U,O: one transaction
            U->>R: update where version<br/>matches
            alt version changed
                R-->>U: conflict
                U-->>H: rollback, redeliver
            else saved
                U->>O: insert<br/>DockAppointmentDoorReassigned
                U->>O: insert<br/>DockAppointmentAtRisk per<br/>overlapped appointment
                U->>O: insert processed_events<br/>row
                U-->>H: ack
            end
        end
    end
    Note over O: after commit the relay<br/>publishes DoorReassigned to the events<br/>and analytics topics, AtRisk to analytics

```

Source: `internal/application/usecases/facts.go`, `internal/application/usecases/yard.go`, `internal/application/usecases/change.go`, `internal/adapters/outbound/kafka/encoder.go`, `apis/asyncapi.yaml`.

Omitted: `ErrDoorDoubleBooked` is deliberately not raised: the physical fact wins. `BOOKED` and `CHECKED_IN` accept the command, and a same-door event changes nothing. The reassignment is also written to the integration topic (service ADR 0010), so the closing note's analytics topic is not the only target.

## 7. RecordDoorRelease

Policy `CompleteOnDoorRelease` on `TrailerUndocked` naming an `appointment_id`.

```mermaid
%%{init: {"sequence": {"wrap": true, "width": 120, "actorMargin": 24, "messageMargin": 28, "boxMargin": 6, "noteMargin": 6, "mirrorActors": false}}}%%
sequenceDiagram
    autonumber
    participant H as kafka<br/>consumer
    participant U as use case
    participant A as Dock<br/>Appointment
    participant R as repositories<br/>and views
    participant O as outbox and<br/>relay to Kafka
    H->>U: TrailerUndocked event id,<br/>appointment_id,<br/>undocked_at
    U->>O: processed_events contains<br/>event id?
    alt already processed
        U-->>H: ack, no effect
    else new
        U->>R: Get appointment
        alt unknown, CANCELLED or NO_SHOW
            U->>O: insert processed_events<br/>row
            U-->>H: ack, ignored and counted
        else COMPLETED already
            U->>O: insert processed_events<br/>row
            U-->>H: ack, no change
        else CHECKED_IN or BOOKED
            U->>A: RecordDoorRelease<br/>undocked_at
            A-->>U: COMPLETED, arrival<br/>implicit if it was BOOKED
            Note over U,O: one transaction
            U->>R: update where version<br/>matches
            alt version changed
                R-->>U: conflict
                U-->>H: rollback, redeliver
            else saved
                U->>O: insert<br/>DockAppointmentCompleted
                U->>O: insert processed_events<br/>row
                U-->>H: ack
            end
        end
    end
    Note over O: after commit the relay<br/>publishes to warehouse.<br/>dock-scheduling.analytics

```

Source: `internal/domain/dockappointment/appointment.go`, `internal/domain/dockappointment/commands.go`, `internal/domain/dockappointment/events.go`, `internal/application/usecases/facts.go`, `internal/application/usecases/yard.go`, `internal/application/usecases/change.go`.

Omitted: no `DockAppointmentCheckedIn` is written for an implicit arrival (the adherence report counts it as `ON_TIME`); one appointment covers exactly one trailer (decision X3).

## 8. MarkNoShow

Scheduler (cron), REST `POST /no-show-sweeps`, policy `NoShowSweep`.

```mermaid
%%{init: {"sequence": {"wrap": true, "width": 120, "actorMargin": 24, "messageMargin": 28, "boxMargin": 6, "noteMargin": 6, "mirrorActors": false}}}%%
sequenceDiagram
    autonumber
    participant H as http<br/>handler
    participant U as use case
    participant A as Dock<br/>Appointment
    participant R as repositories<br/>and views
    participant O as outbox and<br/>relay to Kafka
    H->>U: MarkNoShow<br/>POST /no-show-sweeps
    U->>R: BOOKED appointments past<br/>window_end<br/>plus no-show grace
    loop each qualifying appointment
        U->>A: MarkNoShow now, grace
        A-->>U: NO_SHOW
        Note over U,O: one transaction per<br/>appointment
        U->>R: update where version<br/>matches
        alt version changed
            R-->>U: conflict
            Note over U: skipped, the next sweep<br/>retries
        else saved
            U->>O: insert<br/>DockAppointmentNoShow
        end
    end
    U-->>H: 200 number marked
    Note over O: after commit the relay<br/>publishes to warehouse.<br/>dock-scheduling.events

```

Source: `internal/domain/dockappointment/appointment.go`, `internal/domain/dockappointment/commands.go`, `internal/domain/dockappointment/events.go`, `internal/application/usecases/facts.go`, `internal/application/usecases/yard.go`, `internal/application/usecases/change.go`, `internal/adapters/outbound/kafka/encoder.go`, `apis/asyncapi.yaml`.

Omitted: the idempotency replay (diagram 1). A rerun selects only what still qualifies.

## 9. ImportLegacyAppointment

Policy `LegacyImport` (strangler stage A), on `inbound-receiving` `DockAppointment*` events; off when its group env is unset.

```mermaid
%%{init: {"sequence": {"wrap": true, "width": 120, "actorMargin": 24, "messageMargin": 28, "boxMargin": 6, "noteMargin": 6, "mirrorActors": false}}}%%
sequenceDiagram
    autonumber
    participant H as kafka<br/>consumer
    participant U as use case
    participant A as Dock<br/>Appointment
    participant R as repositories<br/>and views
    participant O as outbox and<br/>relay to Kafka
    H->>U: legacy<br/>DockAppointmentBooked<br/>event id, door, window
    U->>O: processed_events contains<br/>event id?
    alt already processed
        U-->>H: ack, no effect
    else new
        U->>R: legacy_appointment_id<br/>already imported?
        alt imported
            U->>O: insert processed_events<br/>row
            U-->>H: ack, import once
        else not yet
            U->>R: DoorView for the site of<br/>the door
            U->>A: ImportLegacyAppointment<br/>provenance legacy-import,<br/>direction INBOUND
            A-->>U: appointment
            Note over U,O: one transaction
            U->>R: insert appointment
            U->>O: insert<br/>DockAppointmentBooked
            U->>O: insert processed_events<br/>row
            U-->>H: ack
        end
    end
    Note over O: after commit the relay<br/>publishes to warehouse.<br/>dock-scheduling.events

```

Source: `internal/application/usecases/facts.go`, `internal/application/usecases/yard.go`, `internal/application/usecases/change.go`, `internal/adapters/inbound/kafka/yard_consumer.go`, `internal/adapters/inbound/kafka/facility_consumer.go`, `internal/adapters/inbound/kafka/asn_consumer.go`, `internal/adapters/inbound/kafka/cutoff_consumer.go`, `internal/adapters/inbound/kafka/legacy_import_consumer.go`.

Omitted: which invariants an import enforces, how the legacy check-in, cancel and complete events are applied and whether the import raises `DockAppointmentBooked` are settled by decision D-2: the import enforces no invariant, later legacy events move only `legacy-import` appointments, and `DockAppointmentBooked` is raised; the diagram shows that.

## 10. DeclareOperatingHours

Dock planner, REST `PUT /sites/siteId/dock-calendar/operating-hours`.

```mermaid
%%{init: {"sequence": {"wrap": true, "width": 120, "actorMargin": 24, "messageMargin": 28, "boxMargin": 6, "noteMargin": 6, "mirrorActors": false}}}%%
sequenceDiagram
    autonumber
    participant H as http<br/>handler
    participant U as use case
    participant A as Dock<br/>Calendar
    participant R as repositories<br/>and views
    participant O as outbox and<br/>relay to Kafka
    H->>U: DeclareOperatingHours<br/>site, door, weekday rows,<br/>Idempotency-Key
    U->>R: Get DockCalendar of the<br/>site
    U->>A: DeclareOperatingHours rows
    alt opens_at not before closes_at
        A-->>U: ErrInvalidOperatingHours
        U-->>H: 422 problem+json
    else valid
        A-->>U: hours replaced
        Note over U,O: one transaction
        U->>R: upsert calendar where<br/>version matches
        alt version changed
            R-->>U: conflict
            U-->>H: 409 concurrent-update
        else saved
            U->>O: insert<br/>DoorOperatingHoursDeclared
            U-->>H: 200 calendar and ETag
        end
    end
    Note over O: after commit the relay<br/>publishes to warehouse.<br/>dock-scheduling.analytics

```

Source: `internal/domain/dockcalendar/calendar.go`, `internal/domain/dockcalendar/events.go`.

Omitted: the idempotency replay (diagram 1). The calendar is created lazily with the defaults at first use (decision D-1); new hours that leave an active appointment outside them are refused with `ErrHoursConflictWithAppointments` (decision D-8), a branch this diagram does not draw.

## 11. DeclareCalendarSettings

Dock planner, REST `PUT /sites/siteId/dock-calendar/settings`.

```mermaid
%%{init: {"sequence": {"wrap": true, "width": 120, "actorMargin": 24, "messageMargin": 28, "boxMargin": 6, "noteMargin": 6, "mirrorActors": false}}}%%
sequenceDiagram
    autonumber
    participant H as http<br/>handler
    participant U as use case
    participant A as Dock<br/>Calendar
    participant R as repositories<br/>and views
    H->>U: DeclareCalendarSettings<br/>max window, granularity,<br/>tolerance, grace,<br/>Idempotency-Key
    U->>R: Get DockCalendar of the<br/>site
    U->>A: DeclareCalendarSettings<br/>settings
    alt a value is not valid
        A-->>U: ErrInvalidSettings
        U-->>H: 422 invalid-settings
    else valid
        A-->>U: settings replaced
        U->>R: upsert calendar where<br/>version matches
        alt version changed
            R-->>U: conflict
            U-->>H: 409 concurrent-update
        else saved
            U-->>H: 200 calendar and ETag
        end
    end

```

Source: `internal/domain/dockcalendar/calendar.go`, `internal/domain/dockcalendar/events.go`.

Omitted: no outbox row and no relay: the service raises no event for this command (decision D-11 fixes the bounds). The idempotency replay is as in diagram 1.

## 12. ScheduleDoorClosure

Dock planner, REST `POST /sites/siteId/dock-calendar/closures`.

```mermaid
%%{init: {"sequence": {"wrap": true, "width": 120, "actorMargin": 24, "messageMargin": 28, "boxMargin": 6, "noteMargin": 6, "mirrorActors": false}}}%%
sequenceDiagram
    autonumber
    participant H as http<br/>handler
    participant U as use case
    participant A as Dock<br/>Calendar
    participant R as repositories<br/>and views
    participant O as outbox and<br/>relay to Kafka
    H->>U: ScheduleDoorClosure door,<br/>from, to, reason,<br/>Idempotency-Key
    U->>R: Get DockCalendar of the<br/>site
    U->>A: ScheduleDoorClosure<br/>closure
    alt overlaps a closure of the door
        A-->>U: ErrOverlappingClosure
        U-->>H: 409 problem+json
    else no closure overlap
        U->>R: active appointments of the<br/>door in the period
        alt at least one active appointment
            R-->>U: appointment ids
            U-->>H: 409<br/>ErrClosureConflictsWithAppointments<br/>lists the ids
        else none
            A-->>U: closure added
            Note over U,O: one transaction
            U->>R: update calendar where<br/>version matches
            alt version changed
                R-->>U: conflict
                U-->>H: 409 concurrent-update
            else saved
                U->>O: insert<br/>DoorClosureScheduled
                U-->>H: 201 closure
            end
        end
    end
    Note over O: after commit the relay<br/>publishes to warehouse.<br/>dock-scheduling.events

```

Source: `internal/domain/dockcalendar/calendar.go`, `internal/domain/dockcalendar/events.go`, `internal/adapters/outbound/kafka/encoder.go`, `apis/asyncapi.yaml`.

Omitted: the idempotency replay (diagram 1). The active appointments are read from another aggregate while the door's advisory lock and the calendar row lock are held, so a booking on that door cannot commit between the read and the save.

## 13. LiftDoorClosure

Dock planner, REST `POST /sites/siteId/dock-calendar/closures/closureId/lifts`.

```mermaid
%%{init: {"sequence": {"wrap": true, "width": 120, "actorMargin": 24, "messageMargin": 28, "boxMargin": 6, "noteMargin": 6, "mirrorActors": false}}}%%
sequenceDiagram
    autonumber
    participant H as http<br/>handler
    participant U as use case
    participant A as Dock<br/>Calendar
    participant R as repositories<br/>and views
    participant O as outbox and<br/>relay to Kafka
    H->>U: LiftDoorClosure closureId,<br/>Idempotency-Key
    U->>R: Get DockCalendar of the<br/>site
    U->>A: LiftDoorClosure closureId
    alt closure not found
        A-->>U: ErrUnknownClosure
        U-->>H: 404 unknown-closure
    else found
        A-->>U: closure removed
        Note over U,O: one transaction
        U->>R: update calendar where<br/>version matches
        alt version changed
            R-->>U: conflict
            U-->>H: 409 concurrent-update
        else saved
            U->>O: insert DoorClosureLifted
            U-->>H: 200 calendar
        end
    end
    Note over O: after commit the relay<br/>publishes to warehouse.<br/>dock-scheduling.events

```

Source: `internal/domain/dockcalendar/calendar.go`, `internal/domain/dockcalendar/events.go`, `internal/adapters/outbound/kafka/encoder.go`, `apis/asyncapi.yaml`.

Omitted: the idempotency replay (diagram 1).
