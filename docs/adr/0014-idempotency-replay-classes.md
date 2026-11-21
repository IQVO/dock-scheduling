# ADR 0014: Every problem type has a replay class; a retryable refusal is not stored under the Idempotency-Key

## Status

Accepted (2026-11-21). Refines ADR 0008 (the Idempotency-Key middleware: a stored `4xx` is no longer one rule for every refusal) and ADR 0007 (the MCP write wrapper applies the same classes). Decided by the owner on 2026-11-18 as a fleet rule; the standard is `docs/idempotency-replay-classes.md` in warehouse-harness-template and `.claude/rules/fleet/idempotency-and-outbox.md`. Code: `internal/adapters/inbound/http/errors.go` (the class of each catalogue entry), `replay_class.go`, `capture.go`, `idempotency.go`; `internal/adapters/inbound/mcp/errors.go`, `replay_class.go`; `internal/application/usecases/idempotent.go`.

## Context

A request retried under the same `Idempotency-Key` was answered from the store whatever the first answer was, a `4xx` included (ADR 0008: "Every answer below 500 is stored, 4xx included"). That is wrong for a refusal the same request could pass later: a `409 concurrent-update` is gone on the next attempt, a `422 unknown-asn` stops being true when the inbound-receiving event arrives, a `422 window-in-past` stops being true tomorrow, a `409 carrier-suspended` stops being true after `POST /carriers/{scac}/reinstatements`. A client that reuses its key (`cmd/sweep` reuses one UUID across the retries of a pass) was handed the stale refusal forever — this store has no TTL and no purge job (`docs/docs/operations/runbook.md`, "Clear idempotency keys"), so "forever" is literal. It also cannot simply be "never store a 4xx": the fleet rule keeps a class for refusals that must be remembered.

## Decision

Every problem type belongs to exactly one replay class, declared on its entry of the problem catalogue (`problemCatalogue` in `internal/adapters/inbound/http/errors.go`). The middleware reads the class (`verdict` in `idempotency.go`) and holds no list of slugs and no status range beyond 2xx and 5xx.

