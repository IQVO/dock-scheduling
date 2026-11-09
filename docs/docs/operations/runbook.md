---
id: runbook
title: Runbook
sidebar_label: Runbook
description: "Deploying and operating dock-scheduling: Helm chart, probes, migrations, Kafka topics and consumer groups, outbox relay, dead letters, the no-show sweep and routine procedures."
---

# Runbook

Status: **built on develop, not deployed.** `terraform/apps.yaml` in `warehouse-infra` (`origin/develop`) has no `dock-scheduling` entry and there is no `helm-values/dock-scheduling.yaml`, so nothing in the reference cluster runs this service yet. Everything below is what the chart and the binaries do when it is deployed; the first deployment also needs the [pre-release checklist](/docs/operations/before-first-release).

Source: `charts/dock-scheduling/`, `cmd/*`, `internal/adapters/**`.

## Workloads

One image (`ghcr.io/iqvo/dock-scheduling`) carries six binaries; each workload selects one with `command: ["/app/<name>"]`. The image default entrypoint is `./api`.

| Workload (chart) | Binary | Port | Default | Probes |
|---|---|---|---|---|
| Deployment `component=api` | `/app/api`: REST, inline outbox relay, four Kafka consumers (plus the optional legacy importer) | 8080 (Service port 80) | always on | startup, liveness `GET /healthz`; readiness `GET /readyz` |
| Deployment `component=mcp` | `/app/mcp` | 8090 | `mcp.enabled=false` | `GET /healthz`, `GET /readyz` |
| Deployment `component=relay` | `/app/relay` | 8094 (no Service) | `relay.enabled=false` | `GET /healthz` only |
| Deployment `component=analytics-projector` | `/app/projector` | 8091 (no Service) | `analytics.enabled=false` | `/healthz`, `/readyz` |
| Deployment `component=analytics-reports` + Service `<release>-reports` | `/app/reports` | 8092 (Service port 80) | `analytics.enabled=false` | `/healthz`, `/readyz` |
| CronJob | `/app/sweep` | none | `sweeps.enabled=true`, `*/5 * * * *` | exit code |

The chart refuses to render without `database.url` or `database.existingSecret`, without `kafka.brokers`, and without the four consumer-group values (`templates/_helpers.tpl`). `kafka.enabled=false` is refused.

## Probes: what readiness waits for

- `GET /healthz` is liveness only and is never flipped by shutdown.
- `GET /readyz` on `cmd/api` and `cmd/mcp` is 200 only while the drain gate is open, **Postgres answers a ping** and **a Kafka broker answers a topic-less metadata request**. The verdict is cached 3 s and a check round is bounded at 2 s. While a dependency is down `cmd/api` answers 503 `application/problem+json` (`dependency-unavailable`, `not ready: postgres, kafka`); `cmd/mcp` and `cmd/projector` answer 503 with `{"status":"unavailable","failing":[...]}` or `{"status":"not_ready","failing":[...]}`. During graceful shutdown the first step flips it to `{"status":"not_ready"}`.
- Topics are never part of readiness: the broker auto-creates them on first write.
- `cmd/reports` readiness is the analytical database ping only; `cmd/relay` has no readiness (a transient broker or database failure is retried by the loop, not by a restart).
- The startup probe (`/healthz`, 2 s x 30) gives about 60 s for the boot migrations and the first Postgres ping, which are wrapped in a bounded exponential retry (`internal/bootretry`) because the first outbound dial of an injected pod can be reset.

Kafka consumers and writers are constructed without dialling, so a broker outage at boot does not block start: `/readyz` reports it and the loops retry.

## Boot and shutdown order

`cmd/api` boot: parse config (all missing variables named at once) -> run embedded migrations (retried) -> open the pool, ping (retried) -> build consumers -> listen -> start the relay -> start the consumers.

Shutdown on SIGTERM: `/readyz` flips to 503 -> wait `SHUTDOWN_DRAIN_DELAY` (5 s) -> drain HTTP (<= 10 s) -> stop the consumers (await <= 30 s) -> stop the relay (await <= 30 s, Kafka writer close <= 5 s) -> close the Postgres pool last. The process exits non-zero when the relay, the consumers or the listener stop on their own, so the orchestrator restarts it.

