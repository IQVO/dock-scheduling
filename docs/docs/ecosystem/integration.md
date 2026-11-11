---
id: integration
title: Integration
sidebar_label: Integration
description: "Every upstream and downstream of dock-scheduling: protocol, contract, event types and failure behaviour, and the edges that do not exist."
---

# Integration

All integration is **asynchronous over Kafka** (CloudEvents 1.0, structured JSON, one broker platform-wide) plus this service's own unauthenticated REST and MCP APIs. `dock-scheduling` **calls no other service synchronously**: it keeps small event-fed local copies instead and validates bookings against them.

Source: `internal/adapters/inbound/kafka/*`, `internal/adapters/outbound/kafka/encoder.go`, `apis/asyncapi.yaml`, [ADR 0003](/docs/adr/0003-consumed-contracts-and-local-copies), [ADR 0004](/docs/adr/0004-cloudevents-catalogue), [ADR 0009](/docs/adr/0009-kafka-consumers); the consumers' side in the neighbours' own repositories (`origin/develop`) and the yard context map in the central documentation site.

Neighbour patterns follow the yard context map: the yard and facility feeds are customer/supplier or conformist upstreams, and this service conforms to the upstream's payloads (it never negotiates them).

## Upstreams (this service consumes)

| Upstream | Topic | Event types consumed | Effect | Pattern |
| --- | --- | --- | --- | --- |
| `yard-management` | `warehouse.yard-management.events` | `com.warehouse.wms.yard-management.yardvisit.VisitCheckedIn` | Applies the arrival: `BOOKED` to `CHECKED_IN` with an arrival status (early, on time, late). | Customer/Supplier |
| | | `...yardvisit.VisitRejected` | Cancels the `BOOKED` appointment with reason `TURNED_AWAY`. | |
| | | `...trailer.TrailerDocked` | Door at which the trailer really docked wins: the appointment's door is changed (no overlap refusal), later overlapping appointments are flagged at risk (`DOOR_REASSIGNED_OVERLAP`), `DockAppointmentDoorReassigned` is published. | |
| | | `...trailer.TrailerUndocked` | Completes the appointment (an implicit arrival if it was still `BOOKED`). | |
| `facility-layout` | `warehouse.facility.events` | `com.warehouse.wms.facility-layout.zone.ZoneRegistered`, `...locationslot.LocationSlotRegistered`, `...locationslot.LocationSlotDecommissioned` | Maintains the `zones` and `doors` copies: a door's site (through its zone), dock flow and active flag. | Conformist |
| `inbound-receiving` | `warehouse.inbound-receiving.events` | `com.warehouse.wms.inbound-receiving.asn.ASNRegistered`, `...asn.ASNCancelled` | Maintains the `asns` copy; an ASN cancellation cancels an INBOUND `BOOKED` appointment whose ASNs are now all cancelled (reason `ASN_CANCELLED`). | Conformist |
| `process-path-management` | `warehouse.process-path-management.events` | `com.warehouse.wes.process-path-management.cptschedule.CPTScheduleChanged` | Replaces the site's cutoff copy (the whole schedule): outbound bookings validate `cptId` against it. | Conformist |

Behaviour common to every consumer (`internal/adapters/inbound/kafka/kafka.go`, `consumer.go`):

- Decode CloudEvents 1.0 only and dispatch on the **full `type`** string. A message that is not a valid CloudEvent is logged and committed past; an unhandled type is ignored.
- Every effect is a use case that claims the CloudEvents `id` in `processed_events` inside the same transaction as the effect: redelivery changes nothing.
- A message of a handled type that can never be applied (undecodable payload, missing id, a value the domain refuses) is republished to `<topic>.dlq` with `x-dlq-source-topic` and `x-dlq-cause`, then committed past.
- A transient failure (Postgres down, concurrent update) is retried on the same message with capped exponential backoff, forever; the partition waits.
- Facts the policies deliberately do not apply (unknown or terminal appointment, door not yet known, another site) are counted in `dock_scheduling_ignored_facts_total{reason}` and marked processed.
- Yard facts are keyed by visit or trailer, not by appointment, so they can arrive in any order; a door release on a `BOOKED` appointment completes it and records the arrival as implicit.
- A new consumer group starts at the first offset and replays; the local copies converge.

