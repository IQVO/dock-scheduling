---
id: domain-events
title: Domain Events
sidebar_label: Domain Events
description: "Every event Dock Scheduling publishes and consumes, with full CloudEvents types, topics, keys, payload fields, producers and consumers."
---

# Domain Events

Every event this context publishes and consumes, from
`contexts[dock-scheduling].publishes` and `.consumes`. The envelope follows
`conventions.cloudevents` in the design model.

## Envelope

| Attribute | Value |
| --- | --- |
| `specversion` | `1.0` (structured JSON mode) |
| `id` | a UUID minted once, when the event is written to the outbox; a redelivery carries the same `id` |
| `source` | `/warehouse/dock-scheduling` |
| `type` | `com.warehouse.wms.dock-scheduling.<entity>.<EventName>`, entity `dockappointment` or `dockcalendar` |
| `subject` | the aggregate root id: `appointmentId` for `dockappointment` events, `siteId` for `dockcalendar` events |
| `time` | the instant the fact happened, UTC |
| `datacontenttype` | `application/json` |
| `dataschema` | `urn:warehouse:dock-scheduling:events:<EventName>:v1` on the integration topic, `urn:warehouse:dock-scheduling:analytics:<EventName>:v1` on its analytics twin |
| Kafka topic | `warehouse.dock-scheduling.events` for integration events, `warehouse.dock-scheduling.analytics` for events marked `analytics only` |
| Kafka key | the same id as `subject`, so one appointment's (or one site calendar's) events stay in order on one partition |
| Payload case | `snake_case` |
| `version` | in `data` of every event except `DockAppointmentAtRisk`: the aggregate `version` (starts at 1, plus 1 per accepted change); a reader applies only a `version` greater than the stored one |

