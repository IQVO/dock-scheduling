---
id: 0012-carrier-registry
slug: /adr/0012-carrier-registry
title: "12. The carrier registry: carriers keyed by SCAC, an opt-in booking rule and three carrier events"
sidebar_label: "12. Carrier registry"
sidebar_position: 12
description: "dock-scheduling gains a small Carrier aggregate keyed by SCAC (with USDOT and MC/MX numbers and an ACTIVE or SUSPENDED status); a booking that names a SUSPENDED carrier is refused, an unregistered SCAC is accepted as before; CarrierRegistered, CarrierSuspended and CarrierReinstated are published."
---

# 12. The carrier registry: carriers keyed by SCAC, an opt-in booking rule and three carrier events

Status: Accepted (2026-11-10). Extends ADR 0002 (a fourth aggregate and a new refusal on the booking commands), ADR 0004 (three
published types), ADR 0006 (the analytics twin of each), ADR 0005 (a table and the outbox) and ADR 0007 (five MCP tools, which raises
the tool budget from nine to fourteen). Additive: no existing field, route, type or table changes; one new table (migration `0003`).

## Context

A booking carries a free-text carrier `name` and an optional `carrier_scac`. Nothing in this context knows who the carriers are, so a
dock planner cannot stop a carrier that must not load (lapsed insurance, revoked authority) from taking a door window, and the
other contexts have no fact to react to. The dock calendar needs to know who may book.

Carriers are identified in the industry by three different numbers, issued by different bodies for different purposes:

| Identifier | Issued by | What it identifies | Shape |
| --- | --- | --- | --- |
| **SCAC** (Standard Carrier Alpha Code) | NMFTA | the carrier in commercial and EDI documents (bookings, bills of lading, shipping notices) | 2 to 4 upper-case letters |
| **USDOT number** | FMCSA | the carrier's safety registration (inspections, crashes, compliance reviews) | up to 8 digits |
| **MC / MX number** | FMCSA | the carrier's operating authority docket: `MC` for-hire motor carriers, `MX` Mexico-domiciled carriers | `MC` or `MX` followed by up to 8 digits |

The SCAC is the only one of the three that already rides on a booking (`carrier_scac`) and on every sibling event that names a
carrier, and it is the only one that is unique per carrier in the documents this context handles. So it is the key. USDOT and MC/MX
are kept as attributes a planner or a compliance process can look up and compare; they are not keys and are never required.

Alternatives weighed:

- **A site or global setting "only registered carriers may book" (strict mode).** Rejected for now: it would refuse every booking of
  every carrier on the day it is switched on, and the registry starts empty. It is left as a future toggle (below).
- **Make the registry mandatory from the start, back-filling the carriers of existing appointments.** Rejected: it is a data
  migration with a guess for every carrier name, and it breaks outbound-shipping and the REST clients that book today with a SCAC
  this context has never seen.
- **Key the carrier by a generated id and make SCAC an attribute.** Rejected: nothing else in the fleet knows that id, every sibling
  would have to map SCAC to it, and two registrations of one SCAC would be possible.
- **Key by USDOT number.** Rejected: not every carrier that books a door has one (a SCAC-only carrier is normal), and it is not what
  a booking names.
- **Cancel the appointments of a carrier when it is suspended.** Rejected: whether a suspended carrier's loads are refused, moved or
  allowed to finish is an operational decision. The event lets a context that owns that decision react.
- **Look the registry up from another context synchronously.** Rejected by the fleet rule (no synchronous calls to sibling contexts);
  the registry lives here, where the calendar decides.

## Decision

### The aggregate

`Carrier` (package `internal/domain/carrier`, root id `scac`) holds `scac`, `name`, optional `usdot`, optional `mc`, `status`,
`suspension reason` and `suspended_at` (only while suspended), `version`, `created_at` and `updated_at`. It is a pure package with no
infrastructure import.

- **SCAC**: 2 to 4 upper-case ASCII letters. Anything else is `ErrInvalidScac`, REST 422 `invalid-scac`. The registry never
  normalises case: `acmf` is refused, not folded to `ACMF`, so the key in the registry is exactly the key on a booking.
- **USDOT**: 1 to 8 digits, optional. `ErrInvalidUSDOT`, 422 `invalid-usdot`. Leading zeros are kept (it is an identifier, not a number).
- **MC / MX**: 1 to 8 digits with an optional `MC` or `MX` prefix, case-insensitive, optionally followed by one space or hyphen
  (`mc-654321`, `MX 12`, `654321`). It is normalised to `MC<digits>` or `MX<digits>`; no prefix means MC. Anything else, `FF` freight
  forwarder numbers included, is `ErrInvalidMC`, 422 `invalid-mc`.
