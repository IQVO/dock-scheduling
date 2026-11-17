# 7. MCP server

Status: Accepted. Adds a second inbound (driving) adapter over the use cases of ADR 0002 and ADR 0008; the REST adapter and the domain are
unchanged. Code: `internal/adapters/inbound/mcp`, `cmd/mcp`, and two new application files, `internal/application/usecases/initiator.go` and
`idempotent.go`. Implements decision D-6 of the yard-management contracts and the fleet's Agentic Standard v2 (authority levels, stateless
transport, complete surface, attributable writes). Shape reference: inbound-receiving ADR 0005 / 0018 (read-only there; this context owns the
door calendar, so it also takes bookings) and workforce-management (attributed write).
Amended in part by ADR 0012: five carrier registry tools (`list_carriers`, `get_carrier`, `register_carrier`, `suspend_carrier`,
`reinstate_carrier`) bring the surface from nine to fourteen tools; the rest of this ADR stands.

## Decisions

- **A separate binary, the same use cases.** `cmd/mcp` is its own composition root: it runs the embedded migrations (like every sibling), opens the
  pgx pool and wires the SAME `usecases.Service` the REST adapter uses, plus `usecases.Idempotency`. It serves Streamable HTTP only (no stdio, no
  SSE), at `/mcp` and `/`, with `GET /healthz` (liveness), `GET /readyz` (drain gate, Postgres ping, Kafka broker metadata, like `cmd/api`) and
  `GET /metrics`. The adapter imports the application layer and the domain only; `TestMCPAdapterDependencyRule` keeps it off the outbound adapters
  and keeps everything else off it. No in-memory adapter in `cmd/**`.
- **Stateless.** `StreamableHTTPOptions{Stateless: true}`: no `Mcp-Session-Id`, the 2026-07-28 per-request handshake (`server/discover`) is answered,
  legacy `initialize` clients keep working. Nothing relies on session state; writes are made safe to retry by the idempotency key instead.
- **Nine intent-level tools** (decision D-6), camelCase arguments like the REST bodies, times RFC 3339 strings the adapter parses (a bad value is an
  `isError` result with slug `invalid-argument`, not a protocol error):

  | Tool | Level | Use case | REST twin |
  |---|---|---|---|
  | `list_appointments` | L1 | `ListAppointments` | `GET /appointments` |
  | `get_appointment` | L1 | `GetAppointment` | `GET /appointments/{id}` |
  | `get_door_availability` | L1 | `GetDoorAvailability` | `GET /doors/{doorCode}/availability` |
  | `get_dock_calendar` | L1 | `GetDockCalendar` | `GET /sites/{siteId}/dock-calendar` |
  | `list_at_risk_appointments` | L1 | `ListAtRisk` | `GET /appointments?atRisk=true` |
  | `book_appointment` | L3 | `BookAppointment` | `POST /appointments` |
  | `reschedule_appointment` | L3 | `RescheduleAppointment` | `POST /appointments/{id}/reschedules` |
  | `cancel_appointment` | L3 | `CancelAppointment` | `POST /appointments/{id}/cancellations` |
  | `report_eta` | L3 | `ReportEta` | `POST /appointments/{id}/eta-reports` |

  Out of the surface on purpose: arrival, check-in, door release and door reassignment are yard facts applied from Kafka, never recorded from an API
  of this context; operating hours, settings, closures and the no-show sweep are dock-planner / cron operations and stay on REST. A fitness test
  fails on a tenth tool, on a write tool outside the four, and on any tool name that mentions those words.
