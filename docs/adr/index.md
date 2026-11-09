---
title: Architecture decision records
sidebar_label: Index
sidebar_position: 0
slug: /
---

# Architecture decision records

Each status below is copied from the ADR body's own `Status:` line, which is the authority; ADR bodies are not edited after
acceptance except to correct a wording the later ADR that supersedes it names (ADR 0002 A15 now says L3, per ADR 0007).

| ADR | Title | Status |
| --- | --- | --- |
| [0001](./0001-dock-scheduling-bounded-context.md) | Dock Scheduling is a bounded context of the yard domain | Accepted (bootstrap) |
| [0002](./0002-aggregates-invariants-and-assumptions.md) | Aggregates, invariants, error names and assumptions | Accepted |
| [0003](./0003-consumed-contracts-and-local-copies.md) | Consumed contracts and local copies | Accepted |
| [0004](./0004-cloudevents-catalogue.md) | CloudEvents catalogue | Accepted |
| [0005](./0005-postgres-persistence-and-outbox.md) | Postgres persistence and the transactional outbox | Accepted |
| [0006](./0006-analytics-read-side.md) | Analytics read side: an analytics stream, a projector and a schedule-adherence report over a separate analytical database | Accepted (2026-11-06) |
| [0007](./0007-mcp-server.md) | MCP server | Accepted |
| [0008](./0008-rest-adapter-and-api-composition-root.md) | REST adapter and the API composition root | Accepted |
| [0009](./0009-kafka-consumers.md) | Kafka consumers | Accepted |
| [0010](./0010-door-reassigned-is-an-integration-event.md) | DockAppointmentDoorReassigned is an integration event | Accepted (2026-11-09) |
