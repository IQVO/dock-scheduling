# CLAUDE.md — dock-scheduling

`dock-scheduling` is a Supporting bounded context of the yard domain (`wms` tier,
CloudEvents subdomain `wms`, ADR 0001 and ADR 0004) in the `warehouse-systems`
fleet (GitHub org `IQVO`, Go module `github.com/claudioed/dock-scheduling`). It
owns the door calendar of every site: every booked window on every dock door,
inbound and outbound, from booking through arrival to door release, the
no-double-booking rule, door opening hours and closures, ETA-driven risk and
no-shows. Aggregates `DockAppointment` and `DockCalendar`. Yard-management owns
the physical yard (gate, trailers, doors in use). Six binaries: `cmd/api` (REST
`:8080`, in-process outbox relay, the Kafka consumers), `cmd/mcp` (MCP `:8090`),
`cmd/projector` (`:8091`, only writer of the analytical database), `cmd/reports`
(`:8092`, read-only reports), `cmd/relay` (optional stand-alone relay, health on `:8094`)
and `cmd/sweep` (one-shot CronJob that triggers the no-show sweep over REST).
Design source: warehouse-docs `yard/model/model.yaml`.

## Hard rules (each one is in an ADR or fitness-tested)

1. **Hexagonal dependency rule.** `internal/domain` depends on nothing internal
   but domain; `internal/application` only on domain and application; inbound
   and outbound adapters never depend on each other; only `cmd/` wires layers.
   `make arch-test` (`TestHexagonalArchitecture`) enforces it.
2. **CloudEvents 1.0 is MANDATORY** on every Kafka message produced or
   consumed (structured mode, no flat envelope, no envelope toggle). Build and
   decode ONLY through `internal/adapters/kafka/cloudevents/`; `type` is
   `com.warehouse.wms.dock-scheduling.<entity>.<EventName>`, `source`
   `/warehouse/dock-scheduling`, key = subject. A breaking payload change is a
   new `.v2` type. Topics `warehouse.dock-scheduling.events` (integration) and
   `warehouse.dock-scheduling.analytics` (read side). Catalogue:
   `docs/adr/0004-cloudevents-catalogue.md`; payloads `apis/asyncapi.yaml`
   (`TestCloudEventsOnly`, `TestEventCatalogueMatchesContract`).
3. **Events leave through the transactional outbox only** (ADR 0005): the
   aggregate save and its encoded CloudEvents commit in one transaction; the
   relay publishes. Never write to Kafka from a handler or use case. Every
   relay passes an attempt cap (`TestOutboxRelaysPassAttemptCap`).
4. **No live lookup into a sibling context** (ADR 0003). Doors, ASNs, CPTs and
   yard facts are event-fed local copies in this service's Postgres. The only
   HTTP client is `cmd/sweep`, calling this service's own API.
5. **Arrival and door release are yard-management facts.** They reach this
   context only as events (ArrivalFromGate, ActualDoorWins,
   CompleteOnDoorRelease); no REST route or MCP tool records them.
6. **No double booking.** Windows are half-open; two active appointments never
   overlap on one door. The check runs under a per-door advisory lock and an
   exclusion constraint backs it (`migrations/`). Do not remove either.
7. **Consumers** (ADR 0009): group ids only from env vars (a missing one is a
   boot error; `TestKafkaConsumerGroupNeverHardcodedInline`); the CloudEvents
   `id` is claimed in `processed_events` in the effect's transaction; dispatch
   on the full `type`; an unappliable message goes to `<topic>.dlq` with a
   cancellation guard (`TestDeadLetterCallsGuardCancellation`).
   `DOCK_LEGACY_IMPORT_CONSUMER_GROUP` is the one optional group (unset = the
   strangler stage A importer is off).
8. **`version` is the ETag** (weak `W/"<version>"`): starts at 1, +1 per
   accepted change; `If-Match` on reschedule and cancel answers 412 on
   mismatch. Every creating POST requires `Idempotency-Key`; PUT is idempotent
   by method.
