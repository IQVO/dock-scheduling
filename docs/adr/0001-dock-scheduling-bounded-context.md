# 1. Dock Scheduling is a bounded context of the yard domain

Status: Accepted (bootstrap). Design source: IQVO/warehouse-docs `yard/model/model.yaml`, context `dock-scheduling`, and its yard ADRs YD-0001 to YD-0005.

## Context

The yard is modelled as two bounded contexts: dock-scheduling (door time, the future) and yard-management (the physical yard, the present). Owns the door calendar of every site: every booked time window on every dock door, inbound and outbound, from booking through arrival to door release, the no-double-booking rule, door opening hours and closures, ETA-driven risk and no-shows.

## Decision

- This repo owns the aggregates DockAppointment, DockCalendar; the invariants and error names are those of the model.
- Business decisions of the context:
- One owner of door time. Inbound and outbound appointments share one calendar per door; a door whose dockFlow is Both takes either direction.
- No two active (BOOKED or CHECKED_IN) appointments overlap on the same door. Windows are half-open, so [10:00, 12:00) and [12:00, 14:00) are compatible.
- An appointment's direction must be allowed by the door's dockFlow.
- A window lies inside the door's operating hours and outside every closure of that door.
- Arrival and door release are physical facts owned by yard-management; this context applies them from events and never records them from its own API.
- A BOOKED appointment with no arrival by window_end plus the site's no-show grace becomes NO_SHOW (sweep).
- An ETA later than window_start plus the arrival tolerance raises an advisory DockAppointmentAtRisk; nothing reschedules automatically.
- CloudEvents subdomain `wms`: types `com.warehouse.wms.dock-scheduling.<entity>.<Event>` on `warehouse.dock-scheduling.events`
  (analytics-only events on `.analytics`). Published: DockAppointmentBooked, DockAppointmentRescheduled, DockAppointmentCancelled, DockAppointmentNoShow, DockAppointmentCheckedIn, DockAppointmentDoorReassigned, DockAppointmentCompleted, DockAppointmentAtRisk, DoorClosureScheduled, DoorClosureLifted, DoorOperatingHoursDeclared.
- No live REST or MCP lookup into sibling contexts; sibling data is an event-fed local copy.
- No authentication on REST, consistent with the fleet.

## Consequences

Relationships and the full consumed-event list are in the model and in the AsyncAPI contract added with the first feature PR.
