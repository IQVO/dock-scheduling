---
id: troubleshooting
title: Troubleshooting
sidebar_label: Troubleshooting
description: "Symptom, cause, check and fix for the real failure modes of dock-scheduling: boot errors, readiness, bookings refused, events not leaving, facts not arriving, sweeps and analytics."
---

# Troubleshooting

Source: `cmd/*/config.go`, `internal/adapters/inbound/http/errors.go`, `internal/adapters/outbound/outbox/relay.go`, `internal/adapters/inbound/kafka/*`, `charts/dock-scheduling/`. Log messages are listed in [Observability](/docs/operations/observability); variables in [Configuration](/docs/operations/configuration).

## Boot and readiness

| Symptom | Cause | Check | Fix |
|---|---|---|---|
| Pod exits at once, log `... is required` | A mandatory variable is missing. The boot error names every missing one: `DATABASE_URL`, `KAFKA_BROKERS`, and for `cmd/api` the four `*_CONSUMER_GROUP`; `ANALYTICS_DATABASE_URL` for the projector and reports. | `kubectl logs` of the first start: `dock-scheduling api exited with error`. | Set the value (chart: `database.*`, `kafka.brokers`, `config.*ConsumerGroup`). The chart refuses to render without them, so this usually means raw manifests or `extraEnv`. |
| `cmd/mcp` exits for `KAFKA_BROKERS` although it uses no Kafka | By design: its writes enqueue outbox rows a relay must drain, and `/readyz` reports the broker. | The error text says so. | Set `kafka.brokers` for the MCP Deployment. |
| Boot loops on `retrying` ... `run migrations` then exits | Postgres unreachable, wrong DSN, or the migrations connection goes through a pooler that cannot hold the advisory lock. | `retrying` log lines show `op` and `err`; the final error names `DATABASE_URL`. | Fix reachability or set `MIGRATIONS_DATABASE_URL` to a direct session-mode connection (chart: `database.migrationsExistingSecretKey`). |
| Pod is `Running` but never Ready, `/readyz` 503 `dependency-unavailable` | Postgres or Kafka does not answer within 2 s (verdict cached 3 s). | The problem detail reads `not ready: postgres, kafka`; log `readiness: dependency is not ready`. | Restore the dependency; nothing needs a restart. |
| `/readyz` 503 `{"status":"not_ready"}` with no failing list | The pod is draining after SIGTERM. | A `shutdown: readiness flipped` log line. | Expected during a rollout. |
| Pod killed during shutdown | `terminationGracePeriodSeconds` (40) is shorter than the worst-case await bounds (consumers 30 s, relay 30 s). | Exit status 137 and a missing "stopped" sequence. | Raise `terminationGracePeriodSeconds`; the chart test refuses values below 40. |
| `cmd/api` exits non-zero some time after boot | The relay or the consumers returned on their own (a reader failure), by design so the orchestrator restarts it. | Log `the outbox relay stopped unexpectedly` or `the kafka consumers stopped unexpectedly`. | Read the wrapped error; fix Kafka or Postgres. |
| Helm render fails | `database.url`/`existingSecret`, `kafka.brokers` or a consumer group is empty, `kafka.enabled=false`, or the sweep retry budget exceeds `activeDeadlineSeconds`. | The `fail` message names the value. | Set the value. |

## Requests refused

Errors are `application/problem+json`; the `type` is `https://errors.dock-scheduling.warehouse-systems.dev/<slug>`.

| Symptom | Cause | Check | Fix |
|---|---|---|---|
| 400 `idempotency-key-required` on a POST | Every POST needs `Idempotency-Key` (1-128 printable ASCII). | The request headers. | Send a fresh UUID per logical request; reuse it only to retry. |
| 409 `idempotency-key-reused` | The same key was sent with a different method, path or body. | Compare the two requests. | Use a new key. |
| 409 `door-double-booked` | Another `BOOKED` or `CHECKED_IN` appointment overlaps the window on that door (half-open `[start, end)`). | `GET /doors/{doorCode}/availability` for the window. | Pick a free window or another door. |
| 409 `door-unavailable` | The window is outside the door's operating hours or inside a closure. | `GET /sites/{siteId}/dock-calendar` (hours, closures). | Move the window, lift the closure, or declare hours. |
| 422 `unknown-door` | The door is not a known, active dock slot in the local copy. | The facility consumer: log `fact consumed` for `SlotRegistered`; `unknown_door` in `dock_scheduling_ignored_facts_total`. | Wait for or fix the facility feed; a decommissioned door is unknown. |
| 422 `door-flow-mismatch` | Direction does not match the door's dock flow. | The door's `dockFlow`. | Use an inbound or outbound door accordingly. |
| 422 `unknown-asn` / 409 `asn-already-scheduled` | The ASN is not in the local copy (or cancelled), or already on an active appointment. | The ASN consumer on `warehouse.inbound-receiving.events`. | Wait for the ASN fact; cancel the other appointment first. |
| 422 `missing-outbound-ref` | An OUTBOUND booking names neither a known `cptId` nor `shipmentRefs`. | The cutoff copy (`cutoff_schedules`, `cutoffs`) fed from `warehouse.process-path-management.events`. | Pass `shipmentRefs` or wait for the schedule. |
| 422 `window-in-past`, `invalid-window` | Start before now, end not after start, off the site's slot grid, or longer than `max_window_minutes` (default 240). | The calendar settings. | Correct the window. |
| 409 `concurrent-update` / 412 `precondition-failed` | Another writer changed the appointment (`version`), or `If-Match` carries a stale ETag. | `GET /appointments/{id}`: `ETag` `W/"<version>"`. | Re-fetch and retry. |
| 409 `illegal-transition` | The command is not legal in the current state (for example reschedule of a `CHECKED_IN` appointment). | The appointment's `state`. | See the [use cases](/docs/ddd/use-cases). |
| 409 `no-show-too-early` | The no-show grace has not passed. | `no_show_grace_minutes` in the settings (default 60). | Wait. |
| 409 `hours-conflict-with-appointments` / `closure-conflicts-with-appointments` | New hours or a closure would strand active appointments; the detail lists their ids. | The listed appointments. | Reschedule or cancel them first. |
| 400 `unknown-query-parameter` | An unsupported query parameter (rejected, never ignored). | The OpenAPI spec. | Remove it. |
| 500 `internal-error` | An unmapped error; the response is generic on purpose. | Log `request failed` with `error`, `method`, `path`. | Fix the cause named in the log. |

