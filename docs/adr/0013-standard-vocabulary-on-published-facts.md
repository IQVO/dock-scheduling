---
id: 0013-standard-vocabulary-on-published-facts
slug: /adr/0013-standard-vocabulary-on-published-facts
title: "13. Standard vocabulary on published facts: X12 214 status, CBV business step and read-point GLN"
sidebar_label: "13. Standard vocabulary"
sidebar_position: 13
description: "Every published dock-scheduling fact carries, as additive optional data fields, what it means in the standards partners read: x12_status (ANSI X12 214), cbv_biz_step (GS1 EPCIS Core Business Vocabulary) and read_point_gln (the GS1 GLN of the door). One tested mapping table, only honest mappings, nothing invented."
---

# 13. Standard vocabulary on published facts: X12 214 status, CBV business step and read-point GLN

Status: Accepted (2026-11-10). Extends ADR 0004 (three optional fields on the payloads), ADR 0006 (the analytics twin carries the same
fields), ADR 0003 (the consumed `LocationSlotRegistered` is read for one more optional field) and ADR 0005 (a column and the outbox row
builder). Additive: no type, field, route or table is removed or renamed; every new field is optional and omitted when absent; one new
nullable column (migration `0004`). Plan item YF-3, dock-scheduling side.

## Context

Partners do not want to learn this context's private event names and enums (`DockAppointmentCheckedIn`, `arrival_status`, a door code
that only facility-layout understands). They read standards: the ANSI X12 214 shipment status message on the carrier side, GS1 EPCIS
and its Core Business Vocabulary (CBV) on the supply-chain-visibility side, and the GS1 Global Location Number (GLN) as the identity of
a place. Each published fact should say, next to our own data, what it means in those terms, so that a partner (or a later adapter) never
has to guess a mapping we already know.

## Decision

### 1. Three optional fields on the `data` of the facts

| Field | Value | Present when |
|---|---|---|
| `x12_status` | the ANSI X12 214 status code (two characters) | the table below maps the event (and direction) to a code |
| `cbv_biz_step` | the GS1 CBV 2.0 business step, as the URN `urn:epcglobal:cbv:bizstep:<step>` | the table maps the event to a step |
| `read_point_gln` | the 13-digit GS1 GLN of the door where the fact happened | the fact has a door and facility-layout gave that door a GLN |

The CBV step is published in ONE form only, the URN form (CBV section 7.1), never the short word and never the web-URI form. A field
that has no value is OMITTED, never `null` and never `""`: a fact with no standard meaning and no door GLN serialises exactly as it did
before (the goldens of those facts did not change in this PR). The integration fact and its analytics twin are built once and carry
identical `data`; only the `dataschema` stream segment differs (ADR 0006). `cbv_disposition` is out of scope.

### 2. Only honest mappings

A pair (event, direction) maps to a code only when the standard's own definition says what the fact says. Otherwise the decision is
NOT MAPPED, with the reason written in the table, and the field is omitted. A code is never approximated, widened or invented, and
"close enough" is not a reason. A later, better-founded mapping is an additive change (a new value on an existing optional field).

### 3. The mapping

Direction is the appointment's direction. Sources are cited below the table; names are as the code lists publish them.

