# 4. CloudEvents catalogue

Status: Accepted. Every message is CloudEvents 1.0 structured JSON built by `internal/adapters/kafka/cloudevents`:
`source` `/warehouse/dock-scheduling`, `subject` = aggregate id (appointment id, site id for calendar events, SCAC for carrier events), `time` = the
domain instant (UTC), `datacontenttype` `application/json`, `dataschema` `urn:warehouse:dock-scheduling:events:<EventName>:v1`
(`urn:warehouse:dock-scheduling:analytics:<EventName>:v1` on the analytics stream). Kafka key = subject. Topics
`warehouse.dock-scheduling.events` (integration) and `warehouse.dock-scheduling.analytics` (this service's read side; every
published fact appears there too, with the same id and type). Payload `data` is snake_case, times RFC 3339 UTC. Subdomain `wms`.

One `type:` line per published type and stream:

| Event | Stream | Consumers | `type` |
|---|---|---|---|
| DockAppointmentBooked | events | yard-management, warehouse-planning, inbound-receiving | type: com.warehouse.wms.dock-scheduling.dockappointment.DockAppointmentBooked |
| DockAppointmentRescheduled | events | yard-management, warehouse-planning | type: com.warehouse.wms.dock-scheduling.dockappointment.DockAppointmentRescheduled |
| DockAppointmentCancelled | events | yard-management, warehouse-planning, inbound-receiving | type: com.warehouse.wms.dock-scheduling.dockappointment.DockAppointmentCancelled |
| DockAppointmentNoShow | events | yard-management, warehouse-planning, exception-management | type: com.warehouse.wms.dock-scheduling.dockappointment.DockAppointmentNoShow |
| DockAppointmentCheckedIn | analytics only | analytics only | type: com.warehouse.wms.dock-scheduling.dockappointment.DockAppointmentCheckedIn |
| DockAppointmentDoorReassigned | events (per ADR 0010) | inbound-receiving | type: com.warehouse.wms.dock-scheduling.dockappointment.DockAppointmentDoorReassigned |
| DockAppointmentCompleted | analytics only | analytics only | type: com.warehouse.wms.dock-scheduling.dockappointment.DockAppointmentCompleted |
| DockAppointmentAtRisk | analytics only | analytics only | type: com.warehouse.wms.dock-scheduling.dockappointment.DockAppointmentAtRisk |
| DoorClosureScheduled | events (key site_id) | yard-management | type: com.warehouse.wms.dock-scheduling.dockcalendar.DoorClosureScheduled |
| DoorClosureLifted | events (key site_id) | yard-management | type: com.warehouse.wms.dock-scheduling.dockcalendar.DoorClosureLifted |
| DoorOperatingHoursDeclared | analytics only (key site_id) | analytics only | type: com.warehouse.wms.dock-scheduling.dockcalendar.DoorOperatingHoursDeclared |
| CarrierRegistered | events (key scac, per ADR 0012) | yard-management (the carrier registry copy, ADR 0016 / YF-1) | type: com.warehouse.wms.dock-scheduling.carrier.CarrierRegistered |
| CarrierSuspended | events (key scac, per ADR 0012) | yard-management (the carrier registry copy, ADR 0016 / YF-1) | type: com.warehouse.wms.dock-scheduling.carrier.CarrierSuspended |
| CarrierReinstated | events (key scac, per ADR 0012) | yard-management (the carrier registry copy, ADR 0016 / YF-1) | type: com.warehouse.wms.dock-scheduling.carrier.CarrierReinstated |

Analytics-only events go ONLY to `warehouse.dock-scheduling.analytics`. The version of an appointment event is the aggregate
version after the change; calendar events carry the DockCalendar version; carrier events (ADR 0012, subject and key = the SCAC) carry the Carrier version. DockAppointmentAtRisk has no `version` (it is advisory
and does not change the aggregate beyond the ETA report that caused it).

Data fields per event are the AsyncAPI schemas in `apis/asyncapi.yaml` and the golden JSON files in
`internal/adapters/outbound/kafka/testdata`. Locations are not nested objects in these payloads: `door_code` is the facility-layout
location code.

Consumed types are catalogued in ADR 0003. A breaking payload change is a new `.v2` type with a `:v2` dataschema.