## Events not leaving (producer side)

| Symptom | Cause | Check | Fix |
|---|---|---|---|
| Bookings succeed but downstream sees nothing | The relay is not draining: broker down, no relay running (`cmd/mcp` alone starts none), or rows past the attempt cap. | `SELECT count(*), min(created_at) FROM outbox;` growing; `outbox relay pass failed` logs. | Restore Kafka; run the api or enable `relay.enabled`. |
| One row stuck, others flow | The row reached `OUTBOX_MAX_ATTEMPTS` (log `row reached the attempt cap`). | `SELECT id, event_id, aggreg_type, attempts FROM outbox WHERE attempts >= 20;` | Fix the cause, then `UPDATE outbox SET attempts = 0, next_attempt_at = now() WHERE id = <id>;`. |
| Poison rows on `warehouse.dock-scheduling.events.dlq` | A payload that is not the publisher's envelope (event type `outbox-poison`). | Read the DLQ message. | Fix the producer bug; replay by hand. |
| Consumers see the same event twice | At-least-once delivery. | Same CloudEvents `id`. | Dedupe on `id` (this service does). |

## Facts not arriving (consumer side)

| Symptom | Cause | Check | Fix |
|---|---|---|---|
| No door/ASN/cutoff data after first deploy | A new consumer group starts at the first offset and replays; the topic may be empty, the broker unreachable, or the group id wrong. | `kafka reader error; retrying`, `fact consumed` lines; group ids. | Check `KAFKA_BROKERS` and the group variables. |
| Partition stuck, repeated `handling failed; retrying the same message` | A transient failure (Postgres, lost commit) retried forever by design. | `error`, `partition`, `offset`, `attempt`. | Fix the dependency; it resumes. |
| Messages appearing on `<topic>.dlq` | Handled type that can never apply. | Headers `x-dlq-cause`, `x-dlq-source-topic`. | Fix the producer; replay. |
| Arrival or release not applied | The fact cites an unknown or terminal appointment, or the wrong site. | `dock_scheduling_ignored_facts_total{reason}` (`unknown_appointment`, `terminal_appointment`, `site_mismatch`). | Expected for stale facts; otherwise check the yard feed. |

## No-show sweep

| Symptom | Cause | Check | Fix |
|---|---|---|---|
| Appointments never become `NO_SHOW` | `sweeps.enabled=false`, the CronJob fails, or the appointment is still inside `window_end + no_show_grace`. | `kubectl get jobs`; the job's log `sweep: ...`. | Enable the CronJob; wait out the grace. |
| Job fails with exit 1 | The API answered 4xx (other than 429; not retried) or kept failing after `SWEEP_MAX_ATTEMPTS`. | `sweep: failed; exiting non-zero so the Job fails fast` with `url`. | Check `SWEEP_URL` (`/no-show-sweeps` on the api Service) and the API's health. |
| Job exits 2 | Invalid configuration (`SWEEP_URL` empty, bad duration or attempts). | `sweep: invalid configuration`. | Fix the variable. |
| Job pod never completes | The Istio sidecar kept running. | `SIDECAR_QUIT_URL`. | Leave the default in a mesh; set empty outside one. |

## Analytics

| Symptom | Cause | Check | Fix |
|---|---|---|---|
| `GET /reports/freshness` shows a large `lagSeconds`, or `asOf` null | The projector is down, behind, or has not seen an event. | `analytics-projector` readiness and logs; `warehouse.dock-scheduling.analytics` consumer group lag. | Restore the projector; a new group replays from the start. |
| Reports return empty | `analytics.enabled=false`, or the reports pool points at an empty database. | The chart value; `ANALYTICS_DATABASE_URL`. | Enable analytics; wait for the projection. |
| Projector exits at boot | Analytical database unreachable or the DSN is not direct (it runs migrations). | `run analytics migrations` retry logs. | Use a direct DSN with the projector's read-write role. |

## MCP

| Symptom | Cause | Check | Fix |
|---|---|---|---|
| An MCP booking is accepted but events never publish | No relay is running (the MCP binary starts none). | The outbox backlog query above. | Run the api or `cmd/relay`. |
| Replayed write returns the first answer | The same `idempotencyKey` and arguments. | `replayed: true` in the answer and log `mcp write`. | Use a new key for a new action. |
