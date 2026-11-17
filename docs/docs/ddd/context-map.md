---
id: context-map
title: Context Map
sidebar_label: Context Map
description: "Dock Scheduling's neighbourhood on the yard context map, with the upstream and downstream side, pattern and channel of every relationship."
---

# Context Map

Following ddd-crew [Context Mapping](https://github.com/ddd-crew/context-mapping).
This is **dock-scheduling's slice** of the yard context map: every
relationship it has, with the upstream (U) and downstream (D) side, the
pattern and the channel. Abbreviations: OHS = Open Host Service, PL =
Published Language, CF = Conformist, C/S = Customer/Supplier.

```mermaid
flowchart LR
    YM1["yard-management<br/>Supporting"]
    FL["facility-layout"]
    IR1["inbound-receiving"]
    PP["process-path-<br/>management"]
    CS(["carrier scheduler"])
    DP(["dock planner"])
    CR(["scheduler cron"])
    DS["dock-<br/>scheduling<br/>Supporting"]
    YM2["yard-management<br/>Supporting"]
    WP["warehouse-<br/>planning"]
    IR2["inbound-receiving"]
    EM["exception-<br/>management"]
    OS["outbound-<br/>shipping"]
    ANY(["any context"])

    YM1 -->|"U/D: C/S<br/>Kafka VisitCheckedIn,<br/>VisitRejected,<br/>TrailerDocked,<br/>TrailerUndocked"| DS
    FL -->|"U/D: CF<br/>Kafka slots, zones"| DS
    IR1 -->|"U/D: CF<br/>Kafka ASNRegistered,<br/>ASNCancelled"| DS
    PP -->|"U/D: CF<br/>Kafka<br/>CPTScheduleChanged"| DS
    CS -->|"OHS - REST<br/>appointments"| DS
    DP -->|"UI - REST<br/>dock-calendar,<br/>reschedule, cancel"| DS
    CR -->|"UI - REST<br/>no-show-sweeps"| DS
    DS -->|"U/D: C/S<br/>Kafka appointment,<br/>closure and carrier<br/>events"| YM2
    DS -->|"U/D: OHS+PL<br/>Kafka appointment<br/>events"| WP
    DS -->|"U/D: OHS+PL<br/>Kafka Booked, Rescheduled,<br/>Cancelled, DoorReassigned"| IR2
    DS -->|"U/D: OHS+PL<br/>Kafka Booked"| OS
    DS -->|"U/D: OHS+PL<br/>Kafka NoShow"| EM
    DS -.->|"OHS - REST GET<br/>appointments, doors,<br/>dock-calendar"| ANY
```

Source: `internal/adapters/inbound/kafka/yard_consumer.go`, `internal/adapters/inbound/kafka/facility_consumer.go`, `internal/adapters/inbound/kafka/asn_consumer.go`, `internal/adapters/inbound/kafka/cutoff_consumer.go`, `internal/adapters/outbound/kafka/encoder.go`, `apis/asyncapi.yaml`.

Legend: arrows point from upstream to downstream. Solid = events or
commands; dotted = synchronous query.
`yard-management` and `inbound-receiving` appear twice because they are on
both sides: they feed this context and they consume from it. People are
rounded boxes. Patterns on the edges to `warehouse-planning`,
`inbound-receiving`, `exception-management` and `outbound-shipping` are this page's reading; the
model names no pattern for them.

Omitted: Kafka and Postgres as infrastructure; the analytics-only events
(`DockAppointmentCheckedIn`, `DockAppointmentCompleted`, `DockAppointmentAtRisk`,
`DoorOperatingHoursDeclared`), which have no named consumer; the
changes to `warehouse-console`, `warehouse-floor-client` and
`warehouse-ops-agent` (they are clients of the REST routes, listed in
`fleet_changes`). **Separate Ways** (no edge at all): the retail network
(`retailnetwork.*` topics and services); the design model's `hard_boundary` rules
out any yard context consuming a `retailnetwork.*` topic or calling a
retail-network service.

## Relationships

| Neighbour | Direction | Pattern | Channel | Messages | Why this pattern |
| --- | --- | --- | --- | --- | --- |
| `yard-management` (Supporting) | Upstream for yard facts; downstream for appointments (**mutual**) | **Customer/Supplier** both ways (the design model names it on both inbound rows) | Kafka `warehouse.yard-management.events` into this context; `warehouse.dock-scheduling.events` out | In: `VisitCheckedIn`, `VisitRejected`, `TrailerDocked`, `TrailerUndocked`. Out: `DockAppointmentBooked`, `DockAppointmentRescheduled`, `DockAppointmentCancelled`, `DockAppointmentNoShow`, `DoorClosureScheduled`, `DoorClosureLifted`, `CarrierRegistered`, `CarrierSuspended`, `CarrierReinstated` | The two flows are independent (plan against physical) and neither writes into the other's aggregates. Each side shapes its events for the other and keeps an event-fed local copy; no synchronous call in either direction. |
| `facility-layout` | Upstream | **Conformist** | Kafka `warehouse.facility.events` | `LocationSlotRegistered`, `LocationSlotDecommissioned`, `ZoneRegistered` | `facility-layout` is the registry of slots and zones for every context. Dock slots, `dockFlow` and the zone to site join are taken as given into `DoorView`. |
| `inbound-receiving` | Upstream for ASNs; downstream for appointments | **Conformist** for ASNs; consumer of this context's `Booked`, `Rescheduled`, `Cancelled` and `DoorReassigned` | Kafka `warehouse.inbound-receiving.events` in; `warehouse.dock-scheduling.events` out | In: `ASNRegistered`, `ASNCancelled`. Out: `DockAppointmentBooked`, `DockAppointmentRescheduled`, `DockAppointmentCancelled`, `DockAppointmentDoorReassigned` | ASNs are taken as given into `AsnView`. The appointment moved to this context (YD-0002): `inbound-receiving` retired its appointment write routes and keeps an appointments copy fed by this context (its ADR 0019). The legacy import feed (policy `LegacyImport`) was removed in ADR 0011. |
| `process-path-management` | Upstream | **Conformist** | Kafka `warehouse.process-path-management.events` | `CPTScheduleChanged` | The CPT schedule is the fleet's own outbound cutoff; it is taken as given into `CutoffView`. |
| Carrier scheduler (carrier portal) | Upstream (person or carrier system) | **OHS** on this context's REST | REST `POST /appointments`, `.../reschedules`, `.../cancellations`, `.../eta-reports` | `BookAppointment`, `RescheduleAppointment`, `CancelAppointment`, `ReportEta` | Carriers are outside the fleet; the contract is one published REST surface. |
| Dock planner | Upstream (person, console) | UI on this context's REST | REST `PUT .../operating-hours`, `PUT .../settings`, `POST .../closures`, `POST .../closures/{closureId}/lifts` | `DeclareOperatingHours`, `DeclareCalendarSettings`, `ScheduleDoorClosure`, `LiftDoorClosure`; also `RescheduleAppointment` and `CancelAppointment` on behalf of a carrier (D-5) | Human maintenance of the door calendar. |
| Scheduler (cron) | Upstream (CronJob) | UI on this context's REST | REST `POST /no-show-sweeps` | `MarkNoShow` | An idempotent sweep called on a timer. |
| `warehouse-planning` | Downstream | OHS + PL (this page's reading) | Kafka `warehouse.dock-scheduling.events` | `DockAppointmentBooked`, `DockAppointmentRescheduled`, `DockAppointmentCancelled`, `DockAppointmentNoShow` | Inbound labour demand. `warehouse-planning` reads these events instead of `inbound-receiving`'s (its ADR 0020, merged on develop); they gain `site_id` and `direction`. |
| `exception-management` | Downstream | OHS + PL (this page's reading) | Kafka `warehouse.dock-scheduling.events` | `DockAppointmentNoShow` | Opens a problem case per no-show (merged on develop). |
| `outbound-shipping` | Downstream | OHS + PL (this page's reading) | Kafka `warehouse.dock-scheduling.events` | `DockAppointmentBooked` | Ship-confirm context; it also reads the yard's departure facts (merged on develop, not deployed). |
| MCP clients (the design model plans `warehouse-ops-agent`; whether it is wired was not checked) | Downstream | OHS | MCP `cmd/mcp` of this service | 5 L1 read tools, 4 L3 write tools (`book_appointment`, `reschedule_appointment`, `cancel_appointment`, `report_eta`, each with `initiated_by`) | Arrival, door release, calendar changes and the sweep are not exposed over MCP. |
| Any context | Downstream | **OHS** | REST `GET /appointments`, `GET /appointments/{appointmentId}`, `GET /doors/{doorCode}/availability`, `GET /sites/{siteId}/dock-calendar` | `ListAppointments`, `GetAppointment`, `GetDoorAvailability`, `GetDockCalendar` | Read access for tools and diagnostics; no decision path of a sibling depends on it (decisions use events). |

## Notes on the patterns

- **Mutual Customer/Supplier with `yard-management`.** The design model puts the
  two contexts in each other's consumer list. The risk is a start-up cycle
  or a feedback loop; the safeguards named in the design model are idempotent
  consumers (inbox on the CloudEvents `id`) and version-guarded readers
  (apply only `version` greater than the stored one).
- **Conformist upstream, Open Host downstream.** This context conforms to
  `facility-layout`, `inbound-receiving` (ASNs) and `process-path-management`,
  and offers its appointment, closure and carrier events as a published language.
- **No Anti-Corruption Layer.** All neighbours are in the same fleet and
  share the CloudEvents standard; translation stops at local read models.
  The future `transport-visibility` context is the first place the design model
  expects an ACL (an ETA feed into `ReportEta`).
- **No Shared Kernel.** `shared` (door code, site id, carrier and SCAC) is a package inside this service (the
  `ul` code identifier `shared.Carrier`), not shared code across contexts. Identifiers such as
  `siteId`, `doorCode` and `asn_number` are values copied from events.