`terminationGracePeriodSeconds` defaults to 40. The values comment budgets 5 + 10 + 10 + 10 s, but the code's await bounds for consumers and relay are 30 s each; a pod that really needs the full bound would be killed first. In practice an idle consumer and relay return in well under a second.

## Migrations

- OLTP schema: `migrations/0001_dock_scheduling` (appointments, ASN claims, calendars, hours, closures, the zone/door/ASN/cutoff local copies, `processed_events`, `outbox`) and `0002_idempotency_keys`. They are embedded (`go:embed`) and applied by `cmd/api` **and** `cmd/mcp` at boot through golang-migrate, over `MIGRATIONS_DATABASE_URL` when set, else `DATABASE_URL`. golang-migrate holds a session-scoped advisory lock, so give it a direct (non-pooled) connection when PgBouncer sits in front. `cmd/relay` runs no migrations.
- Analytical schema: `analytics/migrations/0001_schedule_adherence` (`analytics_processed_events`, `appointment_facts`), applied by `cmd/projector` over `ANALYTICS_DATABASE_URL` at boot. `cmd/reports` only reads.
- There is no manual migration step; there is no down-migration procedure in the runtime (`*.down.sql` files exist for development).

## Kafka

One broker platform-wide (`KAFKA_BROKERS`). The service never creates topics; the broker auto-creates them on first write.

| Direction | Topic | Who | Consumer group (env) |
|---|---|---|---|
| Produced | `warehouse.dock-scheduling.events` | relay (integration events) | none |
| Produced | `warehouse.dock-scheduling.analytics` | relay (every fact, the analytics twin) | none |
| Produced | `warehouse.dock-scheduling.events.dlq` | relay, for rows that can never be encoded | none |
| Consumed | `warehouse.yard-management.events` | `cmd/api` yard consumer | `YARD_CONSUMER_GROUP` |
| Consumed | `warehouse.facility.events` | `cmd/api` facility consumer | `FACILITY_CONSUMER_GROUP` |
| Consumed | `warehouse.inbound-receiving.events` | `cmd/api` ASN consumer, and the optional legacy importer | `ASN_CONSUMER_GROUP`, `DOCK_LEGACY_IMPORT_CONSUMER_GROUP` |
| Consumed | `warehouse.process-path-management.events` | `cmd/api` cutoff consumer | `CPT_CONSUMER_GROUP` |
| Consumed | `warehouse.dock-scheduling.analytics` | `cmd/projector` | `ANALYTICS_CONSUMER_GROUP` (chart default `dock-scheduling-analytics`) |
| Dead letters of consumed topics | `<consumed topic>.dlq` (headers `x-dlq-source-topic`, `x-dlq-cause`) | each consumer | none |

Group ids are chart values and must be stable and service-prefixed, never derived from a pod name: a stable id lets a rescheduled pod resume from its committed offsets, while a brand-new group starts at the first offset and replays the topic (the local copies converge; the effect is idempotent under the CloudEvents id). Offsets are committed only after the handler returned, so delivery is at-least-once with an atomic effect.

## Outbox relay

Every aggregate change writes its integration and analytics facts into the `outbox` table in the same transaction. The relay claims rows with `FOR UPDATE SKIP LOCKED` (several replicas or relays can run together), encodes each fact to CloudEvents 1.0, writes it (acks=all) and deletes the row only after the write succeeded: delivery is at-least-once and the CloudEvents id is persisted at enqueue time, so a retry republishes the same id.

- Interval `OUTBOX_RELAY_INTERVAL` (1 s); a full batch (`OUTBOX_BATCH_SIZE`, 100) is followed immediately by another pass.
- A failed pass logs `outbox relay pass failed` and retries after the interval; rows stay in the table.
- A row that fails `OUTBOX_MAX_ATTEMPTS` (20) times is excluded from claims, logged once at ERROR (`outbox relay: row reached the attempt cap ...`) with `outbox_id`, `event_type`, `key`, `attempts`, and later rows overtake it (the per-key ordering trade-off of [ADR 0005](/docs/adr/0005-postgres-persistence-and-outbox)). Re-arm it after fixing the cause: `UPDATE outbox SET attempts = 0, next_attempt_at = now() WHERE id = <id>;`.
- A row that can never be encoded (a payload that is not the publisher's envelope) is parked raw on `warehouse.dock-scheduling.events.dlq` as event type `outbox-poison`, never dropped on a broker hiccup.
- `cmd/relay` is the same loop as its own Deployment (`relay.enabled=true`), for scaling the drain apart from the API. It is safe beside the inline relay. `cmd/mcp` starts no relay: the events of an MCP booking leave through the API's (or `cmd/relay`'s) relay, so keep one of them running whenever MCP is enabled.

