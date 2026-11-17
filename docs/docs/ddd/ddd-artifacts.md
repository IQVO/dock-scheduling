---
id: ddd-artifacts
title: DDD artifacts
sidebar_label: DDD artifacts
description: "The ddd-crew artifact pack of dock-scheduling: core domain chart, bounded context canvas, context map, aggregate design canvas, message flows, EventStorming and the diagrams of the built service."
---

# DDD artifacts

The strategic and tactical design of `dock-scheduling` follows the [ddd-crew](https://github.com/ddd-crew) tools. Each page below is checked against the code of this repository: every Mermaid diagram carries a `Source:` line naming the files it was drawn from, and where the yard design model in the central documentation site and the code differ, the code wins and the page says so.

Source: `internal/domain/`, `internal/application/`, `internal/adapters/`, `migrations/`, `apis/`, [ADR 0001](/docs/adr/0001-dock-scheduling-bounded-context) and [ADR 0002](/docs/adr/0002-aggregates-invariants-and-assumptions).

| Page | What it holds | ddd-crew tool |
| --- | --- | --- |
| [Ubiquitous language](/docs/ddd/ubiquitous-language) | Dock appointment, door window, door calendar, closure, arrival status, no-show, SCAC, CPT and the neighbour terms, with the Go identifier of each | glossary |
| [Core domain chart](/docs/ddd/core-domain-chart) | Why the context is Supporting, with its internal slices | [core-domain-charts](https://github.com/ddd-crew/core-domain-charts) |
| [Bounded context canvas](/docs/ddd/bounded-context-canvas) | Purpose, classification, roles, every inbound and outbound message, assumptions | [bounded-context-canvas](https://github.com/ddd-crew/bounded-context-canvas) |
| [Context map](/docs/ddd/context-map) | Every upstream and downstream neighbour with its pattern and channel | [context-mapping](https://github.com/ddd-crew/context-mapping) |
| [Aggregate design canvas](/docs/ddd/aggregate-design-canvas) | `DockAppointment` and `DockCalendar`: states, invariants, commands, events | [aggregate-design-canvas](https://github.com/ddd-crew/aggregate-design-canvas) |
| [Domain message flow](/docs/ddd/domain-message-flow) | Booking, arrival to completion, late ETA and no-show, closure and ASN cancellation | [domain-message-flow-modelling](https://github.com/ddd-crew/domain-message-flow-modelling) |
| [EventStorming](/docs/ddd/eventstorming) | Process-level boards with hotspots and how each was settled | [eventstorming-glossary-cheat-sheet](https://github.com/ddd-crew/eventstorming-glossary-cheat-sheet) |
| [Class diagram](/docs/ddd/class-diagram) | Domain types and the hexagonal ports and adapters | UML |
| [Entity relationship](/docs/ddd/entity-relationship) | The Postgres schema (OLTP and analytics) | ER |
| [Sequence diagrams](/docs/ddd/sequence-diagrams) | One runtime sequence per command use case | UML |
| [Domain events](/docs/ddd/domain-events) | The fourteen published and ten consumed events with full CloudEvents types | event catalogue |

Also in this section: [Use cases](/docs/ddd/use-cases) (one section per command: trigger, inputs, invariants, errors, events) and [Subdomain classification](/docs/ddd/subdomain-classification).

## In numbers (from the code)

| Item | Count | Where |
| --- | --- | --- |
| Aggregates | 2: `DockAppointment` (5 states: `BOOKED`, `CHECKED_IN`, `COMPLETED`, `CANCELLED`, `NO_SHOW`), `DockCalendar` | [Aggregate design canvas](/docs/ddd/aggregate-design-canvas) |
| Command use cases | 16 (eight on `DockAppointment`, four on `DockCalendar`, four on `Carrier`) | [Use cases](/docs/ddd/use-cases) |
| Published events | 14 (10 integration plus analytics twin, 4 analytics only) | [Events](/docs/api-reference/events) |
| Consumed events | 10 from four upstream topics | [Events](/docs/api-reference/events) |
| REST routes | 13 business routes plus `/healthz`, `/readyz`, `/metrics` on the API; 2 report routes on the reports binary | [API reference](/docs/api-reference) |
| ADRs | 12 | [ADR index](/docs/adr) |
