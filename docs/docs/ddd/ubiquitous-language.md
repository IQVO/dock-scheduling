---
id: ubiquitous-language
title: Ubiquitous Language
sidebar_label: Ubiquitous Language
description: "The terms Dock Scheduling speaks (dock appointment, door window, door calendar, closure, arrival status, no-show, SCAC, CPT) and the neighbour terms it uses, each with its Go identifier in the built service."
---

# Ubiquitous Language

Code identifiers are the Go names from the design model's `ul` list (package
and type), checked against `internal/domain` of the built service; `consumed` marks a term that this context only reads from an event.
The "Not to be confused with" column is this page's reading, added to keep
neighbouring terms apart; it is not in the design model.

## This context's terms

From `contexts[dock-scheduling].ul`.

| Term | Definition | Code identifier | Not to be confused with |
| --- | --- | --- | --- |
| **Dock Appointment** | A booked window on one door for one carrier visit, inbound or outbound. | `dockappointment.DockAppointment` | **Yard Visit** (`yard-management`: the physical passage through the gate, with the arrival and departure that really happened) |
| **Door Window** | The half-open interval `[start, end)` an appointment holds on its door. | `dockappointment.Window` | **Operating hours** (when the door is open at all) |
| **Door Calendar** | A site's door opening hours, closures and booking settings. | `dockcalendar.DockCalendar` | **Dock Appointment** (a booking made against the calendar) |
| **Closure** | A period a door cannot be booked (maintenance, damage). | `dockcalendar.Closure` | **No-Show** (a carrier that did not come, not a door that cannot be used) |
| **Arrival Status** | `EARLY`, `ON_TIME` or `LATE`, from the gate arrival against the window and the tolerance. | `dockappointment.ArrivalStatus` | **Appointment state** (`BOOKED`, `CHECKED_IN` and the rest describe the lifecycle, not punctuality) |
| **No-Show** | A booked appointment whose carrier did not arrive by window end plus grace. | `dockappointment.NoShow` (the state `NO_SHOW`, event `DockAppointmentNoShow`) | **Cancelled** (a decision by the carrier or the site, with a `CancellationReason`) |
| **SCAC** | Standard Carrier Alpha Code identifying the carrier. | `shared.Carrier` | **Carrier name** (free text, at most 100 characters) |
| **CPT** | Critical pull time: the fleet's outbound cutoff, which the network uses as its pickup cutoff. | consumed from `process-path-management` | **Window end** (the end of one appointment, not a site-wide cutoff) |

## Value objects and states of the aggregates

From `aggregates[DockAppointment]` and `aggregates[DockCalendar]`; these are
terms the design model uses but does not list in `ul`.

| Term | Definition | Code identifier |
| --- | --- | --- |
| **Direction** | `INBOUND` or `OUTBOUND`; must be allowed by the door's `dockFlow`. | `Direction(INBOUND or OUTBOUND)` |
| **Appointment state** | `BOOKED`, `CHECKED_IN`, `COMPLETED`, `CANCELLED`, `NO_SHOW`. | `DockAppointment` states |
| **Carrier** | The carrier's name and optional SCAC. | `Carrier` with `name` and `scac` |
| **Inbound refs** | The ASN numbers an inbound appointment covers. | `InboundRefs` with `asn_numbers` |
| **Outbound refs** | The `cpt_id` and the shipment references an outbound appointment covers. | `OutboundRefs` with `cpt_id` and `shipment_refs` |
| **ETA** | The estimated arrival: `eta_at`, `reported_at` and `source`. | `Eta` |
| **Expected seal** | The seal number the carrier declares when booking; `yard-management` compares it at the gate. | `ExpectedSeal` with `seal_number` |
| **Operating hours** | Opening and closing times by weekday and time zone, per door or as the site default. | `OperatingHours` |
| **Calendar settings** | `max_window_minutes` (240), `slot_granularity_minutes` (15), `arrival_tolerance_minutes`, `no_show_grace_minutes`. | `Settings` |
| **Arrival tolerance** | The site's allowed deviation around the window start, used for `ArrivalStatus` and for the ETA risk test. | `arrival_tolerance_minutes` |
| **No-show grace** | The time after `window_end` before the sweep marks a `BOOKED` appointment `NO_SHOW`. | `no_show_grace_minutes` |
| **MaxWindow** | The longest window, 4 hours (`ErrInvalidWindow`); a site may lower it, never raise it. | `dockappointment.HardMaxWindow`, `dockappointment.Rules` |
| **Cancellation reason** | `CARRIER_REQUEST`, `SITE_REQUEST`, `ASN_CANCELLED`, `TURNED_AWAY`. | `dockappointment.Reason` |
| **At-risk cause** | `LATE_ETA` or `DOOR_REASSIGNED_OVERLAP`. | `dockappointment.AtRiskCause` |
| **Provenance** | Where an appointment came from; the design model names `legacy-import`. | `dockappointment.Provenance` (`native` or `legacy-import`) |

## Neighbour terms used here

| Term | Definition (owner) | Code identifier |
| --- | --- | --- |
| **Yard** | The enclosed area outside a site's four walls: gate, parking spots and dock doors (`yard-management`). | none |
| **Yard Visit** | One tractor and driver passing the gate, from check-in to check-out (`yard-management`). | `yardvisit.Visit` |
| **Trailer** | Any towable unit on site, including a container on a chassis (`yard-management`). | `trailer.Trailer` |
| **Dock Door** | A Dock location (`facility-layout` role Dock) with a `dockFlow` of Inbound, Outbound or Both (`yard-management`'s term; this context's `DoorView` holds the same slots). | `shared.Location` |
| **Seal** | The numbered seal on a trailer's doors; `INTACT`, `BROKEN` or `NONE` (`yard-management`). | `trailer.SealState` |
| **Detention** | Carrier waiting time charged beyond the agreed free time; the free time is a site setting in `yard-management` (`yard-management`). | `yardpolicy.Policy` |
| **dockFlow** | `Inbound`, `Outbound` or `Both` on a dock slot (`facility-layout`). | field of `LocationSlotRegistered` |
| **Zone and siteCode** | A zone belongs to a site: `ZoneRegistered` carries `zoneId` and `siteCode`; a door reaches its site only through its `zoneId` (`facility-layout`). | field of `ZoneRegistered` |
| **ASN** | The advance shipping notice an inbound appointment covers, identified by `asn_number` (`inbound-receiving`). | field `asn_number` |

No fleet translation table: this context uses the same names as the fleet.
The one rename to remember is ownership: the `DockAppointment` that
`inbound-receiving` used to own is this context's `DockAppointment`
(YD-0002), with the same event names and a superset of the payload fields.
