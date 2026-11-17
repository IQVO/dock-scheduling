---
id: events
title: Events
sidebar_label: Events
description: "The CloudEvents catalogue of dock-scheduling: the fourteen published events, the ten consumed ones, topics, keys, envelope and delivery guarantees."
---

# Events

dock-scheduling publishes **fourteen** event types and consumes **ten**, all as CloudEvents 1.0 (structured JSON mode) on Kafka. The contract is `apis/asyncapi.yaml` (AsyncAPI 2.6.0); the catalogue is guarded by `TestEventCatalogueMatchesContract`. The narrative and the policies behind each event are on [Domain events](/docs/ddd/domain-events).

Source: `apis/asyncapi.yaml`, `internal/adapters/outbound/kafka/encoder.go` (`routeOf`, the ADR 0004 catalogue), `internal/adapters/kafka/cloudevents/cloudevents.go`, `internal/adapters/inbound/kafka/*_consumer.go`, [ADR 0004](/docs/adr/0004-cloudevents-catalogue), [ADR 0010](/docs/adr/0010-door-reassigned-is-an-integration-event), [ADR 0012](/docs/adr/0012-carrier-registry).

## Envelope and delivery

| Attribute | Value |
| --- | --- |
| `specversion` | `1.0` |
| `id` | UUID minted once when the fact is written to the outbox; a relay retry republishes the same id. Consumers dedupe on it. |
| `source` | `/warehouse/dock-scheduling` |
| `type` | `com.warehouse.wms.dock-scheduling.<entity>.<EventName>`, entity `dockappointment`, `dockcalendar` or `carrier` |
| `subject` and Kafka key | the appointment id (`appt-<uuid>`), the site id for calendar events, or the SCAC for carrier events: one appointment's (or carrier's) history stays on one partition |
| `time` | the fact's occurred-at, UTC |
| `datacontenttype` | `application/json`; Kafka `content-type` header `application/cloudevents+json; charset=UTF-8` |
| `dataschema` | `urn:warehouse:dock-scheduling:<stream>:<EventName>:v1`, stream `events` or `analytics` |
| `data` | snake_case fields (below) |

