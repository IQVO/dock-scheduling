---
id: class-diagram
title: Class Diagram
sidebar_label: Class Diagram
description: "UML class diagrams of the Dock Scheduling domain (DockAppointment, DockCalendar, events, policies, read models) and its hexagonal ports and adapters."
---

# Class Diagram

UML class diagrams of the `internal/domain/**` packages
(`dockappointment`, `dockcalendar`, `shared`), then the hexagonal ports and
adapters in the Go layout the fleet uses. Type and field names come from
the yard design model; method names are the design model's commands. Fields are shown `-`
(unexported), methods `+`.

In the code: the package names are the design model's. The Go methods of the root are shorter than
the command names drawn here (`Reschedule`, `Cancel`, `ReportEta`, `MarkAtRisk`,
`RecordArrival`, `ReassignDoor`, `RecordDoorRelease`, `MarkNoShow`), booking is a
constructor, the state type is `State`, the per-site `Rules` value (max window, granularity,
arrival tolerance, no-show grace) is passed to the methods that need it, and the repository is
`AppointmentRepo` (diagram 1c). The ports and adapters are in the code in diagram 5; the drawn
fields and methods are not a copy of the code.

## 1. DockAppointment aggregate

### 1a. Root and states

```mermaid
classDiagram
    direction TB
    class DockAppointment {
        <<AggregateRoot>>
        -appointmentId string
        -siteId string
        -direction Direction
        -doorCode string
        -window Window
        -carrier Carrier
        -inboundRefs InboundRefs
        -outboundRefs OutboundRefs
        -expectedSeal ExpectedSeal
        -eta Eta
        -arrivalStatus ArrivalStatus
        -state AppointmentState
        -provenance string
        -version int
        +BookAppointment(cmd)$ DockAppointment
        +RescheduleAppointment(window, now) error
        +CancelAppointment(reason) error
        +ReportEta(eta, tolerance) error
        +RecordArrival(arrivedAt, tolerance) error
        +ReassignDoor(toDoorCode) error
        +RecordDoorRelease(at) error
        +MarkNoShow(now, grace) error
    }
    class AppointmentState {
        <<Enumeration>>
        BOOKED
        CHECKED_IN
        COMPLETED
        CANCELLED
        NO_SHOW
    }
    class Direction {
        <<Enumeration>>
        INBOUND
        OUTBOUND
    }
    class ArrivalStatus {
        <<Enumeration>>
        EARLY
        ON_TIME
        LATE
    }
    DockAppointment --> AppointmentState
    DockAppointment --> Direction
    DockAppointment --> ArrivalStatus
```

Source: `internal/domain/dockappointment/`, `internal/domain/dockcalendar/`, `internal/domain/shared/shared.go`, `internal/application/ports/ports.go`, `internal/adapters/`.

### 1b. Value objects

```mermaid
classDiagram
    direction LR
    class DockAppointment {
        <<AggregateRoot>>
    }
    class Window {
        <<ValueObject>>
        windowStart time
        windowEnd time
    }
    class Carrier {
        <<ValueObject>>
        name string
        scac SCAC
    }
    class InboundRefs {
        <<ValueObject>>
        asnNumbers string[]
    }
    class OutboundRefs {
        <<ValueObject>>
        cptId string
        shipmentRefs string[]
    }
    class Eta {
        <<ValueObject>>
        etaAt time
        reportedAt time
        source string
    }
    class ExpectedSeal {
        <<ValueObject>>
        sealNumber string
    }
    DockAppointment *-- Window
    DockAppointment *-- Carrier
    DockAppointment *-- InboundRefs
    DockAppointment *-- OutboundRefs
    DockAppointment *-- Eta
    DockAppointment *-- ExpectedSeal
```

Source: `internal/domain/dockappointment/`, `internal/domain/dockcalendar/`, `internal/domain/shared/shared.go`, `internal/application/ports/ports.go`, `internal/adapters/`.

### 1c. Domain service and repository

```mermaid
classDiagram
    direction LR
    class DockAppointment {
        <<AggregateRoot>>
    }
    class DoorSchedule {
        <<DomainService>>
        +CheckNoOverlap(doorCode, window, active) error
    }
    class AppointmentRepo {
        <<Repository>>
        +Get(id) DockAppointment
        +GetForUpdate(id) DockAppointment
        +Save(appointment, expectedVersion) error
        +LockDoor(doorCode) error
        +ListActiveOnDoor(doorCode) DockAppointment[]
        +ListBookedEndedBy(t, limit) string[]
    }
    DoorSchedule ..> DockAppointment : reads active
    AppointmentRepo ..> DockAppointment : persists
```