- **Name**: 1 to 100 characters, not blank, no control characters. `ErrInvalidCarrierName`, 422 `invalid-carrier-name`.
- **Status**: `ACTIVE` or `SUSPENDED`. A new carrier is `ACTIVE`.
- **Suspension reason**: mandatory on a suspension, 1 to 200 characters, no control characters. `ErrInvalidSuspensionReason`, 422
  `invalid-suspension-reason`. It is shown to whoever is refused.
- **Version**: starts at 1, plus 1 per accepted change; every write is guarded by it (optimistic concurrency, as the other aggregates).

| From | To | Command | Raised event |
|---|---|---|---|
| (absent) | `ACTIVE` | `RegisterCarrier` | `CarrierRegistered` |
| `ACTIVE`, `SUSPENDED` | same status | `UpdateCarrier` (name, USDOT, MC) | `CarrierRegistered` (only when the data changed) |
| `ACTIVE` | `SUSPENDED` | `SuspendCarrier(reason)` | `CarrierSuspended` |
| `SUSPENDED` | `ACTIVE` | `ReinstateCarrier` | `CarrierReinstated` |

`RegisterCarrier` is idempotent by SCAC. The same data on a registered SCAC (the MC compared in its normalised form) is a no-op: no
version bump, no event, `200`. Different data on a registered SCAC is refused with 409 `carrier-already-registered`: a registration
never silently overwrites; a change is the explicit `UpdateCarrier`. Suspending a suspended carrier is 409
`carrier-already-suspended`, reinstating an active one is 409 `carrier-not-suspended`. Suspension does not touch the identity data.
Two concurrent first registrations of one SCAC are serialised by a per-SCAC lock (`GetForUpdate`) in the unit of work, so exactly
one creates it.

### REST

| Route | Command | Success | Notes |
| --- | --- | --- | --- |
| `PUT /carriers/{scac}` | `RegisterCarrier` | `201` new (with `Location`), `200` same data | idempotent by method, no `Idempotency-Key`; 409 `carrier-already-registered` for other data |
| `GET /carriers/{scac}` | `GetCarrier` | `200` with `ETag` | 404 `carrier-not-found` |
| `GET /carriers` | `ListCarriers` | `200 {items, nextCursor}` | filter `status`; keyset-paged by SCAC |
| `PATCH /carriers/{scac}` | `UpdateCarrier` | `200` | JSON merge patch of `name`, `usdot`, `mc`; `null` or `""` clears an optional one; `Idempotency-Key` required, `If-Match` honoured |
| `POST /carriers/{scac}/suspensions` | `SuspendCarrier` | `200` | body `{reason}`; `Idempotency-Key` required, `If-Match` honoured |
| `POST /carriers/{scac}/reinstatements` | `ReinstateCarrier` | `200` | no body; `Idempotency-Key` required, `If-Match` honoured |

The shape follows the repo's style: the resource is addressed by its natural key, the idempotent create is a `PUT` (as the
operating-hours declaration), and each state change is a noun sub-resource that is `POST`ed (`suspensions`, `reinstatements`, like
`cancellations` and `reschedules`) so it can carry its own body, `Idempotency-Key` and event. A reinstatement is not a `DELETE` of the
suspension: the carrier stays and the fact "reinstated" is its own event. `ETag` is `W/"<version>"` as for appointments. The wire
fields are camelCase (`suspensionReason`, `suspendedAt`, `createdAt`), absent optionals are `null`.

### The booking rule (rollout-safe, opt-in)

Only a booking or reschedule that names a `carrier_scac` consults the registry, inside the same transaction that books:

| The named SCAC is... | `BookAppointment` / `RescheduleAppointment` |
| --- | --- |
| registered and `SUSPENDED` | refused, 409 `carrier-suspended` (the detail names the SCAC and the reason) |
| registered and `ACTIVE` | accepted |
| **not in the registry** | **accepted exactly as today** |
| (no `carrier_scac` on the booking) | accepted exactly as today; the registry is not read |