9. **Real Postgres and Kafka only.** `DATABASE_URL` and `KAFKA_BROKERS` are
   mandatory; `internal/adapters/outbound/memory` is a test double that no
   binary imports (`TestRuntimeBinaryHasNoInMemoryAdapters`).
10. **No auth layer on REST or MCP** (fleet rule): never add bearer/JWT/API-key
    middleware (`TestNoAuthMiddlewareReintroduced`).
11. **MCP authority** (ADR 0007): read tools L1, writes (book, reschedule,
    cancel, report ETA) L3 with a mandatory `initiated_by`; calendar changes
    and the sweep are not exposed over MCP
    (`TestEveryMCPToolDeclaresAuthority`, `TestMCPWriteToolsAreAtLeastL3`).
12. **An ADR comes first** for a new cross-context contract, a new consumed or
    published type, or any auth. ADRs live in `docs/adr/`.

## Commands

```bash
make check-fast   # fmt-check + vet + arch-test + tests of changed packages: run before saying "done"
make check        # fmt-check vet build lint test (lefthook pre-push)
make check-all    # check + coverage (90% gate on domain + application) + arch-test + bdd
make integration  # real Postgres/Kafka via testcontainers (needs Docker)
make mutation     # gremlins on the domain packages (CI job mutation-fast, blocking)
make guide-lint   # these guides: skills load, references resolve, context budget
make harness-test # unit tests of the agent hooks and red-issue loop
```

Run locally: `DATABASE_URL=... KAFKA_BROKERS=... <the *_CONSUMER_GROUP vars> go
run ./cmd/api`; the projector and reports need `ANALYTICS_DATABASE_URL` (ADR
0006). Every variable per binary: `docs/docs/operations/configuration.md`.

## Where things are written down (read before editing the matching area)

- `.claude/rules/domain-model.md`: language, aggregates, invariants, use
  cases. `.claude/rules/rest-api.md`: routes and error slugs.
  `.claude/rules/integration-events.md`: published and consumed events.
- `.claude/rules/fleet/*.md`: fleet-wide rules copied from the harness
  template; never edit them here.
- `docs/adr/`: decisions (index `docs/adr/index.md`). Contracts:
  `apis/openapi.yaml`, `apis/openapi-reports.yaml`, `apis/asyncapi.yaml`.
  Operating docs: `docs/docs/operations/` (configuration, runbook,
  observability, troubleshooting), `docs/docs/development/testing.md`,
  `docs/docs/overview/quickstart.md`. Helm chart: `charts/dock-scheduling`.
- `HARNESS.md`: what each sensor runs. `INCIDENTS.md`: fleet incident log.
  `.claude/memory/MEMORY.md`: repo heuristics.

harness-template: v4
(hooks, guide-lint).

<!-- harness:scoped-rules:start (generated by tools/migrate_v3.py in warehouse-harness-template; do not hand-edit) -->
## Scoped rules and harness

Claude Code loads each rule below automatically when you touch the matching paths. OpenCode and Codex do NOT: read the rule BEFORE editing matching files.

| When touching | Read |
|---|---|
| `internal/domain/**`, `internal/application/**`, `features/**` | `.claude/rules/domain-model.md` |
| `internal/adapters/**/kafka/**`, `internal/adapters/outbound/events/**`, `apis/asyncapi*` | `.claude/rules/integration-events.md` |
| `internal/adapters/inbound/http/**`, `apis/openapi*.yaml`, `apis/openapi/**` | `.claude/rules/rest-api.md` |

Hooks (`scripts/harness/hook.py`, wired for Claude Code, Codex and OpenCode) block pushes to develop/main, `--no-verify`, bare `rm -rf`, and edits to generated files, and feed gofmt/vet findings back after each edit. Before saying "done" run `make check-fast`; the full gate is `make check-all`. `HARNESS_OFF=1` disables the hooks when debugging the harness itself.
<!-- harness:scoped-rules:end -->