Source: `internal/domain/dockappointment/appointment.go`, `internal/domain/dockappointment/commands.go`, `internal/domain/dockappointment/events.go`, `internal/adapters/outbound/kafka/encoder.go`, `apis/asyncapi.yaml`.

Omitted: the error variables (see
[Aggregate Design Canvas](/docs/ddd/aggregate-design-canvas));
`version` handling beyond the field. `siteId` and `provenance` are in the
event payloads and not in the aggregate's value-object list; they are drawn
as fields on this page's reading. `Window` is the design model's `Window` with
`start` and `end`; the fields are named `windowStart` and `windowEnd` here to
avoid the diagram keyword `end`. `BookAppointment(cmd)` stands for the
booking inputs (door, direction, window, carrier, references, expected
seal). The advisory that `ReportEta` may raise (`DockAppointmentAtRisk`) is
an event, not a return value. The commands that policies drive
(`RecordArrival`, `ReassignDoor`, `RecordDoorRelease`) have no REST route. `ListActiveOnDoor` and
`ListBookedEndedBy` are repository queries the rules need (the overlap check and
the no-show sweep); `LockDoor` takes the per-door advisory lock for the rest of the unit of work, and
`Save` is guarded by the version the caller loaded. `DoorSchedule` is the domain service named by
`ErrDoorDoubleBooked`; the checks that need `DoorView`, `AsnView`,
`CutoffView` and `DockCalendar` are in diagram 4.

## 2. DockCalendar aggregate

```mermaid
classDiagram
    direction TB
    class DockCalendar {
        <<AggregateRoot>>
        -siteId string
        -operatingHours OperatingHours[]
        -closures Closure[]
        -settings Settings
        -version int
        +DeclareOperatingHours(rows) error
        +DeclareCalendarSettings(settings) error
        +ScheduleDoorClosure(closure, activeAppointments) error
        +LiftDoorClosure(closureId) error
    }
    class OperatingHours {
        <<ValueObject>>
        doorCode string
        weekday int
        opensAt time
        closesAt time
        timezone string
    }
    class Closure {
        <<ValueObject>>
        closureId string
        doorCode string
        from time
        to time
        reason string
    }
    class Settings {
        <<ValueObject>>
        maxWindowMinutes int
        slotGranularityMinutes int
        arrivalToleranceMinutes int
        noShowGraceMinutes int
    }
    class DockCalendarRepository {
        <<Repository>>
        +Get(siteId) DockCalendar
        +Save(calendar, events) error
    }
    DockCalendar *-- OperatingHours
    DockCalendar *-- Closure
    DockCalendar *-- Settings
    DockCalendarRepository ..> DockCalendar : persists
```

Source: `internal/domain/dockcalendar/calendar.go`, `internal/domain/dockcalendar/events.go`, `internal/adapters/outbound/kafka/encoder.go`, `apis/asyncapi.yaml`.