| Event | Direction | `x12_status` | `cbv_biz_step` | Decision |
|---|---|---|---|---|
| `DockAppointmentBooked` | INBOUND | `AB` Delivery Appointment Date and/or Time | not mapped | mapped: the booked window is the delivery appointment date and time (`window_start`) |
| `DockAppointmentBooked` | OUTBOUND | `AA` Pick-up Appointment Date and/or Time | not mapped | mapped: the booked window is the pick-up appointment date and time |
| `DockAppointmentRescheduled` | INBOUND | `AB` | not mapped | mapped: X12 has no "rescheduled" code; the appointment is restated at its new window (the previous window is not conveyed) |
| `DockAppointmentRescheduled` | OUTBOUND | `AA` | not mapped | mapped, same reasoning |
| `DockAppointmentCancelled` | both | not mapped | not mapped | X12 `CA` is Shipment Cancelled, but cancelling a door window does not cancel the shipment |
| `DockAppointmentNoShow` | both | not mapped | not mapped | `SN` (Shipment Not Picked Up By Carrier) is absent from the 004010 code list and a lapsed window does not mean the pick-up cannot still happen; `AY` and `A1` are reason codes (element 1651), not statuses |
| `DockAppointmentCheckedIn` | INBOUND | `X1` Arrived at Delivery Location | `urn:epcglobal:cbv:bizstep:arriving` | mapped: the carrier arrived at the site (gate check-in) |
| `DockAppointmentCheckedIn` | OUTBOUND | `X3` Arrived at Pick-up Location | `urn:epcglobal:cbv:bizstep:arriving` | mapped, same reasoning |
| `DockAppointmentDoorReassigned` | both | not mapped | not mapped | an internal move to another door; the appointment date and time, which is what `AA` and `AB` state, did not change |
| `DockAppointmentCompleted` | both | not mapped | not mapped | completion is the door release and does not say the handling finished (a release before any check-in completes the appointment with an implicit arrival): `D1` (Completed Unloading at Delivery Location), `CP` (Completed Loading at Pick-up Location) and the CBV steps `unloading` and `loading` would overstate it |
| `DockAppointmentAtRisk` | both | not mapped | not mapped | an advisory this context derives from a reported ETA, not a carrier-reported status; `X2` (Estimated Date and/or Time of Arrival at Consignee's Location) and `SD` (Shipment Delayed) state different things |
| `DoorClosureScheduled` | n/a | not mapped | not mapped | a door-calendar fact, not a shipment status |
| `DoorClosureLifted` | n/a | not mapped | not mapped | a door-calendar fact, not a shipment status |
| `DoorOperatingHoursDeclared` | n/a | not mapped | not mapped | a door-calendar fact, not a shipment status |
| `CarrierRegistered`, `CarrierSuspended`, `CarrierReinstated` | n/a | not mapped | not mapped | a registry change is not a shipment event in either standard |

The CBV has no business step for scheduling a door window, so no booking, reschedule, cancellation or reassignment maps to one.

Two details a reader of the X12 code lists needs. `AA` and `AB` are Shipment Appointment Status Codes (X12 element 1652, carried in
AT7-03), while `X1` and `X3` are Shipment Status Codes (element 1650, carried in AT7-01); the four codes are disjoint, so the field value
alone identifies the code. The 004010 list spells "Pick-up", the 008040 list "Pickup"; the code is the same. The X12 214 is a carrier-to-
shipper message: this context does not produce a 214, it states in the 214 vocabulary what its own fact means.

Sources (read on 2026-11-10):

- X12 element 1652, Shipment Appointment Status Code: https://www.stedi.com/edi/x12-004010/element/1652 (`AA`, `AB`; same codes on
  https://www.stedi.com/edi/x12-008040/element/1652).
- X12 element 1650, Shipment Status Code: https://www.stedi.com/edi/x12-004010/element/1650 (`X1`, `X3`, `CA`, `CP`, `D1`, `X2`, `SD`;
  `SN` is listed on https://www.stedi.com/edi/x12-008040/element/1650 only). Element 1651 (reasons): https://www.stedi.com/edi/x12-004010/element/1651.
  The AT7 segment: https://www.stedi.com/edi/x12-004010/segment/AT7. Trading-partner guides on the same site list `AA` and `AB`
  as the appointment codes (for example the Ruan and Bass Pro Shops 214 guides).
- GS1 Core Business Vocabulary 2.0, business step `arriving` ("a specific activity within a business process where an object arrives at
  a location"): https://ref.gs1.org/cbv/BizStep-arriving; the URN form and section 7.1: https://ref.gs1.org/standards/cbv/; the URN
  `urn:epcglobal:cbv:bizstep:arriving` is also listed by https://developers.wholechain.com/EventsV2/Bizsteps/.

### 4. `read_point_gln`: the GLN of the door

facility-layout (its ADR 0045) lets a dock slot carry an optional `gln` on `LocationSlotRegistered` (13 digits, GS1 mod-10 check digit).
This context keeps it in its own door copy and never calls facility-layout (ADR 0003):

- Migration `0004_door_gln` adds the nullable column `doors.gln` (13 digits). Rollback is `DROP COLUMN`; an older binary neither reads nor
  writes it.
- The facility consumer reads the optional `gln` (absent, `null` and unknown fields tolerated). A value that is not a valid GLN is
  dropped (the door is stored without one and stays bookable); it never dead-letters a registration. A re-registration that carries no
  `gln` keeps the one already stored (facility-layout sets it at registration only). A re-registration with another valid one replaces it.
  Uniqueness is facility-layout's rule and is not re-checked here.
- The read point of a fact is the door where it happened: the booked door, the NEW door of a reschedule, the door the trailer docked at
  for a reassignment (`to_door_code`), the closed door of a closure. It is resolved from the doors copy in the same transaction that
  writes the outbox row, so it is the GLN known when the fact was recorded. A door unknown or decommissioned by then, a door without a
  GLN, and the facts with no single door (`DoorOperatingHoursDeclared`, the carrier facts) publish no `read_point_gln`. Zone and site GLNs
  are not used: a read point here is a door.

### 5. One place, applied where the outbox row is built

`internal/domain/standards` holds the table (`Of(event, direction)`, `Decisions()`), pure Go with no I/O. The outbox row builder
(`internal/application/outbox`, shared by the Postgres publisher and the in-memory twin the acceptance tests run on) applies it, so the
integration and analytics payloads cannot differ. To make the direction available, every appointment event now carries it in its
`Header` (`Flow`); no published field changed for that. Three tests make the table impossible to forget:

- `standards_test.go` reads every `EventName()` of the domain and fails when an event type has no decision, or the table decides a type
  that no longer exists;
- `internal/architecture/standards_fitness_test.go` does the same for the types `apis/asyncapi.yaml` publishes, checks that the contract
  declares the three fields (optional) on exactly the appointment and door-closure data schemas, and that this ADR lists every event
  and every code;
- the exact-JSON goldens of ADR 0004 pin the payloads (`dock_appointment_booked`, `_rescheduled`, `_checked_in_analytics`,
  `_door_reassigned` changed, with their analytics twins; the other ten types are byte-identical to before).

### 6. Contract and surface

`apis/asyncapi.yaml` declares the three fields (optional, `nullable`, never sent as null) on every `DockAppointment*` data schema and
`read_point_gln` on the two `DoorClosure*` schemas, with an example per mapped shape, and declares the optional `gln` on the consumed
`LocationSlotRegistered`. An appointment event whose decision is "not mapped" still declares `x12_status` and `cbv_biz_step` (always
absent today) so that every appointment fact has one shape and a later mapping is not a schema change. No REST route or MCP tool exposes
the table: the AsyncAPI document and this ADR are the published surface, the MCP tool budget (ADR 0007, ADR 0012) is a hard cap, and
there is no read model of published facts to hang a route on.

## Out of scope

- An X12 / EDI translation adapter (producing a 214 or reading a 990 or 204) and an EPCIS export. Whether either becomes a
  carrier-connectivity context is decision D-Y2 and is pending the owner; this ADR only makes the semantics available on the facts so
  that such a context, if it exists, is a pure translation.
- `cbv_disposition`, and any code for a fact this table decides not to map.
- Backfilling the facts already published: the fields appear on facts recorded after the deployment.

## Consequences

- Partners can read `x12_status`, `cbv_biz_step` and `read_point_gln` without our enums. A consumer that does not know the fields keeps
  working: they are plain additions (outbound-shipping decodes `DockAppointmentBooked` by field name and ignores the rest; the analytics
  projector decodes with `DataAs`). A consumer that rejects unknown fields would break, which the fleet event standard already forbids.
- outbound-shipping pins this repository's `apis/asyncapi.yaml`; its pinned copy needs a re-pin after this merges (additive, it still
  decodes).
- Adding an event type forces a decision in `internal/domain/standards` and a row here, or the build fails.
- A wrong mapping is a published contract error: it is fixed by a new value on the optional field (the old value stays in history).
  That is why the rule is "only honest mappings" and why the unmapped decisions are written down with their reasons.
- Door GLNs published by facility-layout before this version are learned only from a later `LocationSlotRegistered` of that door.
- Rolling back the code leaves the `gln` column unused; rolling back migration `0004` drops it.

## Alternatives considered and rejected

- **Map every event to the nearest X12 code** (`CA` for a cancellation, `D1` and `CP` for a completion, `SN` for a no-show): each overstates
  what the fact says and a partner would act on it. Rejected by the honest-mapping rule.
- **Translate in a consumer or an adapter only.** Rejected for now (see out of scope): the mapping is knowledge of this context's facts,
  and a translation adapter would have to rediscover it.
- **Publish the CBV step as the short word.** Rejected: the URN is the CBV's own identifier and cannot be confused with free text.
- **Publish the fields as `null` when absent.** Rejected: it would change the bytes of every existing fact and break pinned goldens;
  omission is the fleet's additive pattern.
- **Look the door's GLN up from facility-layout when publishing.** Rejected by the fleet rule (no synchronous sibling calls); the doors
  copy already exists for this reason (ADR 0003).
- **Use the zone's or the site's GLN when the door has none.** Rejected: a read point is the place where the fact happened, and a wider
  location would be a different, wrong statement.