| Class | Meaning | The middleware (this store) |
| --- | --- | --- |
| FINAL | the answer cannot change for this request | JOINED: the unit of work rolls back, the refusal is stored on its own afterwards, a replay returns it with `Idempotency-Replayed: true`. DETACHED: stored after the handler. |
| OUTCOME | the command COMMITTED state and then answered an error that must be remembered | JOINED: the writes commit together with the stored answer in the one transaction. DETACHED: the handler's own commits stay and the answer is stored after. |
| RETRYABLE | nothing was committed (the refusal's own work rolled back) and the answer depends on state that can change | nothing is stored, the key is released: the same request under the same key runs again, and a different body under it is a NEW request |

A `2xx` is stored and a `5xx` or a panic is never stored, as before. A runtime answer the catalogue does not know (an unmapped error, a response written without the shared error writer) is released like RETRYABLE — the safe fallback. There is no runtime default: an entry without a class fails `TestEveryCatalogueEntryDeclaresAReplayClass`, a problem the adapter writes that is neither a catalogue entry nor listed as OUTSIDE fails `TestEveryProblemTheAdapterWritesHasAClassOrIsOutsideTheStore`, and this table and the catalogue are held equal by `TestTheADRDeclaresTheClassOfEveryProblem` (both in `internal/adapters/inbound/http`); the MCP catalogue is held to the same table by `internal/adapters/inbound/mcp`.

**How the class was chosen.** FINAL only when the answer cannot change: a field of the request, or a fact that only moves one way (a version that strictly increases, a lifted closure, an appointment state machine with no way back to BOOKED). OUTCOME only for a refusal that provably commits state; no entry qualifies today (see "No OUTCOME today"). Everything else that depends on state that can change is RETRYABLE, and when unsure RETRYABLE: a released refusal costs one more execution, a wrongly FINAL one pins a refusal that is no longer true — with no TTL, for as long as the row lives.

### The store this decides in

`internal/adapters/inbound/http/idempotency.go`, two modes (ADR 0008):

- JOINED (every keyed route except the sweep): the middleware opens ONE unit of work; the lookup, the handler (whose use cases join the same transaction) and the store of a storable answer commit or roll back together. A FINAL 4xx and any 5xx roll the whole unit back — the handler's writes are discarded — and the FINAL answer is then stored on its own in a separate unit of work (a store failure is logged, the answer still sent). A RETRYABLE refusal rolls back and stores nothing: the key is released exactly like the 5xx branch. An OUTCOME refusal would keep the work: `verdict` returns nil and the writes commit with the key row in the one transaction.
- DETACHED (`POST /no-show-sweeps` only): no outer unit of work; the sweep commits one transaction per appointment itself, and a storable answer is stored after the handler. Whatever the answer, the appointments already marked stay marked (the sweep is naturally idempotent: a marked appointment is no longer a candidate, and `ErrNoShowTooEarly`/`ErrIllegalTransition` per appointment are swallowed, `internal/application/usecases/change.go`).

The store itself is `postgres/idempotency_store.go`: `Lookup` has no created_at predicate (no TTL) and `Save` is `INSERT ... ON CONFLICT DO NOTHING` (first writer wins; the loser's post-save re-read finds the winner and replays it). When the first request ends RETRYABLE nothing is stored, so a concurrent duplicate finds no answer and runs the handler itself — both execute, exactly as a retry after the fact; that is the release semantics, not a bug.

### Classification (REST catalogue)

| Problem type | Class | Status | Reason (from the code) |
| --- | --- | --- | --- |
| `malformed-request-body` | FINAL | 400 | the body is not valid JSON for the operation: a function of the request (`decodeJSON`) |
| `unknown-query-parameter` | FINAL | 400 | a query parameter the operation does not take |
| `invalid-limit` | FINAL | 400 | the paging limit is invalid |
| `invalid-cursor` | FINAL | 400 | the paging cursor is invalid (two catalogue entries, one slug, one class) |
| `invalid-availability-query` | FINAL | 400 | both bounds are in the request |
| `invalid-query-parameter` | FINAL | 400 | a query value does not decode or parse |
| `invalid-path-parameter` | FINAL | 400 | a path value does not parse |
| `invalid-if-match` | FINAL | 400 | the `If-Match` header is not an entity tag of a version |
| `invalid-report-range` | FINAL | 400 | the reports' `from`/`to` are in the request (cmd/reports, not a keyed route; classified anyway) |
| `appointment-not-found` | FINAL | 404 | the id is minted by this service (`IDs.NewAppointmentID`), an appointment row is never deleted (only its `asn_claims` are released on cancel), and the slug is not shared: a carrier miss is translated to `carrier-not-found` (`loadCarrier`, usecases/carriers.go) and a missing calendar is lazily defaulted (`CalendarRepo.Get`), never `ports.ErrNotFound`. J5 |
| `door-not-found` | RETRYABLE | 404 | a door is an event-fed local copy of a facility-layout fact (ADR 0003) that a later event can deliver; answered only by the (unkeyed) availability query today, classified so the first keyed route that answers it is already right |
| `unknown-closure` | FINAL | 404 | the closure id is minted here (`IDs.NewClosureID`), a closure is lifted (a flag), never deleted |
| `carrier-not-found` | RETRYABLE | 404 | the SCAC is a natural key chosen by the caller and the registry is filled by a later `PUT /carriers/{scac}`: the fact can arrive later (the fleet natural-key rule). J4 |
| `precondition-failed` | FINAL | 412 | the `If-Match` names a version and versions only grow (1, +1 per accepted change): a stale one never matches again |
| `door-double-booked` | RETRYABLE | 409 | the overlapping window frees when the other appointment is cancelled or moved (`CheckNoOverlap` over the active list) |
| `door-unavailable` | RETRYABLE | 409 | the closure is lifted or ends |
| `asn-already-scheduled` | RETRYABLE | 409 | the ASN's claim frees when the other appointment is cancelled |
| `closure-conflicts-with-appointments` | RETRYABLE | 409 | the active appointments can move or cancel |
| `hours-conflict-with-appointments` | RETRYABLE | 409 | same |
| `overlapping-closure` | RETRYABLE | 409 | the overlapping closure can be lifted |
| `closure-already-lifted` | FINAL | 409 | `Lifted` is a one-way flag (`LiftClosure` sets it, nothing resets it; a later closure is a new id) |
| `illegal-transition` | FINAL | 409 | the state machine only moves forward (Booked → CheckedIn → Completed, Booked/CheckedIn → Cancelled, Booked → NoShow) and no transition re-enters Booked or Active, so a command refused for state can never pass (Reschedule and ReportEta need Booked, Cancel needs Active). The sweep swallows it per appointment (change.go). J2 |
| `concurrent-update` | RETRYABLE | 409 | the optimistic version guard lost a race (`UPDATE ... WHERE version = $n` and the insert PK conflict): the retry reads the new version and passes |
| `no-show-too-early` | RETRYABLE | 409 | the clock moves, so the grace passes (swallowed in the sweep today; classified by its meaning) |
| `carrier-suspended` | RETRYABLE | 409 | a plain pre-check (`checkCarrierMayBook`, usecases/carriers.go:252-270: a read, nothing recorded); a reinstatement clears it. The same slug is an OUTCOME in yard-management, where the turn-away is a committed REJECTED visit (ADR 0016 there) — class is per repo, from the code path that raises it. J1 |
| `carrier-already-registered` | RETRYABLE | 409 | the registered data can be changed by a later update (`PATCH /carriers/{scac}`, `update_carrier`), so the same registration could match later; raised by `PUT /carriers` (unkeyed) and `register_carrier` today |
| `carrier-already-suspended` | RETRYABLE | 409 | a reinstatement clears it |
| `carrier-not-suspended` | RETRYABLE | 409 | a suspension makes the reinstate pass |
| `invalid-door-code` | FINAL | 422 | a value-object rule of the request |
| `invalid-site-id` | FINAL | 422 | same |
| `invalid-carrier` | FINAL | 422 | same |
| `invalid-direction` | FINAL | 422 | same |
| `invalid-window` | FINAL | 422 | same |
| `window-in-past` | RETRYABLE | 422 | compared against the service clock (`Reschedule`, `Book`): the identical request passes tomorrow. J3 |
| `unknown-door` | RETRYABLE | 422 | the door copy is event-fed from facility-layout (ADR 0003): the fact can arrive later |
| `door-flow-mismatch` | RETRYABLE | 422 | the door's flow comes from the same event-fed copy and can change |
| `unknown-asn` | RETRYABLE | 422 | an ASN is a fact fed by inbound-receiving events (ADR 0003): it can arrive, or be uncancelled, later |
| `missing-outbound-ref` | FINAL | 422 | a function of the request |
| `duplicate-asn` | FINAL | 422 | same |
| `invalid-refs` | FINAL | 422 | same |
| `invalid-reason` | FINAL | 422 | same |
| `invalid-eta` | FINAL | 422 | a zero ETA or a too-long source label: the request |
| `invalid-operating-hours` | FINAL | 422 | the request |
| `invalid-settings` | FINAL | 422 | the request |
| `invalid-closure` | FINAL | 422 | the request |
| `invalid-scac` | FINAL | 422 | the request |
| `invalid-usdot` | FINAL | 422 | the request |
| `invalid-mc` | FINAL | 422 | the request |
| `invalid-carrier-name` | FINAL | 422 | the request |
| `invalid-suspension-reason` | FINAL | 422 | the request |

Problems the adapter answers that never reach the replay decision (no class, by construction):

| Problem type | Class | Status | Reason |
| --- | --- | --- | --- |
| `idempotency-key-required` | OUTSIDE | 400 | written by the middleware before any key exists |
| `idempotency-key-invalid` | OUTSIDE | 400 | written by the middleware before the key is used |
| `idempotency-key-reused` | OUTSIDE | 409 | written on a replay against a stored answer; it is never itself the stored answer |
| `not-found` | OUTSIDE | 404 | the router's unknown path and the NUL guard, before the keyed middleware |
| `method-not-allowed` | OUTSIDE | 405 | the router's 405, outside the keyed routes |
| `dependency-unavailable` | OUTSIDE | 503 | the readiness probe's 503, not a keyed route (and a 5xx is never stored) |
| `internal-error` | OUTSIDE | 500 | an unmapped error: a 5xx is never stored (released like RETRYABLE) |

### The MCP write tools (ADR 0007)

The seven write tools run behind `usecases.Idempotency.Do` — a second copy of the store-and-replay rule over the same `idempotency_keys` table with the `mcp:` key prefix. It applies the same classes through its `Failure` hook: `classify` (mcp/errors.go) stores a FINAL refusal as the stored answer and reports ok=false for a RETRYABLE one, which releases the key exactly as the REST middleware does. No OUTCOME can exist on this surface: the tool's work runs inside ONE joined unit of work and a client failure always rolls it back, so nothing commits; the day a tool use case commits before it refuses, the class is added there. The MCP catalogue carries the same class for the same slug as the REST catalogue, held to this table by test. MCP-only problem types:

| Problem type | Class | Status | Reason |
| --- | --- | --- | --- |
| `invalid-argument` | FINAL | 422 | an argument the adapter itself refuses before any use case runs (a timestamp that is not RFC 3339) |
| `invalid-initiator` | FINAL | 422 | the attribution labels are the request |
| `invalid-appointment-id` | FINAL | 422 | the `appt-<uuid>` shape is the request |

### No OUTCOME today

No catalogue entry is OUTCOME: in the JOINED mode a 4xx always rolled the whole unit of work back (nothing commits), and the DETACHED sweep's refusals never commit the failing appointment's own work (its per-appointment unit of work returns the error and rolls back, `markNoShow`). The class and its mechanism are in place anyway — `verdict` returns nil for an OUTCOME refusal, so JOINED commits the writes with the key row and DETACHED keeps them with the stored answer — and the synthetic proof (a test-only catalogue entry that writes through the request's unit of work and then refuses) pins the mechanism in `idempotency_classes_internal_test.go`. The first real OUTCOME is then a catalogue change, not a middleware change.

### The no-show sweep audit (a phase-0 candidate, verified)

`RunNoShowSweep` (usecases/change.go:92-111) commits each `markNoShow` in its own transaction and returns the FIRST error after the pass, so a 4xx answer can follow committed no-shows of other appointments. Verified from the code: the reachable 4xx slugs on `POST /no-show-sweeps` are `appointment-not-found` (a listed id whose row vanished — impossible today, appointments are never deleted), `concurrent-update` (the version guard) and calendar/carrier misses that cannot occur (the calendar is lazily defaulted; the sweep reads no carrier). All are class FINAL/RETRYABLE per their entries, so under this ADR the sweep's refusals are not... a FINAL one (`appointment-not-found`) would be stored; the realistic one (`concurrent-update`) is RETRYABLE and released. The committed no-shows that precede the refusal are successes of the same naturally idempotent command, not part of the refused answer: a retry re-runs the sweep, finds those appointments already NO_SHOW, and completes the rest — nothing is double-marked, and the refusal itself committed nothing (the failing appointment's own unit of work rolled back). This is why the sweep is NOT the fleet's next OUTCOME: there is no refusal here that must be remembered, and storing one would pin a stale "sweep failed" answer under the CronJob's pass key while appointments stayed unswept. Recorded as an open point: the partial-commit shape is worth its own finding if the owner ever wants the sweep's per-pass result recorded.

## Judgement calls (most consequential first)

1. **J1 — `carrier-suspended` is RETRYABLE here and OUTCOME in yard-management.** Same slug, opposite class: this service's `checkCarrierMayBook` is a pure read that refuses (nothing recorded, `usecases/carriers.go:252-270`); yard-management's gate check-in commits a REJECTED visit before it refuses (its ADR 0016). The class follows the code path that raises the slug, never the slug.
2. **J2 — `illegal-transition` is FINAL, not the phase-0 guess RETRYABLE.** The one-way proof: every command refuses unless the appointment is BOOKED (Reschedule, ReportEta) or Active (Cancel), and the machine (Book → CheckedIn → Completed; → Cancelled; → NoShow) never re-enters BOOKED/Active. A refusal therefore can never clear. The feature that would force reclassification: an un-cancel/reopen command, or rescheduling from CHECKED_IN.
3. **J3 — `window-in-past` is RETRYABLE (the phase-0 scan missed it).** It reads the service clock (`commands.go` Reschedule, `appointment.go` Book): the identical request passes tomorrow; a FINAL class would pin a stale refusal with no TTL.
4. **J4 — `carrier-not-found` is RETRYABLE although carriers are never deleted.** The SCAC is a caller-chosen natural key and `PUT /carriers/{scac}` can register it a moment later (fleet natural-key rule); the same argument makes `door-not-found` RETRYABLE when a keyed route can answer it.
5. **J5 — `appointment-not-found` is FINAL.** Ids are minted here, rows are never deleted, and the slug is not shared with a referenced fact (a carrier miss is translated, a missing calendar defaulted). A future delete or reopen of appointments must reclassify it.
6. **J6 — the sweep keeps DETACHED mode and gains released refusals, not a redesign.** Its committed successes stay (naturally idempotent re-runs), and `cmd/sweep`'s per-pass UUID now actually re-executes the remaining work on retry instead of replaying a stored refusal — the CronJob is the one deterministic-key caller in this repo's orbit and it benefits, no consumer change needed.

## Open points (not fixed in this PR)

- **No TTL and no purge job on `idempotency_keys`** (`postgres/idempotency_store.go` Lookup/Save, runbook "Clear idempotency keys"): a stored answer never expires. The fleet fix shape (one exported TTL constant, a background purge in both stores, the read guarded by the same TTL) is order-sourcing #84 / last-mile-delivery #86; this PR records it only. The class rule makes it less urgent (fewer refusals pinned) and more visible (a FINAL refusal now really does live forever).
- **The sweep's partial-commit-then-first-error shape** (audited above): correct under this ADR; a per-pass result record would be its own ADR if ever wanted.
- **`door-not-found` reachability**: only the (unkeyed) availability query answers it today; the entry is classified so the first keyed route that answers it is already right.

## Consequences

- A retried request is answered from the store only when its first answer is final for it: successes, FINAL and OUTCOME refusals. RETRYABLE refusals execute again on the same key, and a different body under a released key is a NEW request, not `idempotency-key-reused`.
- Callers that retry a RETRYABLE refusal under the same key now get real work instead of the stale refusal (the sweep CronJob is the known case). Callers driven by people surface the refusal either way; automation retry behaviour is a separate fleet decision, unchanged here.
- The wire is unchanged: same slugs, titles, statuses, bodies, `Idempotency-Replayed` semantics. Only which answers are remembered changed, and the spec text now says so (`apis/openapi.yaml`, descriptions only).