Every event leaves through the transactional outbox, in the same transaction
as the aggregate change: one outbox row, up to two Kafka messages. Events the design model marks `analytics only` go **only**
to `warehouse.dock-scheduling.analytics`; integration events go to
`warehouse.dock-scheduling.events` and, as a twin with the same `id`, `type`, key and
`data`, to `warehouse.dock-scheduling.analytics` (`conventions.analytics_rule`; the
service's ADR 0004 and, for `DockAppointmentDoorReassigned`, ADR 0010).
In the code the AsyncAPI is version 2.6.0 with one channel per topic: seven
integration types on the events channel, all eleven on the analytics channel.

## Published

### Appointment events (key `appointmentId`)

| Event | Full `type` | Stream | `data` fields | Producing command | Consumers |
| --- | --- | --- | --- | --- | --- |
| `DockAppointmentBooked` | `com.warehouse.wms.dock-scheduling.dockappointment.DockAppointmentBooked` | integration + analytics twin | `appointment_id`, `site_id`, `direction`, `door_code`, `carrier`, `carrier_scac`, `window_start`, `window_end`, `asn_numbers`, `cpt_id`, `shipment_refs`, `expected_seal_number`, `arrival_tolerance_minutes`, `provenance`, `version` | `BookAppointment` | `yard-management`, `warehouse-planning`, `inbound-receiving`, `outbound-shipping` |
| `DockAppointmentRescheduled` | `com.warehouse.wms.dock-scheduling.dockappointment.DockAppointmentRescheduled` | integration + analytics twin | `appointment_id`, `site_id`, `door_code`, `previous_door_code`, `window_start`, `window_end`, `previous_window_start`, `previous_window_end`, `arrival_tolerance_minutes`, `version` | `RescheduleAppointment` (window and/or door; carries `previous_door_code`) | `yard-management`, `warehouse-planning`, `inbound-receiving` |
| `DockAppointmentCancelled` | `com.warehouse.wms.dock-scheduling.dockappointment.DockAppointmentCancelled` | integration + analytics twin | `appointment_id`, `site_id`, `door_code`, `reason`, `version` | `CancelAppointment` (carrier or planner request; policies `CancelOnVisitRejected` with `TURNED_AWAY` and `CancelWhenAllAsnsCancelled` with `ASN_CANCELLED`) | `yard-management`, `warehouse-planning`, `inbound-receiving` |
| `DockAppointmentNoShow` | `com.warehouse.wms.dock-scheduling.dockappointment.DockAppointmentNoShow` | integration + analytics twin | `appointment_id`, `site_id`, `door_code`, `direction`, `carrier_scac`, `window_end`, `version` | `MarkNoShow` (policy `NoShowSweep`) | `yard-management`, `warehouse-planning`, `exception-management` |
| `DockAppointmentDoorReassigned` | `com.warehouse.wms.dock-scheduling.dockappointment.DockAppointmentDoorReassigned` | integration + analytics twin | `appointment_id`, `site_id`, `from_door_code`, `to_door_code`, `version` | `ReassignDoor` (policy `ActualDoorWins`) | `inbound-receiving` |
| `DockAppointmentCheckedIn` | `com.warehouse.wms.dock-scheduling.dockappointment.DockAppointmentCheckedIn` | analytics only | `appointment_id`, `site_id`, `door_code`, `checked_in_at`, `arrival_status`, `visit_id`, `version` | `RecordArrival` (policy `ArrivalFromGate`) | analytics only |
| `DockAppointmentCompleted` | `com.warehouse.wms.dock-scheduling.dockappointment.DockAppointmentCompleted` | analytics only | `appointment_id`, `site_id`, `door_code`, `completed_at`, `version` | `RecordDoorRelease` (policy `CompleteOnDoorRelease`) | analytics only |
| `DockAppointmentAtRisk` | `com.warehouse.wms.dock-scheduling.dockappointment.DockAppointmentAtRisk` | analytics only | `appointment_id`, `site_id`, `door_code`, `eta_at`, `window_start`, `window_end`, `cause` | `ReportEta` (policy `EtaRisk`), `ReassignDoor` (policy `ActualDoorWins`) | analytics only |

### Calendar events (key `siteId`)

| Event | Full `type` | Stream | `data` fields | Producing command | Consumers |
| --- | --- | --- | --- | --- | --- |
| `DoorClosureScheduled` | `com.warehouse.wms.dock-scheduling.dockcalendar.DoorClosureScheduled` | integration + analytics twin | `site_id`, `closure_id`, `door_code`, `from`, `to`, `reason`, `version` | `ScheduleDoorClosure` | `yard-management` |
| `DoorClosureLifted` | `com.warehouse.wms.dock-scheduling.dockcalendar.DoorClosureLifted` | integration + analytics twin | `site_id`, `closure_id`, `door_code`, `version` | `LiftDoorClosure` | `yard-management` |
| `DoorOperatingHoursDeclared` | `com.warehouse.wms.dock-scheduling.dockcalendar.DoorOperatingHoursDeclared` | analytics only | `site_id`, `door_code`, `operating_hours`, `version` | `DeclareOperatingHours` | analytics only |

Stream: `integration + analytics twin` is `warehouse.dock-scheduling.events` plus the same message on
`warehouse.dock-scheduling.analytics`; `analytics only` is the analytics topic alone.

Enumerations used in the payloads: `direction` is `INBOUND` or `OUTBOUND`;
`arrival_status` is `EARLY`, `ON_TIME` or `LATE`; `reason` of
`DockAppointmentCancelled` is a `CancellationReason` (`CARRIER_REQUEST`,
`SITE_REQUEST`, `ASN_CANCELLED`, `TURNED_AWAY`); `cause` of
`DockAppointmentAtRisk` is an `AtRiskCause` (`LATE_ETA`,
`DOOR_REASSIGNED_OVERLAP`).

"Analytics only" means no other context reads the event; the service's projector
folds it into the adherence report. No context may depend on those events for a
decision without a model change. `DockAppointmentDoorReassigned` stopped being
analytics only on 2026-11-09 (service ADR 0010): `inbound-receiving` keeps its copy of
the appointment's door from it. Commands that raise no event: `ReportEta`
(unless the ETA is late), `DeclareCalendarSettings`. `DockAppointmentAtRisk` carries
no `version`; the REST appointment exposes `atRisk` and `atRiskCause` instead.

### Example: a no-show

`DockAppointmentNoShow` is one of the events with three consumers. Ids
and times are illustrative; the `data` fields are exactly those in the design model and the AsyncAPI.

```json
{
  "specversion": "1.0",
  "id": "6f1d2c4e-8a1b-4e8f-9b4a-2d5c7e9f1a30",
  "source": "/warehouse/dock-scheduling",
  "type": "com.warehouse.wms.dock-scheduling.dockappointment.DockAppointmentNoShow",
  "subject": "appt-0001",
  "time": "2026-11-06T14:40:00Z",
  "datacontenttype": "application/json",
  "dataschema": "urn:warehouse:dock-scheduling:events:DockAppointmentNoShow:v1",
  "data": {
    "appointment_id": "appt-0001",
    "site_id": "SITE-A",
    "door_code": "D07",
    "direction": "INBOUND",
    "carrier_scac": "ABCD",
    "window_end": "2026-11-06T14:00:00Z",
    "version": 2
  }
}
```

## Consumed

Every consumer is idempotent on the CloudEvents `id` (a row in `processed_events` written
in the same transaction as the effect) and dispatches on the full `type` string, never on a
suffix (`conventions.integration_rules`). A fact that is valid but changes nothing (an
unknown or terminal appointment, a site mismatch, an unknown door) is counted in
`dock_scheduling_ignored_facts_total` and committed; a payload that can never be applied
is dead-lettered to `<topic>.dlq`. The four consumers and their environment variables
are in the service's ADR 0009. The full types: `yard-management` events are
`com.warehouse.wms.yard-management.<entity>.<EventName>` (entity `yardvisit` or `trailer`),
`process-path-management` is in the `wes` tier
(`com.warehouse.wes.process-path-management.cptschedule.CPTScheduleChanged`), the others
are read from each producer's AsyncAPI.

| Producer | Event and topic | `data` fields the design model relies on | What this context does with it | Idempotency beyond the inbox (in the code) |
| --- | --- | --- | --- | --- |
| `yard-management` | `VisitCheckedIn`, `warehouse.yard-management.events` | `appointment_id`, `visit_id`, `arrived_at` | Policy `ArrivalFromGate`: when it names an `appointment_id`, `RecordArrival` with `arrival_status` from the window and the arrival tolerance | An appointment already `CHECKED_IN` is left unchanged; unknown and terminal ones are ignored and counted; a blank `appointment_id` (walk-in, drop) applies nothing |
| `yard-management` | `VisitRejected`, `warehouse.yard-management.events` | `appointment_id` | Policy `CancelOnVisitRejected`: a `BOOKED` appointment is cancelled with reason `TURNED_AWAY` | Any other state, an unknown id or a site mismatch is ignored and counted |
| `yard-management` | `TrailerDocked`, `warehouse.yard-management.events` | `door_code`, `appointment_id` | Policy `ActualDoorWins`: if the door differs from the booked door, `ReassignDoor` (the physical fact wins, the overlap refusal is skipped) and `DockAppointmentDoorReassigned`; later appointments now overlapping get `DockAppointmentAtRisk` | Same door: no change |
| `yard-management` | `TrailerUndocked`, `warehouse.yard-management.events` | `door_code`, `appointment_id` | Policy `CompleteOnDoorRelease`: when it names an `appointment_id`, `RecordDoorRelease`; `OutOfOrderYardFacts` covers a `BOOKED` appointment (implicit arrival) | A `COMPLETED` appointment is not completed twice |
| `facility-layout` | `LocationSlotRegistered`, `warehouse.facility.events` | `locationCode`, `zoneId`, `role`, `dockFlow` | Upserts a Dock slot into `DoorView` (`doors`); a door whose zone has not arrived is held pending and cannot be booked | Upsert on the door code |
| `facility-layout` | `LocationSlotDecommissioned`, `warehouse.facility.events` | `locationCode` | Deletes the door from `DoorView`: new bookings fail with `ErrUnknownDoor`, existing appointments are left untouched (D-7) | Delete is naturally repeatable |
| `facility-layout` | `ZoneRegistered`, `warehouse.facility.events` | `zoneId`, `siteCode` | Maps `zoneId` to `siteCode`, which gives every door its `site_id` (`zones`) | Upsert on `zoneId` |
| `inbound-receiving` | `ASNRegistered`, `warehouse.inbound-receiving.events` | `asn_number`, `expected_arrival`, `lines` | Inserts the ASN into `AsnView` (`asns`) | Upsert on `asn_number`; a cancelled ASN stays cancelled |
| `inbound-receiving` | `ASNCancelled`, `warehouse.inbound-receiving.events` | `asn_number` | Marks the ASN cancelled in `AsnView`; policy `CancelWhenAllAsnsCancelled` cancels an `INBOUND` `BOOKED` appointment whose ASNs are all cancelled, reason `ASN_CANCELLED` | A cancelled appointment is skipped |
| `process-path-management` | `CPTScheduleChanged`, `warehouse.process-path-management.events` | `site_id`, `timezone`, `cutoffs` | Stores the schedule in `CutoffView` (`cutoff_schedules`, `cutoffs`). Phase 2, not built: policy `PickupFromCutoffs` would keep `OUTBOUND` appointments; today no appointment is created from it (D-3) | The whole schedule of a site replaces the previous one; the event has no version, the inbox claim makes a replay a no-op |

The legacy importer that once read this topic under its own group was removed in ADR 0011: this
context consumes no `inbound-receiving` `dockappointment.*` type.

Source: `internal/application/usecases/facts.go`, `internal/application/usecases/yard.go`, `internal/application/usecases/change.go`, `internal/adapters/inbound/kafka/yard_consumer.go`, `internal/adapters/inbound/kafka/facility_consumer.go`, `internal/adapters/inbound/kafka/asn_consumer.go`, `internal/adapters/inbound/kafka/cutoff_consumer.go`, `internal/adapters/outbound/kafka/encoder.go`, `apis/asyncapi.yaml`, `internal/application/ports/ports.go`, `migrations/0001_dock_scheduling.up.sql`.

## Notes

The gaps this page used to list are closed by the service's decisions (see the
[Model Gap Register](https://iqvo.github.io/warehouse-docs/yard/strategic-design/model-gap-register)):

- The arrival tolerance that `yard-management` needs reaches it on `DockAppointmentBooked` and `DockAppointmentRescheduled` as `arrival_tolerance_minutes` (X1, `DS-G01`).
- `VisitRejected` is consumed here (policy `CancelOnVisitRejected`, X2, `DS-G02`), and also read by `exception-management`.
- `DockAppointmentAtRisk` has no `version`; clients read `atRisk` and `atRiskCause` from the appointment, and `GET /appointments` takes an `atRisk` filter (D-6, `DS-G11`).
- `inbound-receiving` keeps its appointments copy from `DockAppointmentBooked`, `Rescheduled`, `Cancelled` and, since ADR 0010, `DockAppointmentDoorReassigned`. It does not need `NoShow` (X7, `DS-G14`).
- `outbound-shipping` also reads `DockAppointmentBooked`.
