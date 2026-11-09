---
id: introduction
title: Introduction
sidebar_label: Introduction
description: "What dock-scheduling owns and does not own: the door calendar of every site, its subdomain class, its capabilities and a map of this documentation."
---

# Introduction

`dock-scheduling` is the **door calendar** of the warehouse fleet. It owns every booked time window on every dock door of a site, inbound and outbound, from the booking through the carrier's arrival to the release of the door, and it keeps the one rule that makes a calendar worth having: **no two active appointments overlap on the same door.**

It is a **Supporting** subdomain of the **WMS** tier, in the yard domain (the area of the fleet around the gate, the yard and the dock doors). Its CloudEvents carry the type prefix `com.warehouse.wms.dock-scheduling.`. The service is **built on develop, registered in warehouse-infra develop, not applied to the cluster**: `terraform/apps.yaml` has a `dock-scheduling` entry, `helm-values/dock-scheduling.yaml`, a dashboard, alert rules and an image pin exist there, but no pod of it runs yet (see [Runbook](/docs/operations/runbook#registration-in-warehouse-infra)).

Source: `internal/domain/dockappointment`, `internal/domain/dockcalendar`, `internal/application/usecases`, `apis/openapi.yaml`, `apis/asyncapi.yaml`, [ADR 0001](/docs/adr/0001-dock-scheduling-bounded-context).

## The problem it solves

A site has few dock doors and every carrier that loads or unloads needs one. Without a calendar carriers queue at the gate, doors idle between visits and dock labour stands by. `dock-scheduling` supplies the calendar: who is expected at which door, in which window, in which direction (`INBOUND` or `OUTBOUND`), and what happens when reality departs from the plan (a late ETA, a no-show, a trailer that docks at another door).

## What it owns

- **`DockAppointment`** aggregate: a booked window on a door for one carrier visit, with its state (`BOOKED`, `CHECKED_IN`, `COMPLETED`, `CANCELLED`, `NO_SHOW`), version, ETA and at-risk flag.
- **`DockCalendar`** aggregate (one per site): booking settings (maximum window, slot granularity, arrival tolerance, no-show grace), door operating hours (no rows means open 24x7) and door closures.
- The **no-double-booking** rule, per door, enforced under a per-door lock.
- **Direction fits the door**: the appointment's direction must be allowed by the door's dock flow.
- **Hours and closures**: a window must lie inside the door's operating hours and outside its closures.
- **ETA risk**: an ETA later than window start plus the arrival tolerance flags the appointment at risk. It is advisory; nothing reschedules automatically.
- **No-show**: a `BOOKED` appointment with no arrival by window end plus the no-show grace becomes `NO_SHOW`, driven by a Kubernetes CronJob that calls `POST /no-show-sweeps`.
- Event-fed **local copies** of other contexts' facts, kept only to validate bookings: zones and doors (facility-layout), ASNs (inbound-receiving) and the CPT cutoff schedule (process-path-management).
- A **schedule-adherence analytics read side** (projector and reports binaries over a separate analytical database).

## What it does not own

- **The present of the yard.** Gate visits, trailer position, docking, door work and holds belong to `yard-management`. Arrival and door release are physical facts: this context **applies** them from `VisitCheckedIn`, `TrailerDocked`, `TrailerUndocked` and `VisitRejected` events and never records them from its own API. When the physical fact contradicts the calendar (a trailer docks at another door) the physical fact wins, even if that creates an overlap.
- **ASNs and receipts** (`inbound-receiving`), **doors and zones** (`facility-layout`), **the CPT schedule** (`process-path-management`). It keeps read copies only.
- **Outbound shipment planning**: an outbound appointment carries a `cptId` or shipment references and nothing more. Outbound pickups planned from the CPT schedule are not built; an outbound appointment is booked by a caller.
- **ETA feeds**: ETAs enter through the REST route (and the MCP tool); no feed context exists.
- **Automatic rescheduling, detention invoicing, driver data.** None.
- **Authentication.** REST and MCP are unauthenticated fleet-wide; access control is the cluster boundary.

## Main capabilities

| Capability | Entry point |
|---|---|
| Book, reschedule, cancel an appointment; report an ETA | REST `/appointments...`, MCP tools |
| See free and busy windows of a door | `GET /doors/{doorCode}/availability`, MCP `get_door_availability` |
| Declare operating hours and booking settings, schedule and lift closures | REST `/sites/{siteId}/dock-calendar...` |
| Sweep no-shows | `POST /no-show-sweeps` (CronJob) |
| Apply yard, facility, ASN and CPT facts | Kafka consumers inside `cmd/api` |
| Publish the appointment lifecycle and closures | transactional outbox to `warehouse.dock-scheduling.events` |
| Schedule-adherence report and freshness | `cmd/reports` (`/reports/schedule-adherence`, `/reports/freshness`) |

## Where to go next

| You want to | Read |
|---|---|
| See the layout and the six binaries | [Architecture](/docs/overview/architecture) |
| Run it locally | [Quickstart](/docs/overview/quickstart) |
| Configure it | [Configuration](/docs/operations/configuration) |
| Deploy and operate it | [Runbook](/docs/operations/runbook), [Observability](/docs/operations/observability), [Troubleshooting](/docs/operations/troubleshooting), [Before the first release](/docs/operations/before-first-release) |
| Test it | [Testing](/docs/development/testing) |
| Understand the domain | [Ubiquitous language](/docs/ddd/ubiquitous-language), [Use cases](/docs/ddd/use-cases), [Subdomain classification](/docs/ddd/subdomain-classification), [DDD artifacts](/docs/ddd/ddd-artifacts) |
| Call it | [API reference](/docs/api-reference), [Events](/docs/api-reference/events), [MCP tools](/docs/mcp/tools) |
| See its neighbours | [Integration](/docs/ecosystem/integration) |
| Know why it is built this way | [Architecture decision records](/docs/adr) |