- **Write tools are L3, not L2 (deviation from the wording of D-6).** The fleet rule and the template's `TestMCPWriteToolsAreAtLeastL3` reserve
  L1/L2 for read tools: a tool that is not provably read-only is L3 unless an ADR grants L4. Nothing here grants L4. L3 means the gating the CALLER
  must apply: a human confirmation sits between the proposal and the call, held by the client (the ops agent's confirmation token), never by this
  unauthenticated context. The domain invariants (no overlap, hours, closures, flow, ASN uniqueness, state machine) guard every write exactly as
  over REST, so a mistaken call is refused, not applied.
- **Idempotency matches REST.** Every write takes `idempotencyKey` (REST's `Idempotency-Key`: 1..128 printable ASCII, required). Lookup, work and the
  store of the answer run in ONE unit of work (`usecases.Idempotency`), so the aggregate, its outbox rows and the key commit or roll back together.
  Every answer below 5xx is stored, a typed client error included (the same refusal is replayed even if the world changed, like REST's stored 4xx);
  an untyped error (infrastructure) is never stored, so the same key can retry the real work. The same key with another tool or other business
  arguments is `idempotency-key-reused`; the fingerprint is the tool name plus the canonical JSON of the command (not the key, not the attribution
  labels). Racing calls keep the first writer (the primary key blocks the loser, which re-reads, rolls back and replays the winner's answer). The
  answer carries `replayed: true` on a replay. Keys are stored under the prefix `mcp:` in the shared `idempotency_keys` table so an agent key can
  never collide with a REST client's. `expectedVersion` (optional) is REST's `If-Match` on reschedule and cancel: `precondition-failed` when stale.
- **Attribution.** The three optional arguments `proposalId`, `approvedBy` and `agentId` (each at most 128 characters, trimmed) build a
  `usecases.Initiator` whose `via` is ALWAYS set by the adapter (`mcp`, never caller-supplied). It travels in the context
  (`usecases.WithInitiator`), is echoed as `initiatedBy` in the tool result, is recorded as span attributes (`initiated_by.*`) and in one structured
  audit log line per write, and is meant to be stamped by the outbox publisher as `initiated_by` into the event data (snake_case: `via`,
  `proposal_id`, `approved_by`, `agent_id`; omitted when zero; REST writes carry none). See "Follow-up".
- **Errors.** A tool error is `"<slug>: <detail>"` with `isError: true`. The slugs are the REST problem slugs (D-10: kebab-case of the `Err` name),
  pinned by `errors_internal_test.go`; the adapter adds `invalid-argument`, `invalid-initiator`, `invalid-appointment-id` and the three
  `idempotency-key-*`. An untyped error is logged and answered `internal-error` with no detail (no DSN, no SQL).
- **Resources and prompts.** Three scoped read-model resource templates (`dock://sites/{siteId}/calendar`, `dock://sites/{siteId}/at-risk`,
  `dock://appointments/{appointmentId}`; model-supplied segments are validated, never defaulted) and two operator SOP prompts
  (`plan_dock_booking`, `triage_at_risk_appointments`); a test checks every tool a prompt names exists.
- **Telemetry.** One OTel span per call, `mcp.tool <name>`, with `mcp.tool.name` and `warehouse.authority`; writes add `initiated_by.*` and
  `idempotency.replayed`. Spans are no-ops until a provider is installed (the service ships Prometheus metrics today, ADR 0008).
- **No auth** (fleet-wide revert 2026-09-11; `TestNoAuthMiddlewareReintroduced`). The Service is ClusterIP only.

## Configuration of `cmd/mcp`

`DATABASE_URL` (required, boot error naming it), `KAFKA_BROKERS` (required, boot error naming it: the writes enqueue outbox rows that the relay of
`cmd/api` / `cmd/relay` drains, and `/readyz` checks the broker like the API's), `MCP_ADDR` (default `:8090`), `MIGRATIONS_DATABASE_URL` (default
`DATABASE_URL`), `SHUTDOWN_DRAIN_DELAY` (default 5s), `LOG_LEVEL`. It starts no relay and no consumer.

## Follow-up (needs files outside this branch's scope)

The fleet rule asks the adapter to stamp `initiated_by` into the domain event's data. The write path is complete up to the context
(`usecases.InitiatorFrom(ctx)` inside the unit of work); the last step touches files another change owns: the domain events
(`dockappointment` Booked/Rescheduled/Cancelled events carry an `Initiator`), the outbox encoder (`appointmentFactData` /
`OutboxPublisher`), `apis/asyncapi.yaml` (an optional `initiated_by` object on those three schemas, plus ADR 0004 if the catalogue changes) and the
golden event tests. Until it lands the attribution is durable only in the audit log and traces, not in the event.

## Assumptions

- **M1** Tool arguments are camelCase (the REST bodies' casing, workforce-management's tool casing); event data stays snake_case.
- **M2** `report_eta` defaults an absent `source` to `mcp` (REST defaults to `api`), so the ETA's provenance is visible in `get_appointment`.
- **M3** `cancel_appointment` accepts only `CARRIER_REQUEST` and `SITE_REQUEST`, like REST; `ASN_CANCELLED` and `TURNED_AWAY` belong to the policies.
- **M4** The list tools accept `limit` 1..500 (0 or absent = 100); a larger or negative value is `invalid-argument`, not silently clamped.
- **M5** A stored client-error answer is replayed verbatim for its key even after the cause is gone (REST does the same with its stored 4xx); the
  agent uses a new key for a deliberate retry after fixing the cause.
- **M6** The tool budget is nine (the charter's eight plus the ninth D-6 names); raising it is an amendment of this ADR.