## Consumer failure behaviour and dead letters

- A message that is not a valid CloudEvent: warn, commit past.
- A type the consumer has no handler for: ignored.
- A handled type that can never be applied (undecodable payload, blank id, value the domain refuses): republished unchanged to `<topic>.dlq` with `x-dlq-source-topic` and `x-dlq-cause`, then committed past. Inspect and replay from the DLQ topic by hand.
- A transient failure (Postgres down, lost commit, concurrent update): retried on the same message with capped exponential backoff, forever. A partition does not advance until it succeeds, which is intended: dropping the fact would lose it.
- A fact the domain deliberately ignores (unknown or terminal appointment, door not yet known) is marked processed and counted in `dock_scheduling_ignored_facts_total{reason}`.

## No-show sweep

Nothing in the service calls `POST /no-show-sweeps` on a timer. The chart's CronJob (`sweeps.enabled`, default on, `*/5 * * * *`, `startingDeadlineSeconds 60`, `activeDeadlineSeconds 120`) runs `/app/sweep` against the API Service with a fresh `Idempotency-Key` per pass. It retries transport errors, timeouts, 429 and 5xx up to `maxAttempts` (4) with doubling backoff, fails fast (non-zero exit) otherwise, and always asks the Istio sidecar to quit. The chart refuses to render when the worst-case retry budget does not fit `activeDeadlineSeconds`. Set `sweeps.enabled=false` only when an external caller drives the endpoint. To sweep by hand: `curl -X POST -H 'Idempotency-Key: <uuid>' http://<api>/no-show-sweeps`.

## Scaling and exposure

- `autoscaling.api.enabled` adds an HPA (1 to 4 replicas at 70 % CPU, off by default). More than one api replica is safe by design (version-guarded writes, `SKIP LOCKED` relay, consumers dedupe on the event id) but has not been load-tested.
- `cmd/mcp` is deliberately not HPA-scalable: the Streamable HTTP handler keeps per-process session state and the Service has no session affinity.
- Kong routes, when `gatewayApi.enabled` and `mcp.route.enabled` / `analytics.reports.route.enabled` are on: API under the context path, MCP at `/api/mcp/dock-scheduling` rewritten to `/mcp`, reports at `/api/dock-scheduling/reports` rewritten to `/reports`. There is no authentication anywhere; the boundary is the cluster and the gateway.
- Pod hardening: non-root (uid 1000), read-only root filesystem, no privilege escalation, all capabilities dropped. The service writes nothing to disk.

## Routine procedures

| Task | How |
|---|---|
| Roll out a new image | Change `image.tag`; the `checksum/config` annotation rolls the api pods when the ConfigMap changes. Readiness holds traffic until Postgres and Kafka answer. |
| Change a tuning knob | Set the matching `config.*` value (see [Configuration](/docs/operations/configuration)) and roll. |
| Turn the legacy importer on | Set `config.dockLegacyImportConsumerGroup` only after the facility consumer has caught up on doors (an import for an unknown door is skipped and counted as `unknown_door`); unset it at strangler stage E. |
| Check the outbox backlog | `SELECT count(*), min(created_at) FROM outbox;` and `SELECT id, aggreg_type, attempts FROM outbox WHERE attempts >= 20;` |
| Re-arm an excluded outbox row | `UPDATE outbox SET attempts = 0, next_attempt_at = now() WHERE id = <id>;` |
| Check analytics freshness | `GET /reports/freshness` on `cmd/reports`: `asOf` and `lagSeconds`. |
| Rebuild the analytical model | Use a new `ANALYTICS_CONSUMER_GROUP` (reads the topic from the start) against an empty analytical database; the projection dedupes on the event id. |
| Clear idempotency keys | `idempotency_keys` has a `created_at` index for retention; the service has no purge job, so purge by age in SQL if the table grows. |
