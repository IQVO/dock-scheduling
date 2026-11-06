# 5. Postgres persistence and the transactional outbox

Status: Accepted. Implements the persistence side of ADR 0002 (aggregates, invariants), ADR 0003 (local copies) and ADR 0004
(CloudEvents catalogue). Code: `migrations/`, `internal/adapters/outbound/postgres`, `internal/adapters/outbound/kafka`,
`internal/adapters/outbound/outbox`, `cmd/relay`.

## Schema

golang-migrate, embedded in the binary (`migrations/*.sql`, `migrations.FS`); additive numbered pairs, every `down` recreates the
previous schema exactly. DDL runs on `MIGRATIONS_DATABASE_URL` (a direct connection) and falls back to `DATABASE_URL`.

| Table | Holds |
|---|---|
| `dock_appointments` | one row per DockAppointment (the whole snapshot); `version` guards every UPDATE |
| `asn_claims` | one row per ASN of every ACTIVE NATIVE appointment, maintained in the appointment's transaction |
| `dock_calendars`, `calendar_operating_hours`, `calendar_closures` | the DockCalendar aggregate, one calendar per site |
| `zones`, `doors`, `asns`, `cutoff_schedules`, `cutoffs` | the event-fed local copies of ADR 0003 |
| `processed_events` | the consumers' dedupe guard, claimed in the same transaction as the effect |
| `outbox` | the transactional outbox |
| `idempotency_keys` | stored answers of requests made with an Idempotency-Key (2xx and 4xx only) |

## Backstops (the domain decides, the database refuses the loser of a race)

- **Door overlap.** `DoorSchedule.CheckNoOverlap` decides under a per-door `pg_advisory_xact_lock` (`AppointmentRepo.LockDoor`, keyed by
  the door code, which is globally unique per X5). The exclusion constraint `ex_dock_appointments_door_window`
  (`EXCLUDE USING gist (door_code WITH =, tstzrange(window_start, window_end, '[)') WITH &&)` over active native rows that are not a
  door override) is the backstop; SQLSTATE `23P01` surfaces as `ErrDoorDoubleBooked`. Rows whose door was set by a physical fact
  (`door_override`, ActualDoorWins) and legacy imports (D-2: the import enforces no overlap) are outside it (A19).
- **One appointment per ASN.** The primary key of `asn_claims` (SQLSTATE `23505`) surfaces as `ErrAsnAlreadyScheduled` (D-12). Legacy
  imports claim nothing.
- **Optimistic version.** `Save(a, expected)` inserts when `expected` is 0 and otherwise updates `WHERE version = expected`; a stale
  version, a missing row or a second insert of one id is `ErrConcurrentUpdate`. The same holds for the calendar.
- **Calendar creation** takes `pg_advisory_xact_lock` on the site, because a site without a row has no row to lock.

## Unit of work

`UnitOfWork.Do` begins one pgx transaction, carries it in the context (`pgtx`) and commits on nil. A nested `Do` joins. Every repository
statement group runs as a SAVEPOINT of the open transaction (or as its own transaction outside one), so a refused save (a clash on
the door, an ASN taken) does not poison the caller's transaction. The aggregate rows, the `asn_claims`, the `outbox` rows and the
consumer's `processed_events` claim commit or roll back together.

## Outbox and relay

- `OutboxPublisher` writes one row per domain fact in the SAME transaction as the aggregate. The CloudEvents id (UUID v4) is minted
  once, at enqueue, and persisted with the row, so a relay retry republishes the same id and consumers dedupe on it. The payload is
  `{event, aggregate_id, occurred_at, data}` with `data` already in the wire shape of ADR 0004; the relay never re-derives domain state.
  A unit test asserts that each domain event renders exactly the `data` of its golden CloudEvent.
- `kafka.Encode` routes by the catalogue: integration facts go to `warehouse.dock-scheduling.events` and, as a twin with the same id,
  to `warehouse.dock-scheduling.analytics`; analytics-only facts go ONLY to the analytics topic. Key and subject are the aggregate id
  (the site id for calendar facts). The deployment provisions `kafka.Topics()`: events, analytics and `<events>.dlq`.
- `OutboxStore.Drain` claims due rows in id order under `FOR UPDATE SKIP LOCKED` (overlapping relays claim disjoint sets), hands each to
  the sink inside the pass's transaction and deletes it only after the broker accepted it: delivery is at-least-once. The first failed
  row stops the pass: `attempts + 1`, `next_attempt_at` pushed `min(300, 2^attempts)` seconds ahead, the rows before it stay deleted.
- **Attempt cap.** `OUTBOX_MAX_ATTEMPTS` (default 20, 0 = unlimited; a typo falls back to the default with a warning) excludes a row
  from further claims once it has failed that many times. The failure that reaches the cap is logged once at ERROR (id, event type,
  key, attempts) and the row stays in the outbox for inspection; an operator re-arms it with
  `UPDATE outbox SET attempts = 0, next_attempt_at = now() WHERE id = ...`.
- **Ordering trade-off (deliberate, fleet-wide).** While a row backs off, later rows, including later rows of the same aggregate, are
  claimed by the next passes, and once the cap excludes a poison row the rows behind it publish. Per-key order is therefore guaranteed
  only while every row publishes; a consumer must be tolerant of an out-of-order version (the consumers of ADR 0003 are: copies are
  version-guarded and yard facts are idempotent). The alternative, a poison row blocking the relay forever, is worse.
- **Dead letters.** A row with no wire shape (an event the catalogue does not know, a payload that is not the publisher's envelope, an
  unbuildable envelope) can never publish. The sink writes its raw payload to the DLQ topic, keyed by the aggregate id, with
  `x-dlq-error`, `x-dlq-outbox-row`, `x-dlq-aggregate`, `x-dlq-failed-at` and `content-type` headers, and the row is disposed of. A failed
  DLQ write is an ordinary send failure (retried, counted). A cancelled context never dead-letters.
- **Shutdown.** `cmd/relay` stops the loop first (an in-flight pass finishes or rolls back, so each row is either published and removed or
  still in the outbox), then closes the Kafka writer with a hard deadline, then the pool.

## Assumptions

- **A19** The door exclusion constraint covers native, non-override, active rows only (see Backstops). A legacy import can therefore
  overlap another appointment on its door, as D-2 requires, and never fails on the constraint; a native booking still refuses to overlap
  a legacy one because `DoorSchedule` sees every active appointment of the door under the lock.
- **A20** `GetForUpdate` on a calendar takes a per-site advisory lock rather than a row lock, so creating the first calendar of a site
  is serialised too.
- **A21** The relay runs as its own binary (`cmd/relay`, one replica or more, safe to overlap); the API image wires the same relay code
  only if a later step chooses to embed it.
