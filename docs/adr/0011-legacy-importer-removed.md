# 11. The legacy appointment importer is removed (ADR 0019 stage E, dock-scheduling side)

Status: Accepted (2026-11-10). Amends ADR 0002 (A12, the LegacyImport policy and the ImportLegacyAppointment row), ADR 0003 (the
inbound-receiving `dockappointment.*` consumed row and its `DOCK_LEGACY_IMPORT_CONSUMER_GROUP`) and ADR 0009 (the legacy import
consumer and `LegacyUseCases`); each now points here. Everything else in them stands. Subtractive: no published contract changes, no
migration.

## Context

inbound-receiving's ADR 0019 moved the DockAppointment to this service by a strangler: (A) this service imports the appointments
inbound-receiving already holds, from its legacy `inbound-receiving.dockappointment.*` events on `warehouse.inbound-receiving.events`;
(B) inbound-receiving backfills them with a one-shot `api republish-appointments` command; (C) inbound-receiving keeps a local copy and
answers its writes with 410; (D) readers switch; (E) decommission. Stage A was the legacy importer: the optional consumer enabled by
`DOCK_LEGACY_IMPORT_CONSUMER_GROUP` (off when unset), the `LegacyImport` policy (ADR 0002 A12, D-2) and the `ImportLegacy` aggregate
constructor.

Verified on the live cluster on 2026-11-10:

- `DOCK_LEGACY_IMPORT_CONSUMER_GROUP` is unset in dock-scheduling, so the importer never ran and no appointment was ever imported.
- inbound-receiving's appointment copy is empty (`GET /appointments` returns `{"items":[]}`).
- `warehouse.inbound-receiving.events` holds zero messages, and inbound-receiving's side of the strangler (the republish command and the
  legacy encoders) is removed in its own ADR 0022 at the same time, so no legacy event can be produced again.

Nothing legacy exists to import now or later.

## Decision

Remove the importer end to end:

- the consumer `internal/adapters/inbound/kafka/legacy_import_consumer.go` (`NewLegacyImportConsumer`, `LegacyHandlers`,
  `LegacyUseCases`, the four `TypeLegacy*` constants) and its tests, with the `LegacyImport` group in `Groups`/`GroupsFromEnv` and
  `EnvLegacyImportGroup`; `Build` now always builds the four mandatory consumers (yard, facility, asn, cutoff);
- the use cases `ConsumeLegacyBooked`, `ConsumeLegacyCheckedIn`, `ConsumeLegacyCancelled`, `ConsumeLegacyCompleted`, `legacyFact`,
  `LegacyBooking` and the ignored-fact reason `not_legacy_import` (`IgnoredNotLegacy`), which only the importer produced;
- the domain constructor `dockappointment.ImportLegacy` and `LegacyParams`;
- the `DOCK_LEGACY_IMPORT_CONSUMER_GROUP` env and the chart value `config.dockLegacyImportConsumerGroup` (configmap, `values.yaml`, the
  `ci/all-components-values.yaml` fixture and the render tests);
- the four consumed `inbound-receiving.dockappointment.*` messages and their schemas in `apis/asyncapi.yaml`. The topic
  `warehouse.inbound-receiving.events` stays consumed, for the ASN consumer alone (`ASNRegistered`, `ASNCancelled`).

Kept on purpose:

- **The `legacy-import` value of `provenance`.** It is part of the published `DockAppointment` view (`apis/openapi.yaml`,
  `provenance: native | legacy-import`) and of the `DockAppointmentBooked` payload, and the database check constraint admits it. A
  published enum value is a contract and removing it is a breaking change for readers that may switch on it. The value is now
  historical: nothing in this service sets it, and `Rehydrate` still accepts it so a stored row (none exists on the cluster) could
  still be read. A later decision with a named reason can retire it as a `v2`.
- **The door exclusion constraint's scope** (ADR 0005 A19: native, non-override, active rows). Legacy rows were one of the exclusions;
  the constraint is unchanged, no migration.

## Consequences

- One fewer optional consumer and one fewer environment variable: a deployment that still sets `DOCK_LEGACY_IMPORT_CONSUMER_GROUP`
  has it ignored (the service reads only the variables it knows). warehouse-infra `helm-values/dock-scheduling.yaml` sets the chart
  value `config.dockLegacyImportConsumerGroup` to `""` (off) and mentions it in its header comments; once this chart release is
  deployed, that key and the comments are dead and warehouse-infra can drop them in its own change.
- The metric `dock_scheduling_ignored_facts_total` no longer has the `not_legacy_import` reason.
- Dock-scheduling no longer consumes any `com.warehouse.wms.inbound-receiving.dockappointment.*` type. Its dependency on
  inbound-receiving is the two ASN events only.
- If the appointments ever had to be seeded from inbound-receiving again, that would be a new, scoped decision (a one-off script or a
  migration), not a resurrected long-lived consumer.
