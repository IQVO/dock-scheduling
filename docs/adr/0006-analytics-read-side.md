# 6. Analytics read side: an analytics stream, a projector and a schedule-adherence report over a separate analytical database

Status: Accepted (2026-11-06). Additive: the OLTP API, the integration topic `warehouse.dock-scheduling.events` and its payloads are
unchanged. No OLTP migration (the shared CloudEvents id is legal under `UNIQUE (event_id, topic)`, ADR 0005). Adds a second, separate
database. Fills in the "this service's read side" stream that ADR 0004 reserved. Shape reference: inbound-receiving ADR 0006; the
reports contract is split into its own spec as facility-layout and process-path-management do (`apis/openapi-reports.yaml`).

## Context

Every OLTP context of the fleet ships an analytics read side: a separate analytical Postgres fed by a **projector** that consumes the
context's own analytics topic, and a read-only **reports** binary over that database. The question this data product answers is
**schedule adherence**: per site and carrier, how many appointments arrived on time, early or late, and how many were no-shows (the
yard model's D-13 report row, `GET /reports/schedule-adherence`).

## Decision

### 1. What is published and projected

Every published fact is written to `warehouse.dock-scheduling.analytics`; the analytics-only facts (check-in, door reassignment,
completion, at-risk, operating hours) go ONLY there (ADR 0004 table). The two outbox rows of an integration fact share ONE CloudEvents
id, `type` and Kafka key (ADR 0005); `dataschema` is `urn:warehouse:dock-scheduling:analytics:<EventName>:v1`. Payloads equal the
integration payloads, so nothing is added to the wire for the report.

The projector folds six events and acknowledges-and-ignores the rest (door reassignments, at-risk, calendar facts cannot change the
adherence of an appointment): `DockAppointmentBooked`, `Rescheduled`, `CheckedIn`, `Completed`, `Cancelled`, `NoShow`
(`adherence.Kinds()`).

### 2. The analytical model: facts, never aggregates

`analytics/migrations/0001_schedule_adherence` (embedded, `analytics/migrations.go`):

- `analytics_processed_events`: the dedupe set (CloudEvents `id` primary key) and the freshness source (`max(occurred_at)`).
- `appointment_facts`, one row per appointment: `site_id`, `carrier_scac`, the CURRENT window (`window_start`, `window_end`,
  `window_version`), and one column per milestone (`booked_at`, `checked_in_at`, `completed_at`, `cancelled_at`, `no_show_at`,
  `arrival_status`, `cancellation_reason`).
- `outcome` is a **generated stored column**, the single declaration of the classification: an arrival counts as its status
  (`EARLY`, `ON_TIME`, `LATE`), a completion with no check-in is an implicit arrival and counts `ON_TIME` (X6, ADR 0002 A5), a no-show
  counts `NO_SHOW`; an appointment only booked, or cancelled before anyone arrived, has no outcome and is not counted.
- `ref_at` (generated) is the instant the report range applies to: the current window start.

Order never matters: each milestone is filled with `LEAST`/`COALESCE`, a fact arriving before the event that creates the row creates
it, and the window moves only forward in aggregate version, so a replayed or reordered older `Booked`/`Rescheduled` cannot overwrite
a newer window.

### 3. The projector (`cmd/projector`)

- Consumes the analytics topic under a **fixed consumer group read from env** (`ANALYTICS_CONSUMER_GROUP`, REQUIRED, no default; the
  chart sets it). At-least-once; a brand-new group starts at the earliest offset, so the model is rebuilt by replaying the topic under a
  new group.
- **Dedupe and effect in one transaction**: `Projection.Apply` inserts the event id and folds the event in ONE analytical-database
  transaction; the offset is committed only after `Apply` returned.
- **Policy** (`internal/adapters/inbound/kafka/consumer.go`): not a CloudEvents 1.0 message: skipped with a WARN and committed past; a
  valid event of another type: ignored; a known type with an unusable payload (`adherence.ErrInvalidEvent`: missing id, time, site,
  window, version, an unknown arrival status) or one the store deterministically rejects (`ErrRejected`: Postgres SQLSTATE class 22 or
  23): **dead-lettered at once** to `warehouse.dock-scheduling.analytics.dlq` (the raw message plus the source-topic and cause headers),
  then committed past; a **transient** failure (database down, timeout) retries the same message with capped backoff and is never
  dead-lettered.
- Boot: `ANALYTICS_DATABASE_URL` is a DIRECT DSN (golang-migrate's advisory lock does not survive PgBouncer transaction pooling) and is
  never the OLTP `DATABASE_URL`; the embedded migrations and the first ping run under `internal/bootretry`; Kafka is dialled lazily. It
  never opens the OLTP database and writes only the analytical one.
- Serves only `/healthz` and `/readyz` on `ADMIN_ADDR` (default `:8091`); `/readyz` needs the analytical database AND a Kafka broker
  metadata answer. The process exits non-zero when the consumer returns on its own, so the orchestrator restarts it.

### 4. The reports binary (`cmd/reports`, `:8092`)

Read-only HTTP over the analytical database through a read-only pool (`default_transaction_read_only=on`, on top of a SELECT-only
role); RFC 7807 errors; no auth (fleet rule). `HTTP_ADDR` (default `:8092`), `SHUTDOWN_DRAIN_DELAY`, `LOG_LEVEL`,
`ANALYTICS_DATABASE_URL` (REQUIRED). It is **not** part of `cmd/api`: `GET /reports/*` on the OLTP router answers 404 `not-found`
(`TestTheOLTPRouterDoesNotServeTheReports`, and the real-wiring test of `cmd/api`).

| endpoint | question | shape |
| --- | --- | --- |
| `GET /reports/schedule-adherence` | per site and carrier over `[from, to)`: on-time, early, late, no-show | `from`, `to`, `items[]`: `siteId`, `carrierScac` (`-` when the appointment had none), `onTime`, `early`, `late`, `noShow`, `total`, `*Share` (rounded to 4 places) |
| `GET /reports/freshness` | how far the projection is behind | `asOf` (newest applied CloudEvents time), `lagSeconds` (never negative); both `null` before the first event |

Rules: `from` and `to` are mandatory RFC 3339, `from < to`, at most 92 days (400 `invalid-report-range`); optional `siteId` and
`carrierScac` narrow the rows; an unknown query parameter is a 400 `unknown-query-parameter`; a range with no data is a 200 with
`items: []`. SQL only counts; the shares are computed by the REST adapter (`internal/adapters/inbound/http/reports.go`).

### 5. The contract: `apis/openapi-reports.yaml`

The reports binary is a different server, port and database from `cmd/api`, so describing its routes in `apis/openapi.yaml` would
describe a server that does not exist. The route `GET /reports/schedule-adherence` and its schemas MOVED, unchanged in shape, to
`apis/openapi-reports.yaml` (server `http://localhost:8092`, tag `Reports`); `GET /reports/freshness` (served since the binary
split) is added to it, plus `/healthz`, `/readyz` and `/metrics`. Both specs are linted by Spectral with the same ruleset (`api-lint`),
the docs site renders each as its own API reference (`docs/docs/api-reference/rest` and `rest-reports`, `docs-api-drift`), and
`internal/adapters/inbound/http/reports_contract_test.go` fails when the reports router serves a route the spec lacks (or the
reverse) or a 200 body drifts from its schema.

### 6. Why a separate analytical database

The projection is a derived copy that can be dropped and rebuilt from the topic. Keeping it out of the OLTP database means a heavy
report cannot take OLTP connections, the schemas evolve independently, the reports role is read-only on a database that holds nothing
transactional, and the projector has no credentials on OLTP data. The cost is a second database/role to provision in
`warehouse-infra` (`analytics_services` entry).

### 7. Packaging

The one image builds every `cmd/*` (`/app/projector`, `/app/reports` among them). The chart gains `projector` (Deployment, admin port
`8091`) and `reports` (Deployment and Service `<release>-reports`, port `8092`) behind `analytics.enabled`, with a Secret or existing
secret holding two keys: `ANALYTICS_DATABASE_URL` (the writer DSN, projector) and `ANALYTICS_READER_DATABASE_URL` (the SELECT-only
DSN, optional); the reports Deployment receives the reader key AS its `ANALYTICS_DATABASE_URL` (the writer DSN when no reader key is set). Topic provisioning for the infra child: `kafka.Topics()` = `warehouse.dock-scheduling.events`,
`warehouse.dock-scheduling.analytics`, `warehouse.dock-scheduling.events.dlq`; the consumers' dead-letter topics (`<consumed
topic>.dlq`, including `warehouse.dock-scheduling.analytics.dlq`) are created by the broker on first write
(`AllowAutoTopicCreation`), but may be pre-created.

## Consequences

- Two outbox rows per integration event; tests that count outbox rows see two per event (ADR 0005).
- The analytics stream is not an integration contract: only the projector consumes it.
- Appointments that began before the analytics stream existed are only partly visible (no check-in without its event). Replaying from
  the earliest retained offset is the backfill.
- A new event that should change adherence must be added to `adherence.Kinds()` and the analytical schema, or it is silently
  ignored by the report.
- The adherence report now needs `cmd/reports` deployed: a deployment that runs only `cmd/api` serves no `/reports/*`.

## Alternatives considered and rejected

- **Serve the report from `cmd/api` over the OLTP database**: current state only, analytical queries on the OLTP database, and no
  history of check-in or no-show instants. (This was the first cut, replaced by this ADR.)
- **Project from the integration topic**: the check-in, completion and arrival-status facts are analytics-only and never appear there.
- **Pre-aggregated daily counters**: cannot re-bucket a range and a replayed or late event would need decrement logic. Facts are cheap
  at this volume.
- **Compute `outcome` in Go on read**: two definitions of the classification would drift; it is declared once, in the schema.
- **Document the reports in `apis/openapi.yaml`**: it would specify routes the described server does not serve.