The registry is **opt-in and incremental**: it starts empty, a carrier enters it when someone registers it, and an unregistered SCAC
is not an unknown-carrier error. That keeps the change safe to deploy: no booking that was accepted yesterday is refused today
unless a person has explicitly suspended that carrier. No site setting or global flag is needed for this. A **strict mode** ("a
named SCAC must be registered") is a future, separate decision that would be a per-site toggle on the calendar settings; it is
deliberately not built here, because turning it on needs the registry to be populated first.

Existing appointments of a carrier that is later suspended are **not** cancelled or changed. They keep their state; the booking
rule only stops new and moved ones. The appointment read model has no "carrier suspended" flag in this ADR (it would be a derived
field that goes stale on every suspension); `CarrierSuspended` lets another context, or a report, find them. Suspending a carrier
while a booking is in flight can let that one booking through (the check reads the registry row without locking it); that is
accepted, as the booked appointment is exactly what a person reviews after a suspension anyway.

### Events

Three facts, raised by the `Carrier` aggregate and written through the transactional outbox (ADR 0005) in the same transaction as the
carrier row. Each goes to `warehouse.dock-scheduling.events` and, as its analytics twin, to `warehouse.dock-scheduling.analytics`
(ADR 0006): same CloudEvents `id`, same `type`, same key, byte-identical `data`, only the `dataschema` stream segment differs.
CloudEvents 1.0 structured mode, `source` `/warehouse/dock-scheduling`, `type`
`com.warehouse.wms.dock-scheduling.carrier.<EventName>`, `subject` and Kafka key = the SCAC, `dataschema`
`urn:warehouse:dock-scheduling:<stream>:<EventName>:v1`. `data` is snake_case, times RFC 3339 UTC.

| Event | `data` | When |
| --- | --- | --- |
| `CarrierRegistered` | `scac`, `name`, `usdot`, `mc`, `status`, `reason`, `suspended_at`, `version` | first registration and each update that changed data: always the FULL snapshot after the change; absent optionals are `null` |
| `CarrierSuspended` | `scac`, `reason`, `suspended_at`, `version` | a carrier was suspended |
| `CarrierReinstated` | `scac`, `reinstated_at`, `version` | a suspended carrier was reinstated |

The `version` is the carrier version after the change; a reader applies only a version greater than the one it holds. A reader that
wants the identity after a suspension combines the last `CarrierRegistered` with the later status facts, or reads
`GET /carriers/{scac}`. A carrier fact raised by an MCP write carries an optional `initiated_by` object in `data` (`via` `mcp`, and `proposal_id`, `approved_by`,
`agent_id` when supplied: the fleet's agent-authority rule, the attribution ADR 0007 builds); a REST write carries none and the key is
absent. The appointment facts do not carry it yet (the follow-up in ADR 0007); the carrier facts are the first to do so, and the
field is optional so no consumer is affected.

Consumers: none yet. yard-management is expected to copy the registry in a later item (YF-1), so the three integration messages in
`apis/asyncapi.yaml` carry `x-no-consumer-reason` naming that planned consumer, until it lands. The analytics projector acknowledges
the three types without folding them: the adherence report needs no carrier fact (the appointment facts already carry
`carrier_scac`), and an unhandled `type` on the analytics topic is committed past, so adding them required no projector change.

Additive for the existing consumers of `warehouse.dock-scheduling.events`: they dispatch on the full `type` and ignore a type they do
not know (checked for the four readers of the topic in ADR 0010). outbound-shipping, which pins this repo's `apis/asyncapi.yaml` and
consumes `DockAppointmentBooked`, keeps decoding: no existing message or field changed, only three messages and their schemas were
added.

### MCP

Five tools are added: `list_carriers` and `get_carrier` (L1, read-only) and `register_carrier`, `suspend_carrier`, `reinstate_carrier`
(L3, like every write: a human confirmation held by the caller, `idempotencyKey` required, optional `proposalId`, `approvedBy` and
`agentId` echoed as `initiatedBy`). The tool budget is raised from nine to fourteen by this ADR (the governance test pins it).
`UpdateCarrier` stays on REST: changing a carrier's identity data is a compliance operation, not an agent intent. The tool
descriptions of `book_appointment` and `reschedule_appointment` and the planning prompt now mention `carrier-suspended`, and the golden
`testdata/tool_registry.golden.json` pins all fourteen.

### Persistence

Migration `0003_carriers` creates the `carriers` table (primary key `scac` with the same pattern as a CHECK, `usdot` and `mc` as
empty-string-for-none text with CHECKs, `status`, `suspension_reason`, `suspended_at`, `version`, timestamps). It is purely additive
and the down migration drops only that table, so an older binary keeps working against the migrated database. The repository port
`ports.CarrierRepo`, its Postgres adapter and the in-memory fake share one contract test (`internal/testing/repocontract`).

## Consequences

- A carrier can be refused at the calendar, with a reason, with one command, and the rest of the fleet learns it from an event.
- No booking behaviour changes until a carrier is registered AND suspended. Rolling the binary back after a carrier was suspended
  makes that carrier bookable again (the table stays, the rule is gone), which is the safe direction.
- A SCAC the registry does not know cannot be told apart from a carrier nobody has bothered to register: the registry answers "who is
  known to be suspended", not "who is vetted". Strict mode (future) is what turns it into an allow-list.
- The suspended-carrier check adds one primary-key read to a booking that names a SCAC and none to one that does not.
- The integration topic carries three more types with no consumer until yard-management copies them; the analytics topic carries them
  too and the projector ignores them.
- The MCP surface grows to fourteen tools, which the fleet's authority tests pin.
- A breaking change to a carrier payload is a new `.v2` type with a `:v2` dataschema, on both streams (ADR 0004).
