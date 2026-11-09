---
id: entity-relationship
title: Entity Relationship
sidebar_label: Entity Relationship
description: "The Postgres schema of Dock Scheduling, with one table per aggregate root, child tables, read-model tables and the outbox, inbox and migration infrastructure."
---

# Entity Relationship

The Postgres schema, from the service's migrations
(`migrations/0001_dock_scheduling` and `0002_idempotency_keys` for the OLTP database,
`analytics/migrations/0001_schedule_adherence` for the analytics database). This context owns its
database; no table is shared with another context (`conventions.integration_rules`). A relationship
line is drawn only where a foreign key exists, and only inside one aggregate. The earlier version of
this page proposed table names (`appointment_asns`, `rm_doors`, `outbox_events`, ...); they are
replaced by the real ones below.

## Aggregate tables

### DockAppointment

```mermaid
erDiagram
    direction TB
    dock_appointments ||--o{ asn_claims : "FK appointment_id"
    dock_appointments {
        TEXT appointment_id PK "appt-uuid"
        TEXT site_id
        TEXT door_code "value, no FK"
        TEXT direction "INBOUND, OUTBOUND"
        TEXT carrier
        TEXT carrier_scac "empty when none"
        TIMESTAMPTZ window_start
        TIMESTAMPTZ window_end "CHECK gt window_start"
        TEXT_ARRAY asn_numbers
        TEXT cpt_id "empty when none"
        TEXT_ARRAY shipment_refs
        TEXT expected_seal
        TEXT provenance "native, legacy-import"
        BOOLEAN door_override "physical fact set the door"
        TEXT state "BOOKED .. NO_SHOW"
        BIGINT version "CHECK ge 1"
        TIMESTAMPTZ eta_at "nullable, with eta_source"
        TIMESTAMPTZ arrival_at "nullable, with arrival_status"
        TEXT arrival_status "EARLY, ON_TIME, LATE"
        BOOLEAN arrival_implicit
        TEXT at_risk_cause "LATE_ETA, DOOR_REASSIGNED_OVERLAP"
        TEXT cancellation_reason
        TIMESTAMPTZ completed_at
    }
    asn_claims {
        TEXT asn_number PK
        TEXT appointment_id FK
    }
```

Source: `migrations/0001_dock_scheduling.up.sql`, `migrations/0002_idempotency_keys.up.sql`, `analytics/migrations/0001_schedule_adherence.up.sql`.

### DockCalendar

```mermaid
erDiagram
    direction TB
    dock_calendars ||--o{ calendar_operating_hours : "FK site_id"
    dock_calendars ||--o{ calendar_closures : "FK site_id"
    dock_calendars {
        TEXT site_id PK
        INTEGER max_window_minutes
        INTEGER slot_granularity_minutes
        INTEGER arrival_tolerance_minutes
        INTEGER no_show_grace_minutes
        BIGINT version
    }
    calendar_operating_hours {
        TEXT site_id PK
        TEXT door_code PK "empty is the site default"
        TEXT weekday PK "MON .. SUN"
        TEXT opens_at
        TEXT closes_at
        TEXT timezone
        INTEGER position
    }
    calendar_closures {
        TEXT site_id PK
        TEXT closure_id PK
        TEXT door_code
        TIMESTAMPTZ from_at
        TIMESTAMPTZ to_at "CHECK gt from_at"
        TEXT reason
        BOOLEAN lifted "kept for history"
        INTEGER position
    }
```

Source: `internal/domain/dockappointment/appointment.go`, `internal/domain/dockappointment/commands.go`, `internal/domain/dockappointment/events.go`, `internal/domain/dockcalendar/calendar.go`, `internal/domain/dockcalendar/events.go`, `internal/adapters/outbound/kafka/encoder.go`, `apis/asyncapi.yaml`.

