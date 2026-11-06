# 3. Consumed contracts and local copies

Status: Accepted. No live REST or MCP lookup into a sibling: everything this service needs from another context is an
event-fed local copy in its own Postgres. Every consumer: stable consumer group read from an env var (a missing group is a boot
error naming the variable), CloudEvents `id` claimed in `processed_events` in the same transaction as the effect, dispatch on the
FULL `type`, unknown types and unknown fields ignored, a message of a handled type that can never be applied is dead-lettered to
`<consumed topic>.dlq` (headers `x-dlq-source-topic`, `x-dlq-cause`), the offset committed after the transaction; a transient failure
is retried on the same message with capped exponential backoff.

The types, topics and fields below were READ from each producer's `origin/develop:apis/asyncapi.yaml` on 2026-11-05 (yard-management
has no contract on develop yet: its three events are pinned by the cross-context contract of the yard build).

## Consumed events

| Producer | Topic | Full `type` | Key | Fields relied on | Group env |
|---|---|---|---|---|---|
| facility-layout | `warehouse.facility.events` | `com.warehouse.wms.facility-layout.locationslot.LocationSlotRegistered` | locationCode | `locationCode`, `zoneId`, `role` (absent = Storage), `dockFlow` (Inbound/Outbound/Both) | `FACILITY_CONSUMER_GROUP` |
| facility-layout | same | `com.warehouse.wms.facility-layout.locationslot.LocationSlotDecommissioned` | locationCode | `locationCode` | same |
| facility-layout | same | `com.warehouse.wms.facility-layout.zone.ZoneRegistered` | zoneId | `zoneId`, `siteCode` | same |
| inbound-receiving | `warehouse.inbound-receiving.events` | `com.warehouse.wms.inbound-receiving.asn.ASNRegistered` | asn number | `asn_number`, `expected_arrival`, `lines` | `ASN_CONSUMER_GROUP` |
| inbound-receiving | same | `com.warehouse.wms.inbound-receiving.asn.ASNCancelled` | asn number | `asn_number` | same |
| process-path-management | `warehouse.process-path-management.events` | `com.warehouse.wes.process-path-management.cptschedule.CPTScheduleChanged` | site_id | `site_id`, `timezone`, `cutoffs[]{cpt_id, local_time, days_of_week, ship_method}` | `CPT_CONSUMER_GROUP` |
| yard-management | `warehouse.yard-management.events` | `com.warehouse.wms.yard-management.yardvisit.VisitCheckedIn` | visit_id | `visit_id`, `site_id`, `appointment_id` (nullable), `arrived_at`, `version` | `YARD_CONSUMER_GROUP` |
| yard-management | same | `com.warehouse.wms.yard-management.yardvisit.VisitRejected` | visit_id | `visit_id`, `site_id`, `appointment_id` (nullable), `reason`, `rejected_at` (no `version`: advisory/terminal) | same |
| yard-management | same | `com.warehouse.wms.yard-management.trailer.TrailerDocked` | trailer_id | `trailer_id`, `site_id`, `door_code`, `appointment_id` (nullable), `visit_id`, `docked_at`, `version` | same |
| yard-management | same | `com.warehouse.wms.yard-management.trailer.TrailerUndocked` | trailer_id | `trailer_id`, `site_id`, `door_code`, `appointment_id` (nullable), `undocked_at`, `version` | same |
| inbound-receiving (legacy importer, strangler stage A) | `warehouse.inbound-receiving.events` | `com.warehouse.wms.inbound-receiving.dockappointment.DockAppointmentBooked` / `...CheckedIn` / `...Cancelled` / `...Completed` | appointment id | Booked: `appointment_id`, `door_code`, `carrier`, `window_start`, `window_end`, `asn_numbers`; CheckedIn: `appointment_id`, `checked_in_at`; Cancelled: `appointment_id`, `reason`; Completed: `appointment_id`, `completed_at` | `DOCK_LEGACY_IMPORT_CONSUMER_GROUP` (unset = importer off) |