## Downstreams (consume this service's events)

Topic `warehouse.dock-scheduling.events`, key = appointment id (site id for calendar events), source `/warehouse/dock-scheduling`, dataschema `urn:warehouse:dock-scheduling:events:<Event>:v1`. Published through the transactional outbox, at-least-once, the CloudEvents id persisted at enqueue time.

| Downstream | Event types it consumes (from its own consumer code on `origin/develop`) | Use |
| --- | --- | --- |
| `yard-management` | `DockAppointmentBooked`, `DockAppointmentRescheduled`, `DockAppointmentCancelled`, `DockAppointmentNoShow`, `DoorClosureScheduled`, `DoorClosureLifted` | Expected arrivals and the door calendar; a no-show frees the open move. |
| `inbound-receiving` | `DockAppointmentBooked`, `DockAppointmentRescheduled`, `DockAppointmentCancelled`, `DockAppointmentDoorReassigned` | Its `appointments` local copy (its ADR 0019): validates a receipt's appointment and stamps the physical door. |
| `warehouse-planning` | `DockAppointmentBooked`, `DockAppointmentRescheduled`, `DockAppointmentCancelled`, `DockAppointmentNoShow` (per the yard context map; its ADR 0020) | Inbound labour demand per site, replacing the old inbound-receiving source. |
| `outbound-shipping` | `DockAppointmentBooked` | Outbound appointment awareness. |
| `exception-management` | `DockAppointmentNoShow` | Opens a case. |

