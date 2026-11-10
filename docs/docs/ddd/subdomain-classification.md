---
id: subdomain-classification
title: Subdomain classification
sidebar_label: Subdomain classification
description: "dock-scheduling is a Supporting subdomain of the WMS tier: the reasoning, what would change it, and the classification of every neighbour."
---

# Subdomain classification

**`dock-scheduling` is a Supporting subdomain**, in the **WMS** tier (the `wms` segment of its CloudEvents types, `com.warehouse.wms.dock-scheduling.*`), area **WMS** of the fleet, in the yard domain. On the Core Domain Chart it sits at differentiation 0.40 and complexity 0.45 (quadrant 3); its evolution stage is Product. The chart is on [Core Domain Chart](/docs/ddd/core-domain-chart); the full canvas is on [Bounded Context Canvas](/docs/ddd/bounded-context-canvas).

Source: the yard design model's classification in warehouse-docs (`yard/docs/strategic-design/subdomain-classification.md` on `origin/main`), checked against `internal/domain/` and `internal/application/usecases/` in this repository.

## Reasoning

- **Not Generic.** A door calendar is needed by every site with trucks, but the rules are the fleet's own and differ per operator: window length and slot grid, arrival tolerance, no-show grace, operating hours per door, one calendar for both directions on a door that takes both, the physical-fact-wins rule when a trailer docks elsewhere. They are encoded in two aggregates (`DockAppointment` with five states, `DockCalendar`) and a domain service (`DoorSchedule`) with a per-door lock, not in an off-the-shelf product.
- **Not Core.** The fleet does not win customers by how it books a door. What the context protects is a cost (carrier wait, detention, idle dock labour) and the on-time arrival of inbound stock. The rules are fixed and small: overlap, flow, hours and closures, ETA risk and no-show are all deterministic and advisory (nothing reschedules automatically), and the competitive core of the platform stays in the WES conductor and task lifecycle contexts.
- **Therefore Supporting**, with the single most differentiating slice being the no-overlap booking rule over a door's whole calendar, enforced twice (a domain check under an advisory lock, and a database exclusion constraint as backstop).
- **What would change it.** Automatic rescheduling from ETA feeds, pickup booking planned from the CPT schedule (`PickupFromCutoffs`, phase 2, not built) or detention pricing would move it up; replacing it by a vendor yard system behind an anti-corruption layer would move it down.

## Neighbours

Copied from the central documentation site (warehouse-docs `origin/main`): the yard instance for the yard contexts and the main strategic-design page for the rest.

| Neighbour | Classification | Relationship to this context |
| --- | --- | --- |
| `yard-management` | Supporting | Customer/Supplier both ways: sends the physical facts (`VisitCheckedIn`, `VisitRejected`, `TrailerDocked`, `TrailerUndocked`), consumes appointments and closures. |
| `facility-layout` | Generic | Conformist upstream: zones and dock slots (with dock flow). |
| `inbound-receiving` | Supporting | Conformist upstream for ASNs; downstream for the appointment lifecycle and door reassignment; handed the appointment over (strangler). |
| `process-path-management` | Generic | Conformist upstream: the CPT schedule. |
| `warehouse-planning` | Core | Downstream: appointments as inbound labour demand. |
| `exception-management` | Supporting | Downstream: opens a case on a no-show. |
| `outbound-shipping` | Supporting | Downstream: reads `DockAppointmentBooked`. |

Deferred contexts named by the yard model and not built: `transport-visibility` (Generic, would feed ETAs) and `yard-billing` (Generic, detention charges).