Facility-layout and inbound-receiving payloads: facility-layout is camelCase and also carries `eventName`, `eventType`,
`occurredAt` in `data`; inbound-receiving and the process-path and yard events are snake_case. Decoding is tolerant of both
and of extra fields. Process-path-management is a `wes` subdomain event (type segment `wes`), the others are `wms`.

## Local copies (migration `0001`)

| Table | Fed by | Rule |
|---|---|---|
| `zones(zone_id pk, site_code)` | ZoneRegistered | upsert |
| `doors(location_code pk, zone_id, dock_flow)` | LocationSlotRegistered with role Dock | upsert; a re-registration with another role removes the row; LocationSlotDecommissioned deletes it |
| `asns(asn_number pk, state, expected_arrival)` | ASNRegistered, ASNCancelled | state REGISTERED or CANCELLED; CANCELLED is sticky (a late ASNRegistered never revives it); a cancel before the register inserts CANCELLED |
| `cutoffs(site_id, cpt_id, timezone, local_time, days_of_week, ship_method, pk(site_id, cpt_id))` | CPTScheduleChanged | the event is the whole schedule of the site: replace all rows of the site in one transaction |
| `processed_events(event_id pk, consumer, claimed_at)` | every consumer | the dedupe guard, one row per CloudEvents id |

A door's site is `zones.site_code` joined on `doors.zone_id` at use time (X5). A door whose zone has not arrived yet is held pending
(the row exists, the join is empty) and a booking for it is refused with ErrUnknownDoor until the zone event lands.

Ignored facts (unknown, terminal, wrong-site, already-applied, not-a-legacy-import, unknown door) and implicit arrivals are counted in
Prometheus counters (`dock_scheduling_ignored_facts_total{reason}`, `dock_scheduling_implicit_arrivals_total`), not in a table.

## Yard facts and ordering

Yard events are keyed by visit or trailer, not by appointment, so they can arrive in any order across partitions. The policies
ArrivalFromGate, ActualDoorWins, CompleteOnDoorRelease and OutOfOrderYardFacts (ADR 0002) therefore:

- load the appointment inside the claim transaction under `SELECT ... FOR UPDATE`;
- apply a fact to BOOKED or CHECKED_IN appointments only (a BOOKED appointment released by TrailerUndocked completes with an
  implicit arrival; a late VisitCheckedIn for a COMPLETED one is ignored);
- ignore and count a fact for an unknown appointment id, or a CANCELLED/NO_SHOW/COMPLETED appointment (an unknown appointment is
  not an error: it may be a legacy appointment not yet imported);
- ignore a fact whose `site_id` differs from the appointment's;
- VisitRejected (policy CancelOnVisitRejected, X2) cancels a BOOKED appointment with reason TURNED_AWAY; any other state is
  ignored and counted.

## Database backstops

- Exclusion constraint `dock_appointments_no_overlap` on `(door_code WITH =, tstzrange(window_start, window_end, '[)') WITH &&)
  WHERE state IN ('BOOKED','CHECKED_IN') AND NOT door_override` (extension `btree_gist`), behind the per-door advisory lock
  `pg_advisory_xact_lock(hashtextextended(door_code, 0))`. ActualDoorWins reassigns without the application overlap check; the
  constraint would reject a reassignment onto a door where another active appointment overlaps, so the reassignment is applied
  as a door-reservation exception: the physical fact wins, the row is written with `door_override = true` (excluded from the
  constraint; the aggregate sets it in ReassignDoor and a checked reschedule clears it) and the overlap is surfaced as DockAppointmentAtRisk (cause DOOR_REASSIGNED_OVERLAP).
- The importer's id claim is the aggregate id itself: the legacy appointment id (`appt-<uuid>`) is the primary key, so a replayed
  legacy Booked event is a no-op (`INSERT ... ON CONFLICT DO NOTHING` in the same transaction as the `processed_events` claim).
- Partial unique index `dock_appointment_asns_active` on `(asn_number) WHERE active` over the `dock_appointment_asns` rows of
  BOOKED/CHECKED_IN appointments is the backstop of ErrAsnAlreadyScheduled (D-12); legacy imports do not enforce the rule (D-2) and
  write their ASN rows with `active = false` while they are not unique.
