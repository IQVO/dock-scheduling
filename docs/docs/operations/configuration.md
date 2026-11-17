---
id: configuration
title: Configuration
sidebar_label: Configuration
description: "Every environment variable each dock-scheduling binary reads, with default, required flag and meaning."
---

# Configuration

Six binaries ship in one image (`/app/api`, `/app/mcp`, `/app/relay`, `/app/projector`, `/app/reports`, `/app/sweep`). Each reads its own environment; there is no config file and no in-memory or log-sink mode. A missing required variable is a boot error that names the variable, and several missing variables are reported together.

Source: `cmd/api/config.go`, `cmd/mcp/config.go`, `cmd/relay/config.go`, `cmd/projector/main.go` (`loadConfig`), `cmd/reports/main.go` (`loadConfig`), `cmd/sweep/sweep.go` (`loadConfig`), `internal/config/config.go`.

Duration variables (`OUTBOX_RELAY_INTERVAL`, `SHUTDOWN_DRAIN_DELAY`) accept a Go duration (`500ms`, `2s`) or plain seconds (`2`). A malformed or out-of-range value is not fatal: the binary logs a warning naming the variable and uses the default.

## cmd/api (REST API, outbox relay and Kafka consumers in one process)

| Variable | Default | Required | Meaning |
|---|---|---|---|
| `DATABASE_URL` | none | yes | Postgres DSN of the OLTP database (runtime pool). |
| `MIGRATIONS_DATABASE_URL` | value of `DATABASE_URL` | no | Direct (non-pooled) DSN used only for the boot migrations, because `golang-migrate` takes a session-scoped advisory lock that PgBouncer transaction pooling cannot honour. |
| `KAFKA_BROKERS` | none | yes | Comma-separated bootstrap servers for the relay, the consumers and the `/readyz` broker check. |
| `YARD_CONSUMER_GROUP` | none | yes | Consumer group of the yard-management consumer (`warehouse.yard-management.events`). |
| `FACILITY_CONSUMER_GROUP` | none | yes | Consumer group of the facility-layout consumer (`warehouse.facility.events`). |
| `ASN_CONSUMER_GROUP` | none | yes | Consumer group of the inbound-receiving ASN consumer (`warehouse.inbound-receiving.events`). |
| `CPT_CONSUMER_GROUP` | none | yes | Consumer group of the process-path-management cutoff consumer (`warehouse.process-path-management.events`). |
| `HTTP_ADDR` | `:8080` | no | Listen address of the REST API. |
| `OUTBOX_RELAY_INTERVAL` | `1s` | no | Poll interval of the in-process outbox relay. |
| `OUTBOX_BATCH_SIZE` | `100` | no | Outbox rows claimed per relay pass. |
| `OUTBOX_MAX_ATTEMPTS` | `20` | no | Failures before the relay excludes a row from claims. `0` means unlimited; a negative or unparsable value falls back to `20` with a warning. |
| `SHUTDOWN_DRAIN_DELAY` | `5s` | no | How long the process keeps serving after `/readyz` flips to 503. `0` is valid. |
| `LOG_LEVEL` | `info` | no | `debug`, `info`, `warn` (or `warning`), `error`; case-insensitive; anything else is `info`. |

## cmd/mcp (MCP server, Streamable HTTP)

`cmd/mcp` starts no relay and no consumer, yet it requires `KAFKA_BROKERS`: its writes enqueue outbox rows that a relay (inline in `cmd/api`, or `cmd/relay`) drains, and `/readyz` reports the broker.

| Variable | Default | Required | Meaning |
|---|---|---|---|
| `DATABASE_URL` | none | yes | Postgres DSN of the OLTP database. |
| `MIGRATIONS_DATABASE_URL` | value of `DATABASE_URL` | no | Direct DSN for the boot migrations (the MCP binary also runs them). |
| `KAFKA_BROKERS` | none | yes | Bootstrap servers, used by `/readyz` only. |
| `MCP_ADDR` | `:8090` | no | Listen address of the MCP server. |
| `SHUTDOWN_DRAIN_DELAY` | `5s` | no | Wait after `/readyz` flips to 503. |
| `LOG_LEVEL` | `info` | no | Same values as `cmd/api`. |

## cmd/relay (stand-alone outbox relay)