Not on this topic: `DockAppointmentCheckedIn`, `DockAppointmentCompleted`, `DockAppointmentAtRisk` and `DoorOperatingHoursDeclared` are **analytics only** (`warehouse.dock-scheduling.analytics`, consumed by this service's own projector, not an integration contract). See [Events](/docs/api-reference/events) for the full catalogue. Several neighbours mention `CheckedIn` or `Completed` in comments to say it is analytics only; none receives it.

Failure behaviour for consumers is each consumer's own (retry, dead-letter `warehouse.dock-scheduling.events.dlq`); on the producer side a failing row is retried by the relay up to `OUTBOX_MAX_ATTEMPTS`, then excluded and logged (see the [Runbook](/docs/operations/runbook)). Delivery is at-least-once: consumers must dedupe on the CloudEvents `id`.

## Synchronous interfaces

| Client | Interface | Notes |
| --- | --- | --- |
| Carrier schedulers, dock planners, consoles, other contexts | REST on `cmd/api` (`/api/dock-scheduling` behind Kong) | OpenAPI `apis/openapi.yaml`; unauthenticated; the gateway adds no auth. |
| Agents | MCP on `cmd/mcp` (`/api/mcp/dock-scheduling` behind Kong) | Five read tools, four L3 write tools; see [MCP tools](/docs/mcp/tools). Not running on k3s (switched off in `warehouse-infra`, read 2026-11-10): no pod, no route. |
| Report readers | REST on `cmd/reports` (`/api/dock-scheduling/reports` behind Kong) | `apis/openapi-reports.yaml`; reads the analytical database only. Not running on k3s (analytics switched off, read 2026-11-10): no pod, no route. |
| Kubernetes CronJob | `POST /no-show-sweeps` via `cmd/sweep` | The only timer; nothing in the service calls the endpoint itself. Switched off on k3s (read 2026-11-10): no CronJob exists, so the endpoint is called by hand. |

## Edge status

Read-only on 2026-11-10 (`kubectl get pods,deploy -n warehouse-systems`, context `k3s-multipass`). An edge is **live** only when both ends run in the cluster **and** the consumer or producer code and the spec declare it. `dock-scheduling` is applied (Argo application Synced and Healthy, api pod Ready at `v0.1.2`; see the [Runbook](/docs/operations/runbook#what-runs-on-the-k3s-cluster)), so an edge is live when the neighbour also runs. The consumer groups are those of the `warehouse-infra` helm values (`helm-values/dock-scheduling.yaml` and the neighbour's own file) and of the running Deployments' environment. "Live" here means both ends run and declare the edge; it does **not** say events flow without lag: consumer group offsets and lag need `kubectl exec` and were not read.

| Edge | Direction | Consumer group | Neighbour in the cluster (image running) | Status |
| --- | --- | --- | --- | --- |
| `yard-management` | in: `warehouse.yard-management.events` | `dock-scheduling-yard` | `yard-management` api and `yard-management-relay` Ready (`da1e30e`, `v0.1.2`) | live |
| `yard-management` | out: `warehouse.dock-scheduling.events` | `yard-management-dock-scheduling` | same pods; the consumer is in its api | live |
| `facility-layout` | in: `warehouse.facility.events` | `dock-scheduling-facility` | `facility-layout` Ready (`1305245`) | live |
| `inbound-receiving` | in: `warehouse.inbound-receiving.events` | `dock-scheduling-asn` | `inbound-receiving` Ready (`484490f`, v2.0.6) | live |
| `inbound-receiving` | out: `warehouse.dock-scheduling.events` | `inbound-receiving-dock-appointments` | same pod; `dock_appointment_consumer.go` at `484490f` | live |
| `process-path-management` | in: `warehouse.process-path-management.events` | `dock-scheduling-cpt` | `process-path-management` Ready (`a336eaf`) | live |
| `warehouse-planning` | out: `warehouse.dock-scheduling.events` | `warehouse-planning-dock-appointments` | `warehouse-planning` Ready (`ef8b946`); `dock_appointment_consumer.go` | live |
| `exception-management` | out: `warehouse.dock-scheduling.events` | `exception-management-dock-scheduling` | `exception-management` Ready (`35bb7d9`); `dock_scheduling_exceptions_consumer.go`; the Deployment carries `DOCK_SCHEDULING_EXCEPTIONS_CONSUMER_GROUP` | live |
| `outbound-shipping` | out: `warehouse.dock-scheduling.events` | none: no `warehouse-infra` entry or helm values found for it | no pod (`kubectl get deploy outbound-shipping`: NotFound) | not live, not registered in infra |

The consumers' side of these rows (the event types a neighbour reads) comes from the neighbours' own code and specs at the commit each pod runs (the image tag is that commit), as the tables above say; the group and neighbour columns come from `warehouse-infra` `origin/develop` and the cluster listing. The `exception-management` row was "not wired in infra" until the `exception-management` group ids were pinned (`warehouse-infra` commit `a703971`, 2026-11-09); the running Deployment now carries it.

Not verified: the offsets and lag of every group, whether the topics hold the events a consumer expects, and delivery from this service's outbox. The api log of the dock pod shows `kafka reader error; retrying` lines with `i/o timeout` on the yard, facility, inbound-receiving and process-path-management topics at 23:21Z on 2026-11-10 (quoted in the [Runbook](/docs/operations/runbook#what-runs-on-the-k3s-cluster)); they were not diagnosed.

## Edges that do not exist

- **No outbound calls.** This service calls no sibling over REST, MCP or A2A; there is no HTTP client in `cmd/api`. A booking that needs a door, ASN or cutoff the copies do not hold is refused (`unknown-door`, `unknown-asn`, `missing-outbound-ref`), never looked up synchronously.
- **No A2A server** (`cmd/a2a` does not exist).
- **No ETA feed.** `transport-visibility` does not exist; ETAs enter through `POST .../eta-reports` and the `report_eta` MCP tool.
- **No automatic pickup booking from the CPT schedule** (`PickupFromCutoffs` is phase 2): the cutoff copy is stored and used only to validate `cptId`.
- **No retail-network edge.** No `retailnetwork.*` topic is consumed and no retail-network service is called.
- **No legacy appointment feed.** The stage A importer of the ADR 0019 strangler was removed (ADR 0011): this context consumes no inbound-receiving `dockappointment.*` type, only the two ASN events.

Where the Helm chart or OpenAPI mention a "kind" cluster (`servers` in `apis/openapi.yaml`, the image comment in `values.yaml`) they predate the move to k3s; the contract itself is unaffected.