Topics: `warehouse.dock-scheduling.events` (integration events; contract for other contexts), `warehouse.dock-scheduling.analytics` (every published fact as a twin with the same `id`, `type`, key and `data`, only the `dataschema` stream segment differs, plus the analytics-only facts; read only by this service's projector), `warehouse.dock-scheduling.events.dlq` (rows that can never be encoded). Writes are acks=all with no topic fixed on the writer. Delivery is **at-least-once**; order is preserved per key, except that a row excluded by the attempt cap is overtaken by later rows ([ADR 0005](/docs/adr/0005-postgres-persistence-and-outbox)). The events channel carries seven types; the analytics channel carries all eleven.

### Standard semantics on the facts (ADR 0013)

Every appointment fact (and every door closure) can carry up to three **optional** `data` fields that say what the fact means in the
standards partners read. Each is omitted (never `null`) when it has no honest value, so a fact with none of them is byte-for-byte the
payload it was before, and the integration fact and its analytics twin carry identical `data`.

| Field | Meaning | Value |
| --- | --- | --- |
| `x12_status` | the ANSI X12 214 status code | `AB` Delivery Appointment Date and/or Time (INBOUND booked or rescheduled), `AA` Pick-up Appointment Date and/or Time (OUTBOUND booked or rescheduled), `X1` Arrived at Delivery Location (INBOUND check-in), `X3` Arrived at Pick-up Location (OUTBOUND check-in) |
| `cbv_biz_step` | the GS1 EPCIS Core Business Vocabulary business step, URN form | `urn:epcglobal:cbv:bizstep:arriving` (check-in) |
| `read_point_gln` | the GS1 GLN (13 digits) of the door where the fact happened | the door's GLN as facility-layout registered it (ADR 0045 there), when it has one; closures carry only this field |

Every other event (cancellation, no-show, door reassignment, completion, at-risk, the calendar and carrier facts) is decided NOT mapped
to a code, with the reason, in [ADR 0013](/docs/adr/0013-standard-vocabulary-on-published-facts); it carries only `read_point_gln`
where it has a door with a GLN. The mapping is one table (`internal/domain/standards`) and the AsyncAPI declares the fields.

## Published

### Appointment events (key `appointmentId`)

| Event | Full `type` | Stream | `data` fields | Producing command | Consumers |
| --- | --- | --- | --- | --- | --- |
| `DockAppointmentBooked` | `com.warehouse.wms.dock-scheduling.dockappointment.DockAppointmentBooked` | integration + analytics twin | `appointment_id`, `site_id`, `direction`, `door_code`, `carrier`, `carrier_scac`, `window_start`, `window_end`, `asn_numbers`, `cpt_id`, `shipment_refs`, `expected_seal_number`, `arrival_tolerance_minutes`, `provenance`, `version`; optional `x12_status`, `read_point_gln` | `BookAppointment` | `yard-management`, `warehouse-planning`, `inbound-receiving`, `outbound-shipping` |
| `DockAppointmentRescheduled` | `com.warehouse.wms.dock-scheduling.dockappointment.DockAppointmentRescheduled` | integration + analytics twin | `appointment_id`, `site_id`, `door_code`, `previous_door_code`, `window_start`, `window_end`, `previous_window_start`, `previous_window_end`, `arrival_tolerance_minutes`, `version`; optional `x12_status`, `read_point_gln` | `RescheduleAppointment` (window and/or door; carries `previous_door_code`) | `yard-management`, `warehouse-planning`, `inbound-receiving` |
| `DockAppointmentCancelled` | `com.warehouse.wms.dock-scheduling.dockappointment.DockAppointmentCancelled` | integration + analytics twin | `appointment_id`, `site_id`, `door_code`, `reason`, `version` | `CancelAppointment` (carrier or planner request; policies `CancelOnVisitRejected` with `TURNED_AWAY` and `CancelWhenAllAsnsCancelled` with `ASN_CANCELLED`) | `yard-management`, `warehouse-planning`, `inbound-receiving` |
| `DockAppointmentNoShow` | `com.warehouse.wms.dock-scheduling.dockappointment.DockAppointmentNoShow` | integration + analytics twin | `appointment_id`, `site_id`, `door_code`, `direction`, `carrier_scac`, `window_end`, `version` | `MarkNoShow` (policy `NoShowSweep`) | `yard-management`, `warehouse-planning`, `exception-management` |
| `DockAppointmentDoorReassigned` | `com.warehouse.wms.dock-scheduling.dockappointment.DockAppointmentDoorReassigned` | integration + analytics twin | `appointment_id`, `site_id`, `from_door_code`, `to_door_code`, `version`; optional `read_point_gln` (the new door) | `ReassignDoor` (policy `ActualDoorWins`) | `inbound-receiving` |
| `DockAppointmentCheckedIn` | `com.warehouse.wms.dock-scheduling.dockappointment.DockAppointmentCheckedIn` | analytics only | `appointment_id`, `site_id`, `door_code`, `checked_in_at`, `arrival_status`, `visit_id`, `version`; optional `x12_status`, `cbv_biz_step`, `read_point_gln` | `RecordArrival` (policy `ArrivalFromGate`) | analytics only |
| `DockAppointmentCompleted` | `com.warehouse.wms.dock-scheduling.dockappointment.DockAppointmentCompleted` | analytics only | `appointment_id`, `site_id`, `door_code`, `completed_at`, `version` | `RecordDoorRelease` (policy `CompleteOnDoorRelease`) | analytics only |
| `DockAppointmentAtRisk` | `com.warehouse.wms.dock-scheduling.dockappointment.DockAppointmentAtRisk` | analytics only | `appointment_id`, `site_id`, `door_code`, `eta_at`, `window_start`, `window_end`, `cause` | `ReportEta` (policy `EtaRisk`), `ReassignDoor` (policy `ActualDoorWins`) | analytics only |

### Calendar events (key `siteId`)

| Event | Full `type` | Stream | `data` fields | Producing command | Consumers |
| --- | --- | --- | --- | --- | --- |
| `DoorClosureScheduled` | `com.warehouse.wms.dock-scheduling.dockcalendar.DoorClosureScheduled` | integration + analytics twin | `site_id`, `closure_id`, `door_code`, `from`, `to`, `reason`, `version` | `ScheduleDoorClosure` | `yard-management` |
| `DoorClosureLifted` | `com.warehouse.wms.dock-scheduling.dockcalendar.DoorClosureLifted` | integration + analytics twin | `site_id`, `closure_id`, `door_code`, `version` | `LiftDoorClosure` | `yard-management` |
| `DoorOperatingHoursDeclared` | `com.warehouse.wms.dock-scheduling.dockcalendar.DoorOperatingHoursDeclared` | analytics only | `site_id`, `door_code`, `operating_hours`, `version` | `DeclareOperatingHours` | analytics only |

### Carrier events (key `scac`, ADR 0012)

| Event | Full `type` | Stream | `data` fields | Producing command | Consumers |
| --- | --- | --- | --- | --- | --- |
| `CarrierRegistered` | `com.warehouse.wms.dock-scheduling.carrier.CarrierRegistered` | integration + analytics twin | `scac`, `name`, `usdot`, `mc`, `status`, `reason`, `suspended_at`, `version` (the full snapshot) | `RegisterCarrier` (first registration), `UpdateCarrier` | none yet (`yard-management` planned) |
| `CarrierSuspended` | `com.warehouse.wms.dock-scheduling.carrier.CarrierSuspended` | integration + analytics twin | `scac`, `reason`, `suspended_at`, `version` | `SuspendCarrier` | none yet (`yard-management` planned) |
| `CarrierReinstated` | `com.warehouse.wms.dock-scheduling.carrier.CarrierReinstated` | integration + analytics twin | `scac`, `reinstated_at`, `version` | `ReinstateCarrier` | none yet (`yard-management` planned) |

A carrier fact raised by an MCP write also carries an optional `initiated_by` object in `data` (`via`, `proposal_id`, `approved_by`, `agent_id`); a REST write carries none.

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
decision without an ADR. `DockAppointmentDoorReassigned` stopped being
analytics only on 2026-11-09 (service ADR 0010): `inbound-receiving` keeps its copy of
the appointment's door from it. Commands that raise no event: `ReportEta`
(unless the ETA is late), `DeclareCalendarSettings`. `DockAppointmentAtRisk` carries
no `version`; the REST appointment exposes `atRisk` and `atRiskCause` instead.

## Consumed

| Topic | Full `type` | Handler effect |
| --- | --- | --- |
| `warehouse.yard-management.events` | `com.warehouse.wms.yard-management.yardvisit.VisitCheckedIn` | `RecordArrival` |
| | `com.warehouse.wms.yard-management.yardvisit.VisitRejected` | cancel with `TURNED_AWAY` |
| | `com.warehouse.wms.yard-management.trailer.TrailerDocked` | `ReassignDoor` |
| | `com.warehouse.wms.yard-management.trailer.TrailerUndocked` | `RecordDoorRelease` |
| `warehouse.facility.events` | `com.warehouse.wms.facility-layout.zone.ZoneRegistered` | zone copy |
| | `com.warehouse.wms.facility-layout.locationslot.LocationSlotRegistered` | door copy |
| | `com.warehouse.wms.facility-layout.locationslot.LocationSlotDecommissioned` | door deactivated |
| `warehouse.inbound-receiving.events` | `com.warehouse.wms.inbound-receiving.asn.ASNRegistered` | ASN copy |
| | `com.warehouse.wms.inbound-receiving.asn.ASNCancelled` | ASN copy and `CancelWhenAllAsnsCancelled` |
| `warehouse.process-path-management.events` | `com.warehouse.wes.process-path-management.cptschedule.CPTScheduleChanged` | replaces the site's cutoff copy |

Consumer behaviour (CloudEvents only, dispatch on the full type, claim the id in `processed_events`, dead-letter to `<topic>.dlq`, retry transient failures) is on [Integration](/docs/ecosystem/integration). The projector additionally consumes this service's own analytics topic.
