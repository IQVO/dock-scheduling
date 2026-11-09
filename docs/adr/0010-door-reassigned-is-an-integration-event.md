# 10. DockAppointmentDoorReassigned is an integration event

Status: Accepted (2026-11-09). Amends the `DockAppointmentDoorReassigned` row of ADR 0004 (CloudEvents catalogue) and the
analytics-only list of ADR 0006 section 1; everything else in both stands. Additive: no payload, `type`, key or analytics-stream
change, no migration.

## Context

inbound-receiving keeps a local copy of the INBOUND dock appointments fed from `warehouse.dock-scheduling.events` (its ADR 0019) and
stamps a receipt with the appointment's door. When yard-management docks a trailer at another door than the booked one, this context
applies ActualDoorWins (ADR 0002, ADR 0003) and raises
`com.warehouse.wms.dock-scheduling.dockappointment.DockAppointmentDoorReassigned`. Until now ADR 0004 routed that fact ONLY to
`warehouse.dock-scheduling.analytics`, and the analytics topic is this context's internal data product (ADR 0006), not a contract.
So inbound-receiving's copy kept the OLD door, and a receipt opened after the reassignment recorded the wrong door.

Alternatives weighed:

- **inbound-receiving reads the analytics topic.** Rejected: it would turn an internal data product into a contract nobody versions
  for outside consumers, and couple a sibling to this context's projector-facing stream.
- **inbound-receiving derives the door from yard-management's `TrailerDocked`.** Rejected: the appointment's door is
  dock-scheduling's decision (the state machine decides whether the physical door wins, e.g. not for a terminal appointment);
  re-deriving it in a consumer duplicates that rule and can disagree with it.
- **Raise a `DockAppointmentRescheduled` on a door reassignment.** Rejected: a reschedule is a booking change with its own window
  semantics and consumers (yard-management, warehouse-planning) that would read it as a new plan; a door reassignment is a physical
  fact and keeps the window.

## Decision

`DockAppointmentDoorReassigned` is published on BOTH topics, exactly like `DockAppointmentRescheduled` and the other
integration-and-analytics facts:

- `warehouse.dock-scheduling.events`: `type` `com.warehouse.wms.dock-scheduling.dockappointment.DockAppointmentDoorReassigned`
  (unchanged), `source` `/warehouse/dock-scheduling`, `subject` and Kafka key = appointment id, `dataschema`
  `urn:warehouse:dock-scheduling:events:DockAppointmentDoorReassigned:v1`, CloudEvents 1.0 structured mode.
- `warehouse.dock-scheduling.analytics`: the twin, unchanged (same CloudEvents `id`, `type`, key and payload; only the dataschema
  stream segment differs, `...:analytics:DockAppointmentDoorReassigned:v1`).
- Payload identical on both streams, the one already pinned: `appointment_id`, `site_id`, `from_door_code`, `to_door_code`,
  `version` (the aggregate version after the change).

Mechanism: one outbox row per fact as before (ADR 0005); the relay's encoder (`internal/adapters/outbound/kafka/encoder.go`,
`routeOf`) now classifies the fact as integration-and-analytics, so the relay writes the integration message first and its analytics
twin second. The golden pair `testdata/dock_appointment_door_reassigned{,_analytics}.golden.json` pins both envelopes, and
`apis/asyncapi.yaml` lists the message on the integration channel.

Consumer: inbound-receiving (its appointments local copy, ADR 0019 there), in a parallel change on its side.

## Consequences

- Additive for every existing consumer of `warehouse.dock-scheduling.events`. Verified on each consumer's `origin/develop` (the
  only four services that read the topic):
  - inbound-receiving `internal/adapters/inbound/kafka/dock_appointment_consumer.go`: the `switch e.Type()` returns nil in its
    `default` branch, so an unknown type is committed past untouched (until its parallel change starts handling this type).
  - warehouse-planning `internal/adapters/inbound/kafka/dock_appointment_consumer.go`: the `default` branch of `switch e.Type()`
    returns nil; nothing is claimed or dead-lettered and the offset commits.
  - exception-management `internal/adapters/inbound/kafka/consumer.go`: dispatch is a map lookup on the full `type`; a miss
    returns nil (committed past). Its dock-scheduling consumer handles only `DockAppointmentNoShow`.
  - yard-management `internal/adapters/inbound/kafka/consumer.go`: same map dispatch, a miss returns nil and commits.
- The integration topic carries one more message per door reassignment. Reassignments are rare (one per physical mismatch), so the
  volume change is negligible.
- The payload is now a contract: a breaking change to it is a new `.v2` type with a `:v2` dataschema (ADR 0004), on both streams.
- The analytics projector is unaffected: it reads only the analytics topic, where nothing changed (it still ignores the type).
- `DockAppointmentCheckedIn`, `DockAppointmentCompleted`, `DockAppointmentAtRisk` and `DoorOperatingHoursDeclared` stay
  analytics-only; promoting any of them needs its own decision with a named consumer.