Omitted: the plain indexes (by window start, by site, by door among active rows, `BOOKED` by window end for the
no-show sweep, a partial index on `at_risk_cause`, a GIN index on `asn_numbers` for
`CancelWhenAllAsnsCancelled`). Two database backstops enforce invariants: the GiST **exclusion
constraint** `ex_dock_appointments_door_window` on `(door_code, tstzrange(window_start, window_end, '[)'))`
for `BOOKED` and `CHECKED_IN` rows that are native and not `door_override` (a losing writer
fails with `23P01`, mapped to `ErrDoorDoubleBooked`; the domain service under the per-door advisory
lock decides first), and the primary key of `asn_claims` (one row per ASN of every active native
appointment, `ErrAsnAlreadyScheduled`). Legacy imports and `ActualDoorWins` rows are deliberately exempt.
Comparison operators are written `lt` / `gt` / `ge` inside the diagrams, `..`
abbreviates the full state list, and an array column is written `TEXT_ARRAY`. `site_id` is the `siteCode`
found through the door's zone, never parsed from a code. Text keys that are compared or ordered in Go use
the `C` collation. The extension `btree_gist` is required.

## Read-model tables

```mermaid
erDiagram
    direction LR
    zones {
        TEXT zone_id PK
        TEXT site_code
    }
    doors {
        TEXT door_code PK "locationCode of a Dock slot"
        TEXT zone_id "value, no FK"
        TEXT dock_flow "Inbound, Outbound, Both"
    }
    asns {
        TEXT asn_number PK
        TEXT status "REGISTERED, CANCELLED"
        TIMESTAMPTZ expected_arrival "nullable"
        TIMESTAMPTZ updated_at
    }
    cutoff_schedules ||--o{ cutoffs : "FK site_id"
    cutoff_schedules {
        TEXT site_id PK
        TEXT timezone
    }
    cutoffs {
        TEXT site_id PK
        TEXT cpt_id PK
        TEXT local_time
        TEXT_ARRAY days_of_week
        TEXT ship_method
    }
```

Source: `internal/adapters/inbound/kafka/yard_consumer.go`, `internal/adapters/inbound/kafka/facility_consumer.go`, `internal/adapters/inbound/kafka/asn_consumer.go`, `internal/adapters/inbound/kafka/cutoff_consumer.go`, `internal/application/ports/ports.go`, `migrations/0001_dock_scheduling.up.sql`.

Omitted: `DoorAvailabilityView`, which is computed from `dock_appointments`,
`calendar_operating_hours` and `calendar_closures` and has no table of its own. `doors` holds only Dock
slots (role Dock) and `door_code` is the slot's `locationCode`; a decommission deletes the row. There
is no foreign key from `doors.zone_id` to `zones` because the two events may arrive in either order: a
door whose zone has not arrived is held pending, cannot be booked, and the join to the site is made at use
time (decision X5). `cutoff_schedules` is replaced as a whole per site by `CPTScheduleChanged`. A cancelled
ASN stays cancelled.

### Analytics database

The adherence report reads a **separate analytics database**, written only by the `projector` binary
and read only by the `reports` binary, rebuilt by replaying the analytics topic.

```mermaid
erDiagram
    direction LR
    analytics_processed_events {
        TEXT event_id PK
        TEXT event_type
        TIMESTAMPTZ occurred_at
        TIMESTAMPTZ applied_at
    }
    appointment_facts {
        TEXT appointment_id PK
        TEXT site_id
        TEXT carrier_scac
        TIMESTAMPTZ window_start
        TIMESTAMPTZ window_end
        BIGINT window_version "older events never overwrite"
        TIMESTAMPTZ booked_at
        TIMESTAMPTZ checked_in_at
        TIMESTAMPTZ completed_at
        TIMESTAMPTZ cancelled_at
        TIMESTAMPTZ no_show_at
        TEXT arrival_status
        TEXT cancellation_reason
        TEXT outcome "generated: status, ON_TIME or NO_SHOW"
        TIMESTAMPTZ ref_at "generated: the report range instant"
    }
```

Source: `migrations/0001_dock_scheduling.up.sql`, `migrations/0002_idempotency_keys.up.sql`, `analytics/migrations/0001_schedule_adherence.up.sql`.

`appointment_facts` has one row per appointment and one column per milestone, kept with
`LEAST`/`COALESCE`, so arrival order never matters. `outcome` is the adherence classification, computed by
the database: an arrival counts as its status, a completion without a check-in (an implicit arrival) as
`ON_TIME`, a no-show as `NO_SHOW`; an appointment that was only booked or was cancelled before anyone
arrived has no outcome and is not counted.

