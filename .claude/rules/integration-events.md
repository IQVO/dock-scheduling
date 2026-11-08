---
paths:
  - "internal/adapters/**/kafka/**"
  - "internal/adapters/outbound/events/**"
  - "apis/asyncapi*"
---

# Cross-service integration events (Kafka)

dock-scheduling BOTH publishes and consumes. It publishes every
DockAppointment/DockCalendar fact to `warehouse.dock-scheduling.events`
(integration) and to `warehouse.dock-scheduling.analytics` (consumed only by
this service's own `cmd/projector`); it consumes
`warehouse.yard-management.events`, `warehouse.facility.events`,
`warehouse.inbound-receiving.events` and
`warehouse.process-path-management.events` (ADR 0003). The outbox relay
(`cmd/relay`, also started inline by `cmd/api`) is the only writer.

## Events: CloudEvents 1.0 is MANDATORY

Every Kafka message this service produces or consumes (integration
`warehouse.dock-scheduling.events` AND analytics
`warehouse.dock-scheduling.analytics`) is a CloudEvents 1.0 event in
structured content mode. This is a hard fleet rule, not a preference — there
is nothing to "choose" here:

- No flat envelope (`event_id`/`event_type`/`occurred_at`), no dual-write,
  no dual-read, no envelope toggle env var (`EVENT_ENVELOPE_MODE` is gone
  fleet-wide). `internal/architecture/fitness_test.go`'s
  `TestNoEventEnvelopeToggleOrFlatEnvelope` fails CI on any of them.
- Build/validate/(un)marshal with `github.com/cloudevents/sdk-go/v2/event`
  (latest v2) via ONE helper package,
  `internal/adapters/kafka/cloudevents/` (with its golden exact-JSON tests).
  Transport stays `segmentio/kafka-go` (no sdk-go protocol/client packages,
  no hand-rolled CloudEvent structs).
- Every produced message carries the Kafka header
  `content-type: application/cloudevents+json; charset=UTF-8`
  (`cloudevents.ContentTypeHeader()`), next to the W3C trace headers
  (`traceparent`/`tracestate` stay in headers, never duplicated into
  extension attributes). Message key = aggregate id, `kafkago.Hash{}`
  balancer.
- Required attributes: `specversion=1.0`; `id` (UUID v4 minted ONCE per
  domain event and persisted with the outbox row, so redelivery carries the
  same id); `source=/warehouse/dock-scheduling`; `type`; `subject` (aggregate
  instance id, never empty); `time` (domain occurred-at, UTC);
  `datacontenttype=application/json`;
  `dataschema=urn:warehouse:dock-scheduling:<events|analytics>:<EventName>:v<N>`.
  No custom extension attributes without an ADR.
- `type` = `com.warehouse.wms.dock-scheduling.<entity>.<EventName>` (subdomain
  `wms`; this repo's exact prefix is
  `com.warehouse.wms.dock-scheduling`). The SAME `type` names the occurrence
  on both the integration and the analytics topic; `dataschema` names the
  payload shape. Breaking payload change => new `.v2` type + new dataschema
  version, never mutate an existing one.
- Consumers decode with `cloudevents.Decode` (validates), dispatch on the
  FULL `type` string (never a short name or suffix match), ignore unknown
  types, read `time`/`subject` from attributes and the payload via `DataAs`,
  dedupe on `id`, and DLQ/skip — WARN log + commit past, never crash, never
  block the partition, never fall back to parsing a legacy shape — anything
  that fails CloudEvents validation.
- Tests: a golden exact-JSON test per published `type` (all attributes + the
  `content-type` header) in `internal/adapters/outbound/kafka/encoder_test.go`
  (+ `testdata/`); a legacy-flat-message-rejected test per consumer; Kafka
  integration tests via testcontainers only.

Full standard, subdomain table and the fleet's cross-service type catalogue:
the warehouse-docs repository's strategic-design page on the CloudEvents
event standard. It is recorded in this repo as ADR
`docs/adr/0004-cloudevents-catalogue.md`.

### Published types

One row per published event (ADR 0004 is the authority; `subject` is the
aggregate id — appointment id, or site id for the dockcalendar rows).

| `type` | topic(s) | `subject` | `dataschema` |
| --- | --- | --- | --- |
| `com.warehouse.wms.dock-scheduling.dockappointment.DockAppointmentBooked` | events + analytics | appointment id | `urn:warehouse:dock-scheduling:events:DockAppointmentBooked:v1` |
| `com.warehouse.wms.dock-scheduling.dockappointment.DockAppointmentRescheduled` | events + analytics | appointment id | `urn:warehouse:dock-scheduling:events:DockAppointmentRescheduled:v1` |
| `com.warehouse.wms.dock-scheduling.dockappointment.DockAppointmentCancelled` | events + analytics | appointment id | `urn:warehouse:dock-scheduling:events:DockAppointmentCancelled:v1` |
| `com.warehouse.wms.dock-scheduling.dockappointment.DockAppointmentNoShow` | events + analytics | appointment id | `urn:warehouse:dock-scheduling:events:DockAppointmentNoShow:v1` |
| `com.warehouse.wms.dock-scheduling.dockappointment.DockAppointmentCheckedIn` | analytics only | appointment id | `urn:warehouse:dock-scheduling:analytics:DockAppointmentCheckedIn:v1` |
| `com.warehouse.wms.dock-scheduling.dockappointment.DockAppointmentDoorReassigned` | analytics only | appointment id | `urn:warehouse:dock-scheduling:analytics:DockAppointmentDoorReassigned:v1` |
| `com.warehouse.wms.dock-scheduling.dockappointment.DockAppointmentCompleted` | analytics only | appointment id | `urn:warehouse:dock-scheduling:analytics:DockAppointmentCompleted:v1` |
| `com.warehouse.wms.dock-scheduling.dockappointment.DockAppointmentAtRisk` | analytics only | appointment id | `urn:warehouse:dock-scheduling:analytics:DockAppointmentAtRisk:v1` |
| `com.warehouse.wms.dock-scheduling.dockcalendar.DoorClosureScheduled` | events (key site_id) + analytics | site id | `urn:warehouse:dock-scheduling:events:DoorClosureScheduled:v1` |
| `com.warehouse.wms.dock-scheduling.dockcalendar.DoorClosureLifted` | events (key site_id) + analytics | site id | `urn:warehouse:dock-scheduling:events:DoorClosureLifted:v1` |
| `com.warehouse.wms.dock-scheduling.dockcalendar.DoorOperatingHoursDeclared` | analytics only (key site_id) | site id | `urn:warehouse:dock-scheduling:analytics:DoorOperatingHoursDeclared:v1` |

### Consumed types

The EXACT `type` strings were read from each producer's
`origin/develop:apis/asyncapi.yaml` (ADR 0003 holds the full table with
fields).

| `type` | topic | producer |
| --- | --- | --- |
| `com.warehouse.wms.yard-management.yardvisit.VisitCheckedIn` | `warehouse.yard-management.events` | yard-management |
| `com.warehouse.wms.yard-management.trailer.TrailerDocked` | `warehouse.yard-management.events` | yard-management |
| `com.warehouse.wms.yard-management.trailer.TrailerUndocked` | `warehouse.yard-management.events` | yard-management |
| `com.warehouse.wms.facility-layout.locationslot.LocationSlotRegistered` | `warehouse.facility.events` | facility-layout |
| `com.warehouse.wms.facility-layout.locationslot.LocationSlotDecommissioned` | `warehouse.facility.events` | facility-layout |
| `com.warehouse.wms.facility-layout.zone.ZoneRegistered` | `warehouse.facility.events` | facility-layout |
| `com.warehouse.wms.inbound-receiving.asn.ASNRegistered` | `warehouse.inbound-receiving.events` | inbound-receiving |
| `com.warehouse.wms.inbound-receiving.asn.ASNCancelled` | `warehouse.inbound-receiving.events` | inbound-receiving |
| `com.warehouse.wes.process-path-management.cptschedule.CPTScheduleChanged` | `warehouse.process-path-management.events` | process-path-management |
| `com.warehouse.wms.inbound-receiving.dockappointment.DockAppointment*` (Booked/CheckedIn/Cancelled/Completed) | `warehouse.inbound-receiving.events` | inbound-receiving (legacy importer, strangler stage A) |

## Consumer group id

Every consumer's group id comes from the environment, never a hardcoded
string literal — `internal/architecture/fitness_test.go`'s
TestKafkaConsumerGroupNeverHardcodedInline enforces this. The five variables
(`YARD_CONSUMER_GROUP`, `FACILITY_CONSUMER_GROUP`, `ASN_CONSUMER_GROUP`,
`CPT_CONSUMER_GROUP` and the optional strangler-stage-A
`DOCK_LEGACY_IMPORT_CONSUMER_GROUP`) are read by
`internal/adapters/inbound/kafka/groups.go` from `cmd/api`'s boot; the
projector's group is `ANALYTICS_CONSUMER_GROUP` (`cmd/projector`). No
consumer replays from FirstOffset into an in-memory read model: the local
copies (doors, ASNs, cutoffs) live in Postgres, so a plain stable group id
is correct.
