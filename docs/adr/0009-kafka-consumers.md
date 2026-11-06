# 9. Kafka consumers

Status: Accepted. Implements the consuming side of ADR 0003 (consumed contracts and local copies) on top of the `Consume*` use cases
of ADR 0002 / `internal/application/usecases`. Code: `internal/adapters/inbound/kafka`. `cmd/api` (`consumers.go`) wires all five: it calls
`kafka.GroupsFromEnv` (through `loadConfig`), `kafka.NewTransport`, `kafka.Build` and `kafka.Start`, passes the one `usecases.Service`
(real `telemetry.Metrics`, system clock and ids) and stops them after the HTTP drain and before the relay.

## Consumers

| Consumer | Constructor | Topic | Group env | Handled types (full `type`) | Use cases |
|---|---|---|---|---|---|
| yard | `NewYardConsumer` | `warehouse.yard-management.events` | `YARD_CONSUMER_GROUP` | `...yard-management.yardvisit.VisitCheckedIn`, `...yardvisit.VisitRejected`, `...trailer.TrailerDocked`, `...trailer.TrailerUndocked` | ArrivalFromGate, CancelOnVisitRejected, ActualDoorWins, CompleteOnDoorRelease (all under OutOfOrderYardFacts) |
| facility | `NewFacilityConsumer` | `warehouse.facility.events` | `FACILITY_CONSUMER_GROUP` | `...facility-layout.zone.ZoneRegistered`, `...locationslot.LocationSlotRegistered`, `...locationslot.LocationSlotDecommissioned` | DoorView and the zone -> site join (X5 pending-until-zone) |
| asn | `NewAsnConsumer` | `warehouse.inbound-receiving.events` | `ASN_CONSUMER_GROUP` | `...inbound-receiving.asn.ASNRegistered`, `...asn.ASNCancelled` | AsnView, CancelWhenAllAsnsCancelled |
| cutoff | `NewCutoffConsumer` | `warehouse.process-path-management.events` | `CPT_CONSUMER_GROUP` | `com.warehouse.wes.process-path-management.cptschedule.CPTScheduleChanged` | CutoffView (store only, D-3) |
| legacy import | `NewLegacyImportConsumer` | `warehouse.inbound-receiving.events` | `DOCK_LEGACY_IMPORT_CONSUMER_GROUP` | `...inbound-receiving.dockappointment.DockAppointmentBooked`, `...CheckedIn`, `...Cancelled`, `...Completed` | LegacyImport (strangler stage A, D-2) |

The asn consumer and the legacy importer read the same topic under different groups. Each constructor has a `...ForTopic` twin (the
integration tests' unique topics). Each takes a `Transport` (reader factory, dead-letter factory, logger, retry policy) and the narrow
use-case interface it drives (`YardUseCases`, `FacilityUseCases`, `AsnUseCases`, `CutoffUseCases`, `LegacyUseCases`); `*usecases.Service`
implements all of them (compile-time checked).

## Groups

Every group comes from its environment variable; a blank one is a boot error that names the variable (`ErrNoConsumerGroup`;
`GroupsFromEnv` lists every missing variable at once). The one exception is `DOCK_LEGACY_IMPORT_CONSUMER_GROUP`: unset means the
importer is OFF (`NewLegacyImportConsumer` returns `(nil, nil)` and builds no reader; `Build` and `Start` skip it).

## Delivery

At-least-once with an atomic effect. `FetchMessage` + `CommitMessages` (never the auto-committing `ReadMessage`); the offset moves only
after the handler returned nil. The effect and the `processed_events` claim of the CloudEvents `id` share one `UnitOfWork`
(`usecases.consume`), so a rollback un-claims the id and a redelivery is processed, while a duplicate id is a no-op. Failure classes:

- **Not a CloudEvent** (including the retired flat envelope): WARN, commit past, never dead-lettered.
- **Unknown `type`** (dispatch is on the FULL string, never a prefix or short name): ignored without a claim.
- **Poison** (a handled type that can never be applied): dead-lettered to `<topic>.dlq` with `x-dlq-source-topic` and `x-dlq-cause`, then
  committed past. Poison is a payload that does not decode, a required field that is blank (`visit_id`, `site_id`, `door_code`,
  `zoneId`, `siteCode`, `asn_number`, `appointment_id`, `cpt_id`, ...), a time that does not parse as RFC3339, and a value the domain
  refuses as invalid (`ErrInvalidDoorCode`, `ErrInvalidSiteID`, `ErrInvalidCarrier`, `ErrInvalidDirection`, `ErrInvalidDockFlow`,
  `ErrInvalidAppointmentID`, `ErrInvalidWindow`, `ErrDuplicateAsn`, `ErrInvalidRefs`, `ErrInvalidReason`, `ErrDoorDoubleBooked`,
  `ErrAsnAlreadyScheduled`). The dead-letter publish itself is retried forever.
- **Transient** (Postgres down, `ErrConcurrentUpdate`, a lost commit): the SAME message is retried with capped exponential backoff
  (200 ms doubling to 5 s), forever.
- **Shutdown**: a handler that fails because `ctx` was cancelled is neither retried nor dead-lettered (`ctx.Err()` is checked in
  `handleWithDeadLetter`, the deciding function; fleet escape 18b); the offset stays uncommitted.

Facts that are valid but change nothing (unknown or terminal appointment, site mismatch, already applied, not a legacy import, unknown
door) are NOT errors: the use case counts them (`dock_scheduling_ignored_facts_total{reason}`) and the message is committed.

## Decoding

Tolerant reader: only the fields ADR 0003 lists are declared; unknown fields are ignored. facility-layout is camelCase (and carries
`eventName`, `eventType`, `occurredAt`); the consumer also accepts the snake_case spelling and an upper- or lower-case `Dock` role. An
absent `role` is Storage. A blank `appointment_id` / JSON null (a walk-in, a drop) is valid and applies nothing. A missing time field
falls back to the CloudEvents `time` attribute; a window bound has no fallback.

## Ordering

Yard events are keyed by visit or trailer, not appointment, so no cross-aggregate order exists; the DockAppointment state machine is the
guard (OutOfOrderYardFacts): a release before the gate fact completes a BOOKED appointment with an implicit arrival, a late
`VisitCheckedIn` for a COMPLETED appointment is ignored and counted. The yard `version` is not used as a guard for that reason. A
facility slot that arrives before its zone is held pending in `doors` and resolves when `ZoneRegistered` lands (X5): the join lives in
the DoorView, so no consumer-side buffering is needed. The ASN copy is guarded by state: CANCELLED is sticky. CPTScheduleChanged is a
whole-schedule replace on one partition (keyed by site); the event carries no version and the claim makes a replay a no-op.

## Assumptions

- A1. The yard `version` is informational for dock-scheduling; the state machine, not the version, orders yard facts.
- A2. A blank `site_id` on a yard event is poison (every yard event carries it by contract), although a blank `appointment_id` is not.
- A3. A legacy `DockAppointmentBooked` whose door is unknown at import time is skipped and counted (`unknown_door`), not retried; the
  importer must be enabled after the facility consumer caught up, otherwise the appointment is not imported and its later legacy events
  are ignored as `unknown_appointment` (reported as a model gap).
- A4. A topic that does not exist yet is kafka-go's internal retry, logged through the reader's `ErrorLogger`, never a crash.