## Infrastructure tables

```mermaid
erDiagram
    direction LR
    outbox {
        BIGSERIAL id PK
        TEXT event_id UK "CloudEvents id, minted once"
        TEXT aggreg_type
        TEXT aggreg_id "aggregate root id, the Kafka key"
        JSONB payload "fact envelope"
        TIMESTAMPTZ occurred_at
        INTEGER attempts "capped, then kept for re-arming"
        TIMESTAMPTZ next_attempt_at
        TIMESTAMPTZ created_at
    }
    processed_events {
        TEXT event_id PK "CloudEvents id, inbox"
        TIMESTAMPTZ processed_at
    }
    idempotency_keys {
        TEXT key PK
        TEXT fingerprint "method, path, body"
        INTEGER status "2xx and 4xx only"
        TEXT content_type
        BYTEA body
        TIMESTAMPTZ created_at
    }
```

Source: `internal/adapters/inbound/http/errors.go`, `internal/application/usecases/facts.go`.

**All three are infrastructure, not domain.** `outbox` is written in the same transaction as the aggregate
(one row per domain fact) and drained by the relay, which encodes it as a CloudEvent and writes it to
`warehouse.dock-scheduling.events` and/or `warehouse.dock-scheduling.analytics` depending on the event's
stream; rows at the attempt cap stay until an operator re-arms them, and a row that can never be encoded
goes to `warehouse.dock-scheduling.events.dlq`. `processed_events` is the inbox: one row per consumed CloudEvents `id`, claimed in
the same transaction as the effect. `idempotency_keys` stores the first 2xx or 4xx answer to a REST
`Idempotency-Key` (never a 5xx), keyed by the request fingerprint so that the same key with another request
is `409 idempotency-key-reused`. The migration tool keeps its own bookkeeping table.

## Why no foreign key crosses an aggregate or a context

- **Across aggregates.** `dock_appointments.door_code` and `.site_id` are
  values, not foreign keys to `dock_calendars` or `calendar_closures`.
  `DockAppointment` and `DockCalendar` are separate consistency boundaries
  saved in separate transactions; a foreign key would couple their lifecycles.
  The rules that span them (`ErrDoorUnavailable`,
  `ErrClosureConflictsWithAppointments`, `ErrHoursConflictWithAppointments`) are checks in the use case.
  The one foreign key inside the appointment boundary is `asn_claims.appointment_id`.
- **To other contexts.** `door_code`, `asn_number`, `cpt_id`, `site_id` and
  `shipment_ref` name things owned by `facility-layout`, `inbound-receiving`,
  `process-path-management` or an outbound system. Those live in other databases; this context only
  holds event-fed copies (`zones`, `doors`, `asns`, `cutoff_schedules`).

## Table ≠ aggregate

| Table | Aggregate / role | Notes |
| --- | --- | --- |
| `dock_appointments` | **DockAppointment** (root) | One row per appointment, the whole snapshot; `version` guards every update. |
| `asn_claims` | **DockAppointment** (its `InboundRefs`, as a uniqueness guard) | One row per ASN of an active native appointment. |
| `dock_calendars` | **DockCalendar** (root) | One row per site; the settings. Created lazily. |
| `calendar_operating_hours` | **DockCalendar** (its `OperatingHours`) | Replaced as a set by `DeclareOperatingHours`. |
| `calendar_closures` | **DockCalendar** (its `Closure` list) | Added by `ScheduleDoorClosure`; `LiftDoorClosure` sets `lifted`. |
| `zones`, `doors` | read model `DoorView` | From `facility-layout` zone and slot events. |
| `asns` | read model `AsnView` | From `ASNRegistered`, `ASNCancelled`. |
| `cutoff_schedules`, `cutoffs` | read model `CutoffView` | From `CPTScheduleChanged`. |
| `appointment_facts` (analytics database) | read model `AdherenceReport` | Projected from the analytics topic. |
| `outbox` | none (infrastructure) | Transactional outbox. |
| `processed_events` | none (infrastructure) | Inbox. |
| `idempotency_keys` | none (infrastructure) | REST replay store. |