Omitted: `ErrInvalidOperatingHours`, `ErrOverlappingClosure` and
`ErrClosureConflictsWithAppointments`. `activeAppointments` is a parameter of
`ScheduleDoorClosure` because the refusal needs the appointments of the door
and lists their ids; the two aggregates stay separate (this page's reading).
`doorCode` on `OperatingHours` is empty for the site default. The design model gives
no `weekday` type and states no bound for the settings.

## 3. Appointment events and the policies that raise them

### 3a. Yard facts and ETA risk

```mermaid
classDiagram
    direction LR
    class DockAppointmentCheckedIn {
        <<DomainEvent>>
        appointment_id, site_id
        door_code, checked_in_at
        arrival_status, visit_id, version
    }
    class DockAppointmentDoorReassigned {
        <<DomainEvent>>
        appointment_id, site_id
        from_door_code, to_door_code
        version
    }
    class DockAppointmentCompleted {
        <<DomainEvent>>
        appointment_id, site_id
        door_code, completed_at, version
    }
    class DockAppointmentAtRisk {
        <<DomainEvent>>
        appointment_id, site_id, door_code
        eta_at, window_start, window_end
        cause
    }
    class ArrivalFromGate {
        <<Policy>>
        +OnVisitCheckedIn()
    }
    class ActualDoorWins {
        <<Policy>>
        +OnTrailerDocked()
    }
    class CompleteOnDoorRelease {
        <<Policy>>
        +OnTrailerUndocked()
    }
    class EtaRisk {
        <<Policy>>
        +OnEtaReported()
    }
    ArrivalFromGate ..> DockAppointmentCheckedIn : RecordArrival
    ActualDoorWins ..> DockAppointmentDoorReassigned : ReassignDoor
    ActualDoorWins ..> DockAppointmentAtRisk : overlap
    CompleteOnDoorRelease ..> DockAppointmentCompleted : RecordDoorRelease
    EtaRisk ..> DockAppointmentAtRisk : late ETA
```

Source: `internal/domain/dockappointment/`, `internal/domain/dockcalendar/`, `internal/domain/shared/shared.go`, `internal/application/ports/ports.go`, `internal/adapters/`.

### 3b. Booking, cancelling, no-show and import

```mermaid
classDiagram
    direction LR
    class DockAppointmentBooked {
        <<DomainEvent>>
        appointment_id, site_id
        direction, door_code
        carrier, carrier_scac
        window_start, window_end
        asn_numbers, cpt_id
        shipment_refs
        expected_seal_number
        provenance, version
    }
    class DockAppointmentRescheduled {
        <<DomainEvent>>
        appointment_id, site_id
        door_code
        window_start, window_end
        previous_window_start
        previous_window_end, version
    }
    class DockAppointmentCancelled {
        <<DomainEvent>>
        appointment_id, site_id
        door_code, reason, version
    }
    class DockAppointmentNoShow {
        <<DomainEvent>>
        appointment_id, site_id
        door_code, direction
        carrier_scac, window_end
        version
    }
    class NoShowSweep {
        <<Policy>>
        +OnSweepRequested()
    }
    class CancelWhenAllAsnsCancelled {
        <<Policy>>
        +OnAsnCancelled()
    }
    NoShowSweep ..> DockAppointmentNoShow : MarkNoShow
    CancelWhenAllAsnsCancelled ..> DockAppointmentCancelled : CancelAppointment
```

Source: `internal/application/usecases/facts.go`, `internal/application/usecases/yard.go`, `internal/application/usecases/change.go`, `internal/adapters/outbound/kafka/encoder.go`, `apis/asyncapi.yaml`.

Omitted: the CloudEvents envelope (see
[Domain Events](/docs/ddd/domain-events));
`PickupFromCutoffs` (phase 2, names no command or event of its own);
`OutOfOrderYardFacts`, which is a rule on `RecordDoorRelease` and
`RecordArrival` rather than a source of events. `BookAppointment`,
`RescheduleAppointment` and `CancelAppointment` by a carrier raise
`DockAppointmentBooked`, `Rescheduled` and `Cancelled` directly, not through
a policy; `DockAppointmentRescheduled` has no policy at all. The `LegacyImport` policy that once raised
`DockAppointmentBooked` was removed (ADR 0011).

## 4. Read models and the checks that use them

```mermaid
classDiagram
    direction LR
    class DoorView {
        <<ReadModel>>
        locationCode, zoneId
        role, dockFlow
        ErrUnknownDoor
        ErrDoorFlowMismatch
    }
    class DockCalendar {
        <<AggregateRoot>>
        hours, closures
        settings
        ErrDoorUnavailable
    }
    class AsnView {
        <<ReadModel>>
        asn_number, lines
        expected_arrival
        ErrUnknownAsn
    }
    class CutoffView {
        <<ReadModel>>
        site_id, timezone
        cutoffs
        ErrMissingOutboundRef
    }
    class DockAppointment {
        <<AggregateRoot>>
        BookAppointment
        RescheduleAppointment
    }
    class DoorAvailabilityView {
        <<ReadModel>>
        free windows per door
    }
    class AdherenceReport {
        <<ReadModel>>
        on-time, early, late
        no-show per site
        and carrier
    }
    DoorView ..> DockAppointment
    DockCalendar ..> DockAppointment
    AsnView ..> DockAppointment
    CutoffView ..> DockAppointment
    DockAppointment ..> DoorAvailabilityView
    DockCalendar ..> DoorAvailabilityView
    DockAppointment ..> AdherenceReport
```

Source: `internal/domain/dockappointment/appointment.go`, `internal/domain/dockappointment/commands.go`, `internal/domain/dockappointment/events.go`, `internal/adapters/inbound/kafka/yard_consumer.go`, `internal/adapters/inbound/kafka/facility_consumer.go`, `internal/adapters/inbound/kafka/asn_consumer.go`, `internal/adapters/inbound/kafka/cutoff_consumer.go`, `internal/application/ports/ports.go`, `migrations/0001_dock_scheduling.up.sql`.

Reading the diagram: an arrow into `DockAppointment` is a check a booking
makes against that copy, and the `Err...` listed in the source box is the
refusal. `DoorAvailabilityView` is built from `DockAppointment` and
`DockCalendar` state; `AdherenceReport` is built from the appointment events
on the analytics topic.

Omitted: the projectors that fill `DoorView`, `AsnView` and `CutoffView`
(shown in the ports view below). `DockCalendar` is an aggregate of this
context, not a copy; it is drawn here because a booking reads it. The
`cancelled` flag of an ASN is this page's reading of how `ASNCancelled` is
stored.

## 5. Hexagonal ports and adapters

### 5a. Inbound adapters, use cases and domain

```mermaid
flowchart LR
    subgraph IN["internal/adapters/inbound"]
        HTTP["http<br/>REST and cron calls"]
        KAF["kafka<br/>consumers"]
    end
    subgraph APP["internal/application/usecases"]
        UC1["Appointment<br/>Book, Reschedule,<br/>Cancel, ReportEta,<br/>MarkNoShow"]
        UC2["Yard facts<br/>RecordArrival,<br/>ReassignDoor,<br/>RecordDoorRelease"]
        UC3["Calendar<br/>hours, settings,<br/>closures"]
        PRJ["projectors<br/>DoorView, AsnView,<br/>CutoffView,<br/>AdherenceReport"]
    end
    subgraph DOM["internal/domain"]
        D1["dockappointment"]
        D2["dockcalendar"]
        D3["shared"]
    end
    HTTP --> UC1
    HTTP --> UC3
    KAF --> UC1
    KAF --> UC2
    KAF --> PRJ
    UC1 --> D1
    UC2 --> D1
    UC3 --> D2
    D1 --> D3
```

Source: `internal/domain/dockappointment/`, `internal/domain/dockcalendar/`, `internal/domain/shared/shared.go`, `internal/application/ports/ports.go`, `internal/adapters/`.

### 5b. Ports and outbound adapters

```mermaid
flowchart LR
    subgraph APP["internal/application/usecases"]
        UC["every use case<br/>and projector"]
    end
    subgraph PORTS["internal/application/ports"]
        P1["AppointmentRepo and<br/>CalendarRepo"]
        P2["DoorView, AsnView,<br/>CutoffView"]
        P3["EventPublisher outbox"]
        P4["ProcessedEvents, UnitOfWork,<br/>IdempotencyStore, Clock, IDGen"]
    end
    subgraph OUT["internal/adapters/outbound"]
        PG["postgres"]
        KP["kafka relay"]
    end
    UC -.-> P1
    UC -.-> P2
    UC -.-> P3
    UC -.-> P4
    P1 --- PG
    P2 --- PG
    P3 --- PG
    P4 --- PG
    PG --> KP
```

Source: `internal/application/usecases/facts.go`, `internal/application/usecases/yard.go`, `internal/application/usecases/change.go`, `internal/adapters/inbound/kafka/yard_consumer.go`, `internal/adapters/inbound/kafka/facility_consumer.go`, `internal/adapters/inbound/kafka/asn_consumer.go`, `internal/adapters/inbound/kafka/cutoff_consumer.go`, `internal/application/ports/ports.go`, `migrations/0001_dock_scheduling.up.sql`, `internal/adapters/inbound/http/errors.go`.

All paths are under `internal/`. The dashed arrows mean every use case calls
ports only: the repository of its aggregate, the event publisher (outbox),
the inbox (`ProcessedEvents`), the unit of work, the clock and the id generator; the per-door advisory
lock is `AppointmentRepo.LockDoor`, and the read-model ports (`DoorView`, `AsnView`,
`CutoffView`) are separate interfaces. Report readers (`AdherenceReader`,
`FreshnessReader`) belong to the `reports` binary. The
`http` adapter drives the carrier and planner commands and the sweep, which
a Kubernetes CronJob calls over REST, so there is no `scheduler` adapter.
The `kafka` adapter drives the policies (`ArrivalFromGate`, `ActualDoorWins`,
`CompleteOnDoorRelease`, `CancelWhenAllAsnsCancelled`) and the
projectors; the `AdherenceReport` projector reads this context's own
analytics topic.

Omitted: the HTTP read path (`GET` routes read the repositories and
`DoorAvailabilityView` directly); the MCP adapter (`internal/adapters/inbound/mcp`, nine tools: five
L1 reads and four L3 writes, service ADR 0007), the analytics projector and the report binary. There are no outbound HTTP clients: `sync_calls` is empty.
