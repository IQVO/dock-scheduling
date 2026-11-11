---
id: runbook
title: Runbook
sidebar_label: Runbook
description: "Deploying and operating dock-scheduling: what runs on the k3s cluster, Helm chart, probes, migrations, Kafka topics and consumer groups, outbox relay, dead letters, the no-show sweep and routine procedures."
---

# Runbook

Status: **built on develop, registered in warehouse-infra develop and applied to the k3s cluster at `v0.1.2` (api only).** Read-only on 2026-11-10 (`kubectl get` and `kubectl logs`, context `k3s-multipass`): the Argo CD application `dock-scheduling` is Synced and Healthy and one `dock-scheduling` pod is Ready with 0 restarts. See [What runs on the k3s cluster](#what-runs-on-the-k3s-cluster) for the evidence and for what is **not** verified (consumer lag, topic contents), [Registration in warehouse-infra](#registration-in-warehouse-infra) for the infra wiring and the [pre-release checklist](/docs/operations/before-first-release). The workload sections below describe what the chart and the binaries do; on k3s only the api workload is switched on.

Source: `charts/dock-scheduling/`, `cmd/*`, `internal/adapters/**`.

## What runs on the k3s cluster

Read-only on 2026-11-10 (about 20:55 in UTC-03:00), context `k3s-multipass`, namespace `warehouse-systems`. Nothing was applied, exec'd or port-forwarded from this page.

| Fact | Value read |
|---|---|
| Argo CD application `dock-scheduling` (namespace `argocd`) | Sync `Synced`, health `Healthy`; last operation `Succeeded` at 2026-11-10T03:10:38Z. Chart from this repository, `targetRevision: develop`, synced at `86bd1ed`; values from `warehouse-infra` develop (`helm-values/_platform.yaml`, `helm-values/dock-scheduling.yaml`, `helm-values/kind-resources/dock-scheduling.yaml`). |
| Deployment and pod | Deployment `dock-scheduling` 1/1; pod `dock-scheduling-675b448888-qcx4b` Running, Ready, 0 restarts, age about 20 h. |
| Image running | `ghcr.io/iqvo/warehouse-infra/dock-scheduling:d5db895-arm64`. `d5db895` is the commit of this repository's tag `v0.1.2` (`gh release list -R IQVO/dock-scheduling`: `v0.1.2`, Latest, 2026-11-10; the tag ref resolves to the same commit). The pin is `terraform/targets/k3s.images.yaml` in `warehouse-infra` develop, set by commit `05fd28b` "chore(images): pin dock-scheduling and yard-management at v0.1.2" (#200). |
| Service and route | Service `dock-scheduling` ClusterIP, port 80 to 8080. HTTPRoute `dock-scheduling` for `/api/dock-scheduling` (prefix stripped) on the `warehouse-gateway`: conditions `Accepted`, `ResolvedRefs` and `Programmed` all `True`. |
| Secrets | `dock-scheduling-db` and `dock-scheduling-analytics-db` exist. |
| Consumer groups in the ConfigMap | `dock-scheduling-yard`, `dock-scheduling-facility`, `dock-scheduling-asn`, `dock-scheduling-cpt`; no `DOCK_LEGACY_IMPORT_CONSUMER_GROUP` (the importer is off). |

**Components.** Only the api runs: REST, the inline outbox relay and the four Kafka consumers (the boot log lines `kafka consumers running` with `yard consumer, facility consumer, asn consumer, cutoff consumer` and `outbox relay running` confirm it). The rest is switched off by `warehouse-infra` commit `89c9592` ("reduced footprint on k3s", 2026-11-09, because the agent had about three free pod slots): `mcp.enabled: false`, `relay.enabled: false` (the api relays inline), `analytics.enabled: false` (no projector, no reports), `sweeps.enabled: false`. So on the cluster there is no `dock-scheduling-mcp`, `-relay`, `-projector` or `-reports` pod, no `dock-scheduling` CronJob, and no Kong route for MCP or reports; the no-show sweep has to be called by hand (see [No-show sweep](#no-show-sweep)). The flags go back to `true` in one `warehouse-infra` PR when capacity is freed.

**Version skew.** `warehouse-infra` pins the image at `v0.1.2`, but Argo syncs the chart from this repository's `develop`. Develop is ahead of `v0.1.2`: commits `508dde2` to `86bd1ed` (ADR 0011) remove the legacy appointment importer, which the `v0.1.2` binary still contains, switched off by the empty group id. The pages of this site describe develop.

**Edges.** Both ends of every edge except `outbound-shipping` run in the cluster; see [Edge status](/docs/ecosystem/integration#edge-status).

**Not verified.** Consumer group lag, committed offsets and the list of topics on the broker (`kafka-topics.sh` and `kafka-consumer-groups.sh` need `kubectl exec`, which was not used). Whether the dashboard and the six alert rules are loaded in the observability stack. The page does not claim the service is healthy beyond pod Ready, restart count and the log lines below.

**What the log of the api pod showed.** `kubectl logs --tail=50`, read 2026-11-10 about 20:54 in UTC-03:00 (23:54Z). The pod has 0 restarts, yet the log holds broker trouble in the same few windows (09:19Z to 09:43Z and 23:21Z):

- `kafka reader error; retrying`, topic `warehouse.facility.events`: `the kafka reader got an unknown error reading partition 1 of warehouse.facility.events at offset 25563: read tcp 10.42.1.51:35460->10.42.0.232:9092: i/o timeout` (2026-11-10T09:19:11Z). The same error, with offset 0, appears for `warehouse.yard-management.events` and `warehouse.inbound-receiving.events`, and offsets 0 to 6 for `warehouse.process-path-management.events`, at 23:21:11Z to 23:21:20Z.
- `readiness: dependency is not ready`, dependency `kafka`: `kafka metadata: ask kafka.warehouse-systems.svc.cluster.local:9092: read tcp 10.42.1.51:49838->10.43.25.132:9092: i/o timeout` (09:43:33Z); dependency `postgres`: `context deadline exceeded` and dependency `kafka`: `lookup kafka.warehouse-systems.svc.cluster.local: i/o timeout` (23:21:13Z).
- `kubectl get events` shows the matching `Readiness probe failed ... /readyz` Warning events (503 and a client timeout) about 35 minutes before the read. The pod's Ready condition was `True` when read.

A retrying `kafka reader error` line is the consumer loop doing its job ([Consumer failure behaviour](#consumer-failure-behaviour-and-dead-letters)); the lines are reported as found, not diagnosed. Whether the groups caught up afterwards was not checked.

## Registration in warehouse-infra

Registered by `warehouse-infra` PR 193 (commit `b4b827e`, "register dock-scheduling and yard-management on k3s"); the image was pinned by commit `c42c475`. Read from `origin/develop`:

| Item | State in `warehouse-infra` develop |
|---|---|
| `terraform/apps.yaml` | Entry `dock-scheduling`: `kind: service`, `repo_owner: IQVO`, `database: true`, `analytics: true` (the default key pair `ANALYTICS_DATABASE_URL` and `ANALYTICS_READER_DATABASE_URL`), no web part. |
| Image | `terraform/targets/k3s.images.yaml` pins `dock-scheduling` at tag `d5db895-arm64`, the commit of this repository's `v0.1.2` tag (commit `05fd28b`, #200; it replaced the `v0.1.1` pin `7f20813-arm64` of commit `c42c475`). |
| Helm values | `helm-values/dock-scheduling.yaml` sets four stable consumer groups: `dock-scheduling-yard`, `dock-scheduling-facility`, `dock-scheduling-asn`, `dock-scheduling-cpt`; the projector group `dock-scheduling-analytics`; `existingSecret` wiring for `dock-scheduling-db` and `dock-scheduling-analytics-db`; and, since commit `89c9592` (2026-11-09, reduced footprint), `mcp.enabled: false`, `relay.enabled: false` (the inline relay of `cmd/api` drains the outbox), `analytics.enabled: false` and `sweeps.enabled: false` (the chart default is the `/no-show-sweeps` CronJob every 5 minutes; it does not run on k3s now); Kong plugins `cors`, `opentelemetry`, `prometheus`; no A2A block. The retired `config.dockLegacyImportConsumerGroup` key (the legacy importer, removed in ADR 0011) is no longer read by the chart; drop it from the values file. |
| Kong routes | The values declare `/api/dock-scheduling` (prefix stripped), `/api/mcp/dock-scheduling` (to `/mcp`) and `/api/dock-scheduling/reports` (to `/reports`). Only the first exists in the cluster (`kubectl get httproute -A`, 2026-11-10): the MCP and reports routes render with their components, which are off. |
| Observability | One dashboard and six alert rules: see [Observability](/docs/operations/observability#dashboards-and-alerts). |
| CI | `warehouse-infra` lints the chart and runs its selector check for this service. |
| Kafka topics | `docs/kafka-topics-yard-dock.md` in `warehouse-infra` says the yard-domain topics must be pre-created, idempotently, **before the first sync** (`scripts/tf.sh app dock-scheduling apply`): a kafka-go group consumer started before its topic exists is never handed the partition. Whether the topics were pre-created, and what they hold now, was not checked (a listing needs `kubectl exec`); the api log of 2026-11-10 names partitions 0 to 7 of `warehouse.yard-management.events`, `warehouse.inbound-receiving.events` and `warehouse.facility.events`, so the broker answered for them. Topics: `warehouse.dock-scheduling.events`, `warehouse.dock-scheduling.analytics`, `warehouse.yard-management.events`, `warehouse.yard-management.analytics`, `warehouse.facility.events`, `warehouse.inbound-receiving.events`, `warehouse.process-path-management.events`. |
| Cluster | Applied: Argo application Synced and Healthy, one api pod Ready (see [What runs on the k3s cluster](#what-runs-on-the-k3s-cluster)). Every integration edge except `outbound-shipping` has both ends running (see [Integration](/docs/ecosystem/integration#edge-status)). |

The ADR bodies in `docs/adr/` are not edited by this status update: any sentence in them about the platform state says what it said when it was written.

## Workloads

One image (`ghcr.io/iqvo/dock-scheduling`) carries six binaries; each workload selects one with `command: ["/app/<name>"]`. The image default entrypoint is `./api`.

| Workload (chart) | Binary | Port | Default | Probes |
|---|---|---|---|---|
| Deployment `component=api` | `/app/api`: REST, inline outbox relay, four Kafka consumers | 8080 (Service port 80) | always on | startup, liveness `GET /healthz`; readiness `GET /readyz` |
| Deployment `component=mcp` | `/app/mcp` | 8090 | `mcp.enabled=false` | `GET /healthz`, `GET /readyz` |
| Deployment `component=relay` | `/app/relay` | 8094 (no Service) | `relay.enabled=false` | `GET /healthz` only |
| Deployment `component=analytics-projector` | `/app/projector` | 8091 (no Service) | `analytics.enabled=false` | `/healthz`, `/readyz` |
| Deployment `component=analytics-reports` + Service `<release>-reports` | `/app/reports` | 8092 (Service port 80) | `analytics.enabled=false` | `/healthz`, `/readyz` |
| CronJob | `/app/sweep` | none | `sweeps.enabled=true`, `*/5 * * * *` | exit code |

On the k3s cluster (read 2026-11-10) only `component=api` runs; `mcp`, `relay`, `analytics` and `sweeps` are switched off by `warehouse-infra` ([What runs on the k3s cluster](#what-runs-on-the-k3s-cluster)).

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

Shutdown on SIGTERM: `/readyz` flips to 503 -> wait `SHUTDOWN_DRAIN_DELAY` (5 s) -> drain HTTP (up to 10 s) -> stop the consumers (await up to 30 s) -> stop the relay (await up to 30 s, Kafka writer close up to 5 s) -> close the Postgres pool last. The process exits non-zero when the relay, the consumers or the listener stop on their own, so the orchestrator restarts it.

`terminationGracePeriodSeconds` defaults to 40. The values comment budgets 5 + 10 + 10 + 10 s, but the code's await bounds for consumers and relay are 30 s each; a pod that really needs the full bound would be killed first. In practice an idle consumer and relay return in well under a second.

## Migrations

- OLTP schema: `migrations/0001_dock_scheduling` (appointments, ASN claims, calendars, hours, closures, the zone/door/ASN/cutoff local copies, `processed_events`, `outbox`) and `0002_idempotency_keys`. They are embedded (`go:embed`) and applied by `cmd/api` **and** `cmd/mcp` at boot through golang-migrate, over `MIGRATIONS_DATABASE_URL` when set, else `DATABASE_URL`. golang-migrate holds a session-scoped advisory lock, so give it a direct (non-pooled) connection when PgBouncer sits in front. `cmd/relay` runs no migrations.
- The first OLTP migration runs `CREATE EXTENSION IF NOT EXISTS btree_gist` (the door exclusion constraint needs it), so the migration role must be allowed to create extensions, or the extension must exist already.
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
| Consumed | `warehouse.inbound-receiving.events` | `cmd/api` ASN consumer | `ASN_CONSUMER_GROUP` |
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

Nothing in the service calls `POST /no-show-sweeps` on a timer. The chart's CronJob (`sweeps.enabled`, default on, `*/5 * * * *`, `startingDeadlineSeconds 60`, `activeDeadlineSeconds 120`) runs `/app/sweep` against the API Service with a fresh `Idempotency-Key` per pass. It retries transport errors, timeouts, 429 and 5xx up to `maxAttempts` (4) with doubling backoff, fails fast (non-zero exit) otherwise, and always asks the Istio sidecar to quit. The chart refuses to render when the worst-case retry budget does not fit `activeDeadlineSeconds`. Set `sweeps.enabled=false` only when an external caller drives the endpoint. On the k3s cluster the CronJob is switched off (`warehouse-infra` commit `89c9592`; no `dock-scheduling` CronJob exists, read 2026-11-10), so no timer drives the endpoint: a `BOOKED` appointment becomes `NO_SHOW` only when someone calls it. To sweep by hand: `curl -X POST -H 'Idempotency-Key: <uuid>' http://<api>/no-show-sweeps`.

## Scaling and exposure

- `autoscaling.api.enabled` adds an HPA (1 to 4 replicas at 70 % CPU, off by default). More than one api replica is safe by design (version-guarded writes, `SKIP LOCKED` relay, consumers dedupe on the event id) but has not been load-tested.
- `cmd/mcp` is deliberately not HPA-scalable: the Streamable HTTP handler keeps per-process session state and the Service has no session affinity.
- Kong routes, when `gatewayApi.enabled` and `mcp.route.enabled` / `analytics.reports.route.enabled` are on: API under the context path, MCP at `/api/mcp/dock-scheduling` rewritten to `/mcp`, reports at `/api/dock-scheduling/reports` rewritten to `/reports`. There is no authentication anywhere; the boundary is the cluster and the gateway. On k3s only the API route is created (MCP and reports are off).
- Pod hardening: non-root (uid 1000), read-only root filesystem, no privilege escalation, all capabilities dropped. The service writes nothing to disk.

## Routine procedures

| Task | How |
|---|---|
| Roll out a new image | Change `image.tag`; the `checksum/config` annotation rolls the api pods when the ConfigMap changes. Readiness holds traffic until Postgres and Kafka answer. |
| Change a tuning knob | Set the matching `config.*` value (see [Configuration](/docs/operations/configuration)) and roll. |
| Check the outbox backlog | `SELECT count(*), min(created_at) FROM outbox;` and `SELECT id, aggreg_type, attempts FROM outbox WHERE attempts >= 20;` |
| Re-arm an excluded outbox row | `UPDATE outbox SET attempts = 0, next_attempt_at = now() WHERE id = <id>;` |
| Check analytics freshness | `GET /reports/freshness` on `cmd/reports`: `asOf` and `lagSeconds`. |
| Rebuild the analytical model | Use a new `ANALYTICS_CONSUMER_GROUP` (reads the topic from the start) against an empty analytical database; the projection dedupes on the event id. |
| Clear idempotency keys | `idempotency_keys` has a `created_at` index for retention; the service has no purge job, so purge by age in SQL if the table grows. |