Optional: `cmd/api` already relays inline. It does not run migrations (the schema belongs to the API's boot) and does not read `LOG_LEVEL` (JSON logs to stderr at the default level).

| Variable | Default | Required | Meaning |
|---|---|---|---|
| `DATABASE_URL` | none | yes | Postgres DSN of the OLTP outbox. |
| `KAFKA_BROKERS` | none | yes | Bootstrap servers the relay writes to. |
| `OUTBOX_RELAY_INTERVAL` | `1s` | no | Poll interval. |
| `OUTBOX_BATCH_SIZE` | `100` | no | Rows per pass. |
| `OUTBOX_MAX_ATTEMPTS` | `20` | no | Failure cap per row; `0` = unlimited. |
| `ADMIN_ADDR` | `:8094` | no | Listen address of the `GET /healthz` probe server. |

## cmd/projector (analytics writer)

Consumes the service's own `warehouse.dock-scheduling.analytics` topic and is the only writer of the analytical database. It never opens the OLTP database.

| Variable | Default | Required | Meaning |
|---|---|---|---|
| `ANALYTICS_DATABASE_URL` | none | yes | DSN of the analytical Postgres, direct, with the projector's own read-write role (the binary runs the embedded analytical migrations over it). |
| `KAFKA_BROKERS` | none | yes | Bootstrap servers of the analytics consumer and `/readyz`. |
| `ANALYTICS_CONSUMER_GROUP` | none | yes | Fixed consumer group of the projector (the chart defaults it to `dock-scheduling-analytics`). A brand-new group starts at the earliest offset and rebuilds the model from history. |
| `ADMIN_ADDR` | `:8091` | no | Listen address of `/healthz` and `/readyz`. |
| `SHUTDOWN_DRAIN_DELAY` | `5s` | no | Wait after `/readyz` flips to 503. |
| `LOG_LEVEL` | `info` | no | Same values as `cmd/api`. |

## cmd/reports (analytics reader)

Read-only HTTP reader over the analytical database. It never opens the OLTP database or Kafka.

| Variable | Default | Required | Meaning |
|---|---|---|---|
| `ANALYTICS_DATABASE_URL` | none | yes | DSN of the analytical Postgres, ideally a read-only role (the chart feeds it from the secret key `ANALYTICS_READER_DATABASE_URL`). |
| `HTTP_ADDR` | `:8092` | no | Listen address of the reports API. |
| `SHUTDOWN_DRAIN_DELAY` | `5s` | no | Wait after `/readyz` flips to 503. |
| `LOG_LEVEL` | `info` | no | Same values as `cmd/api`. |

## cmd/sweep (no-show sweep CronJob entrypoint)

A one-shot process: it POSTs `SWEEP_URL` with a fresh `Idempotency-Key`, retries transient failures and exits. Exit codes: `0` ok, `1` gave up or rejected, `2` invalid configuration. It asks the Istio sidecar to quit on every exit path.

| Variable | Default | Required | Meaning |
|---|---|---|---|
| `SWEEP_URL` | none | yes | Endpoint to POST; the chart renders `http://<release>:<service.port>/no-show-sweeps`. |
| `SWEEP_REQUEST_TIMEOUT` | `15s` | no | Per-attempt timeout, positive Go duration. |
| `SWEEP_MAX_ATTEMPTS` | `4` | no | Attempts before giving up, integer >= 1. |
| `SWEEP_RETRY_BACKOFF` | `1s` | no | First backoff; doubles per attempt. |
| `SWEEP_MAX_BACKOFF` | `8s` | no | Backoff cap. |
| `SIDECAR_QUIT_URL` | `http://127.0.0.1:15020/quitquitquit` | no | Istio quit endpoint; set it empty to disable (outside a mesh). |

Retryable outcomes: transport error, timeout, HTTP 429 and 5xx. Any other non-2xx status stops the sweep at once. Defaults give a worst case of 4 x 15 s + 3 x 8 s = 84 s, inside the chart's `activeDeadlineSeconds: 120`.

## Variables the chart renders that no binary reads

The Helm chart renders `OTEL_EXPORTER_OTLP_ENDPOINT` (`otel.enabled`, default `true`) and carries an `environment` value, but no Go code in this repository reads them: the telemetry adapter is Prometheus only (see [Observability](/docs/operations/observability)). They are harmless and listed here so nobody hunts for them in the code.

## Where the values come from

Helm values map one to one onto the variables above (`config.*`, `database.*`, `kafka.brokers`, `mcp.*`, `relay.*`, `analytics.*`, `sweeps.*`). See the [runbook](/docs/operations/runbook).
