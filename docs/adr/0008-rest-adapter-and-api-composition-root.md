# 8. REST adapter and the API composition root

Status: Accepted. Implements the inbound side of ADR 0002 (aggregates, invariants, error names), `apis/openapi.yaml` and the
runtime rules of ADR 0005. Code: `internal/adapters/inbound/http`, `cmd/api`, `internal/adapters/outbound/{system,telemetry}`,
`internal/config`, `internal/bootretry`. (ADR 0006 is reserved for the analytics read side, ADR 0007 and up for MCP.)

## Decisions

- **chi, one handler per use case.** Every route of the contract is a method of `usecases.Service` behind a handler that decodes, calls
  ONE use case and maps the view to a DTO; handlers never see a domain aggregate. `.claude/rules/rest-api.md` lists the route -> use case
  table. The adapter imports the application layer and the domain errors only; the architecture test keeps it off the outbound adapters.
- **Problem documents (D-10).** Every non-2xx is `application/problem+json` with `type =
  https://errors.dock-scheduling.warehouse-systems.dev/<slug>`. Domain errors: 409 for conflicts and illegal states
  (`ErrDoorDoubleBooked`, `ErrDoorUnavailable`, `ErrAsnAlreadyScheduled`, `ErrClosureConflictsWithAppointments`,
  `ErrHoursConflictWithAppointments`, `ErrOverlappingClosure`, `ErrClosureAlreadyLifted`, `ErrIllegalTransition`, `ErrConcurrentUpdate`),
  422 for value-object and reference rules (everything else), slug = kebab-case of the `Err` name without the prefix. A table test
  (`errors_internal_test.go`) derives the slug from the name. The adapter's own refusals are 400 (`malformed-request-body`,
  `invalid-query-parameter`, `unknown-query-parameter`, `invalid-limit`, `invalid-cursor`, `invalid-path-parameter`, `invalid-if-match`,
  `invalid-availability-query`, `invalid-report-range`, `idempotency-key-required`, `idempotency-key-invalid`), 404
  (`appointment-not-found`, `door-not-found`, `unknown-closure`, `not-found`), 405, 412 (`precondition-failed`) and 503
  (`dependency-unavailable`). An unmapped error is a 500 `internal-error`; the cause is logged, never echoed.
- **ETag / If-Match.** `ETag` is the weak tag `W/"<version>"` of the appointment on every answer that carries one. `If-Match` is honoured
  on reschedule and cancel (412 on a different current version, 400 on a value that is not a tag, absent or `*` means no precondition).
- **Idempotency-Key** (`idempotency.go`). Required on every POST. Every answer below 500 is stored, 4xx included, in the existing
  `idempotency_keys` table through `ports.IdempotencyStore`; a replay is byte-identical and carries `Idempotency-Replayed: true`; the
  same key with another method, path or body is 409 `idempotency-key-reused`; a 5xx or a panic is never stored. Two modes:
  - JOINED (book, reschedule, cancel, ETA, closure, lift): lookup, handler and store of a 2xx answer run in ONE unit of work, so the
    aggregate, its outbox rows and the key commit or roll back together (the use cases' own `Do` joins). A 4xx or 5xx answer discards the
    unit (a failed statement may have aborted the transaction); a 4xx is then stored on its own. Racing requests with one key all try to
    store, the primary key keeps the first writer, the loser re-reads, rolls its work back and replays the winner's answer.
  - DETACHED (the sweep): the sweep is naturally idempotent and commits one transaction per appointment by design, so the handler runs
    outside any unit of work and the answer is stored after it.
- **`rejectNUL` and `rejectBadQuery`.** A NUL in the path is 404 `not-found`, in the query or a body 400 `malformed-request-body`, before the
  key middleware records anything (Postgres text cannot hold U+0000: SQLSTATE 22021 was a 500 on sibling services). A query string that
  does not percent-decode is 400, because `net/url` would otherwise drop the pair and read the parameter as absent.
- **Readiness.** `GET /readyz` is the drain gate plus two dependency checks (cached 3 s, bounded 2 s): the pgx pool ping and Kafka
  reachability. The Kafka check dials a fresh connection on every call and sends a metadata request with an EMPTY, non-nil topic list
  (`Conn.Brokers`): zero topics, not the null list that means "all topics", so asking never creates a topic and the service's own topics
  need not exist; `kafka-go`'s `Client.Metadata` answers from a cached cluster layout and would keep saying ready after the broker is gone.
  A failing dependency is 503 `dependency-unavailable` naming it; draining is 503 `{"status":"not_ready"}`. `GET /healthz` is liveness
  only and never flips.
- **Report route (D-13).** `GET /reports/schedule-adherence` reads `ports.AdherenceReader` (rows per site and carrier; the adapter adds
  the total and the shares). Without a reader, which is the case until the analytics projector lands (ADR 0006), the route answers the
  contract shape with no rows. The range is mandatory, `from < to`, at most 92 days.
- **`cmd/api`.** `run(ctx)` -> `runWith(ctx, getenv, onListen)`: load config (a missing `DATABASE_URL`, `KAFKA_BROKERS` or mandatory
  `*_CONSUMER_GROUP` is a boot error naming the variable, every missing one at once), run the embedded migrations with the boot retry,
  open the pool, build the Kafka consumers (ADR 0009), start the outbox relay and the consumers, serve. Shutdown order: `/readyz` -> 503,
  drain delay, HTTP drain, consumers stop (their effects enqueue outbox rows), relay stop, Kafka writer close (bounded), pool close
  last; the process exits non-zero when the relay or the consumers return on their own. It never imports the in-memory adapters.
  Metrics: `GET /metrics` serves a private Prometheus registry (Go and process collectors, `dock_scheduling_http_requests_total`,
  `dock_scheduling_http_request_duration_seconds`, `dock_scheduling_ignored_facts_total{reason}`,
  `dock_scheduling_implicit_arrivals_total`); the same `telemetry.Metrics` is the `ports.Metrics` of the use cases, so the consumers'
  ignored facts and implicit arrivals are counted in the API's registry. Clock and ids are `internal/adapters/outbound/system`.

## Assumptions

- **A22** The API process runs the outbox relay itself (A21 left this open). Several replicas relaying at once are safe because the
  store claims rows with `FOR UPDATE SKIP LOCKED`; `cmd/relay` stays as the same loop in a stand-alone binary for a deployment that wants
  to scale the relay apart. There is no switch to turn the embedded relay off: an API that cannot publish is a misconfiguration, so the
  process exits non-zero when the relay returns on its own.
- **A23** A malformed path identifier (appointment id, door code, site id) is 400 `invalid-path-parameter`, not 422: it never reaches a
  use case, so it is not a value-object rule of a command. The same door code in a request BODY is 422 `invalid-door-code`.
- **A24** `POST /no-show-sweeps` keeps the `Idempotency-Key` requirement of the contract. A CronJob sends one key per run.
- **A25** The Kafka consumers (yard facts, facility layout, ASN, CPT cutoffs and the optional legacy importer) run inside `cmd/api`
  next to the relay (ADR 0009); their groups are `YARD_CONSUMER_GROUP`, `FACILITY_CONSUMER_GROUP`, `ASN_CONSUMER_GROUP`,
  `CPT_CONSUMER_GROUP` (mandatory) and `DOCK_LEGACY_IMPORT_CONSUMER_GROUP` (unset = importer off). A booking of a door that no
  facility event announced is 422 `unknown-door`. The real-wiring test feeds doors, ASNs and yard facts through Kafka only.
- **A26** CORS is not configured: the API is cluster-internal behind the gateway (fleet decision 2026-09-11, no auth layer).
