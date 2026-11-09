---
id: observability
title: Observability
sidebar_label: Observability
description: "What dock-scheduling exposes for operators: Prometheus metrics, MCP tool spans, structured logs, probes, and the dashboards and alerts that exist (none)."
---

# Observability

Status: **built on develop, not deployed.** No dashboard or alert rule for `dock-scheduling` exists in `warehouse-infra` (`terraform/dashboards`, `terraform/alerting` on `origin/develop` contain nothing for it), so everything below is what the binaries emit, not what a deployed stack already shows.

Source: `internal/adapters/outbound/telemetry/telemetry.go`, `internal/adapters/inbound/http/*`, `internal/adapters/inbound/mcp/tools.go`, `cmd/*`.

## Metrics: Prometheus, not OpenTelemetry

The telemetry adapter owns a private Prometheus registry and serves it at `GET /metrics` on `cmd/api` (`:8080`), `cmd/mcp` (`:8090`) and `cmd/reports` (`:8092`). `cmd/projector` and `cmd/relay` expose probes only, no `/metrics`.

There is **no OpenTelemetry metrics or trace SDK** in the code (`go.mod` has the OTel API only). The chart renders `OTEL_EXPORTER_OTLP_ENDPOINT` into the pods when `otel.enabled` (default `true`), but nothing in the binaries reads it: do not expect OTLP output from this service.

| Instrument | Type | Unit | Attributes | Meaning |
|---|---|---|---|---|
| `dock_scheduling_http_requests_total` | counter | requests | `method`, `route`, `status` | Finished HTTP requests. `route` is the chi route pattern (for example `/appointments/{appointmentId}`) and `unmatched` for no match, never the raw path, so cardinality stays bounded. |
| `dock_scheduling_http_request_duration_seconds` | histogram (default Prometheus buckets) | seconds | `method`, `route` | Request duration. |
| `dock_scheduling_ignored_facts_total` | counter | facts | `reason` | Yard, ASN or legacy facts the policies deliberately changed nothing for. `reason` is one of `unknown_appointment`, `terminal_appointment`, `site_mismatch`, `unknown_door`, `already_applied`, `not_legacy_import`. |
| `dock_scheduling_implicit_arrivals_total` | counter | arrivals | none | Door releases applied to a `BOOKED` appointment (arrival implied, no `TrailerDocked` or `VisitCheckedIn` seen first). |
| `go_*`, `process_*` | Go runtime and process collectors | | | Standard client_golang collectors. |

Reading the ignored-facts counter: a steady trickle of `already_applied` is normal (redelivery). A growing `unknown_door` means the facility consumer has not delivered the door yet (or the importer was enabled too early). A growing `unknown_appointment` means yard facts cite appointments this service does not hold.

Useful queries (not shipped as rules): `sum by (route, status) (rate(dock_scheduling_http_requests_total[5m]))`; `histogram_quantile(0.95, sum by (le, route) (rate(dock_scheduling_http_request_duration_seconds_bucket[5m])))`; `increase(dock_scheduling_ignored_facts_total[1h])` by `reason`.

There is no metric for outbox depth, relay lag or consumer lag: read them from the database (`SELECT count(*), min(created_at) FROM outbox`) and from the broker's consumer-group tooling. The projector's lag is `GET /reports/freshness`.

## Spans

`cmd/mcp` wraps every tool call in a span `mcp.tool <name>` through the global OTel tracer (scope `github.com/claudioed/dock-scheduling/internal/adapters/inbound/mcp`), with attributes `mcp.tool.name`, `warehouse.authority` (the tool's level, `L1` for reads, `L3` for writes) and, on writes, `idempotency.replayed`; errors are recorded and the span status set to error. No tracer provider is installed by any binary, so the global tracer is a no-op: the spans exist in code and become visible only if a provider is registered. REST handlers create no spans.

## Logs

JSON via `log/slog` to stdout (`cmd/relay` and `cmd/sweep` write to stderr). `LOG_LEVEL` is `debug`, `info` (default), `warn` or `error`. There are no request logs: a request is visible in the metrics, and only failures log. Fields and messages an operator meets:

| Message | Level | Fields | Where |
|---|---|---|---|
| `request failed` | error | `error`, `method`, `path` | An unmapped error became a 500; the response detail is generic and the real error is only here. |
| `handler panicked` | error | `panic`, `method`, `path` | A recovered panic, answered as a 500 problem. |
| `readiness: dependency is not ready` | warn | `dependency`, `error` | `/readyz` failing (`postgres`, `kafka`, `analytics-postgres`). |
| `fact consumed` | info | `type`, `event_id`, `subject` | One line per applied Kafka fact. |
| `skipping a message that is not a valid CloudEvent` | warn | `consumer`, `topic`, `error` | Non-CloudEvents message committed past. |
| `an event that can never be applied; dead-lettering it` | warn | `consumer`, `topic`, ... | Poison message sent to `<topic>.dlq`. |
| `<consumer> message can never be applied; dead-lettering it without retry` | error | `error`, `partition`, `offset` | Same, from the loop. |
| `<consumer> handling failed; retrying the same message` | error | `error`, `partition`, `offset`, `attempt`, `retry_in` | Transient failure, the partition is blocked until it passes. |
| `kafka reader error; retrying` | error | `topic`, `detail` | Broker or topic trouble inside kafka-go. |
| `outbox relay pass failed` | error | `error` | A relay pass failed, rows stay in `outbox`. |
| `outbox relay: row reached the attempt cap and is excluded from future claims; re-arm it ...` | error | `outbox_id`, `event_type`, `key`, `attempts`, `max_attempts`, `error` | A poison row; see the [runbook](/docs/operations/runbook). |
| `outbox relay running` / `kafka consumers running` | info | `interval`, `batch_size`, `max_attempts`, `events_topic`, `analytics_topic`, `dlq_topic` / `consumers` | Boot confirmation. |
| `retrying` / `succeeded after retry` | warn / info | `op`, `attempt`, `in`, `err` | Boot-time migration or ping retries. |
| `mcp write` | info | `tool`, `appointment_id`, `state`, `via`, `agent_id`, `proposal_id`, `approved_by`, `replayed` | Every MCP write, with its attribution. |
| `sweep: attempt failed` / `sweep: done` / `sweep: failed; exiting non-zero ...` | warn / info / error | `url`, `attempt`, `max_attempts`, `error` | The `cmd/sweep` CronJob. |
| `shutdown: readiness flipped to not-ready; waiting for traffic to drain` | info | `drain_delay` | Graceful shutdown started. |

## Probes and what they say

See the [runbook](/docs/operations/runbook#probes-what-readiness-waits-for). In short: `/healthz` is liveness, `/readyz` names the failing dependency, and `cmd/api` answers a `dependency-unavailable` problem while Postgres or Kafka is unreachable.

## Dashboards and alerts

None exist for this service in `warehouse-infra`. Kong-level request metrics (`kong_http_requests_total`) would cover it only once the chart's HTTPRoutes are enabled and the service is registered in `terraform/apps.yaml`.
