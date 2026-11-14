# calendar_rules.feature - DockCalendar (aggregate canvas "DockCalendar", ADR 0002 A2/A3/A7/A8, D-1/D-3/D-7/D-8) and the local copies
# the calendar and the bookings read: operating hours as a replaced-whole set in the row's time zone, closures that are half-open and
# only blocked by ACTIVE appointments, settings with documented bounds, the facility-layout and CPT feeds, and the availability read
# model after each lifecycle event. door_calendar.feature pins the headline rules; these pin the boundaries behind them.
Feature: The calendar, its settings and the copies it reads obey their documented bounds

  Background:
    Given the clock is 2030-01-07T03:00:00Z
    And site WH1 has door D-BOTH taking Both flows
    And site WH1 has door D-IN taking Inbound flows
    And site WH1 has door D-OUT taking Outbound flows
    And ASN ASN-1 is registered
    And ASN ASN-2 is registered
    And ASN ASN-3 is registered
    And CPT cutoff WH1-1500 exists at site WH1

  # --- settings (DeclareCalendarSettings) -------------------------------------------------------------------------------------

  Scenario: A site that declared nothing has the defaults, no hours and no closures at version 0 (D-1)
    When a GET request is sent to "/sites/WH9/dock-calendar"
    Then the response status is 200
    And the response field "version" is 0
    And the response field "settings.maxWindowMinutes" is 240
    And the response field "settings.slotGranularityMinutes" is 15
    And the response field "settings.arrivalToleranceMinutes" is 15
    And the response field "settings.noShowGraceMinutes" is 60
    And the response list "operatingHours" has 0 items
    And the response list "closures" has 0 items

  Scenario Outline: A setting outside its range is refused as invalid-settings
    When a PUT request is sent to "/sites/WH1/dock-calendar/settings" with the JSON body {"maxWindowMinutes":<window>,"slotGranularityMinutes":<grain>,"arrivalToleranceMinutes":<tolerance>,"noShowGraceMinutes":<grace>}
    Then the response is the problem invalid-settings with status 422
    When a GET request is sent to "/sites/WH1/dock-calendar"
    Then the response field "version" is 0
    And the response field "settings.maxWindowMinutes" is 240

    Examples:
      | window | grain | tolerance | grace |
      | 0      | 15    | 15        | 60    |
      | 241    | 15    | 15        | 60    |
      | 240    | 0     | 15        | 60    |
      | 240    | 241   | 15        | 60    |
      | 240    | 15    | -1        | 60    |
      | 240    | 15    | 241       | 60    |
      | 240    | 15    | 15        | -1    |
      | 240    | 15    | 15        | 1441  |

  Scenario Outline: A setting at either end of its range is accepted and raises no event
    When a PUT request is sent to "/sites/WH1/dock-calendar/settings" with the JSON body {"maxWindowMinutes":<window>,"slotGranularityMinutes":<grain>,"arrivalToleranceMinutes":<tolerance>,"noShowGraceMinutes":<grace>}
    Then the response status is 200
    And the response field "version" is 1
    And the response field "settings.noShowGraceMinutes" is <grace>
    And 0 events have been published

    Examples:
      | window | grain | tolerance | grace |
      | 1      | 1     | 0         | 0     |
      | 240    | 240   | 240       | 1440  |

  # --- operating hours (DeclareOperatingHours, A7) ----------------------------------------------------------------------------

  Scenario: A new declaration replaces the whole set of rows, so the old site default no longer applies
    Given the PUT request to "/sites/WH1/dock-calendar/operating-hours" with the JSON body {"operatingHours":[{"weekday":"MON","opensAt":"06:00","closesAt":"22:00","timezone":"UTC"}]} has been accepted
    When an INBOUND booking is requested on door D-BOTH from 04:00 to 05:00 for ASN-1
    Then the response is the problem door-unavailable with status 409
    When the PUT request to "/sites/WH1/dock-calendar/operating-hours" with the JSON body {"operatingHours":[{"doorCode":"D-IN","weekday":"MON","opensAt":"08:00","closesAt":"16:00","timezone":"UTC"}]} has been accepted
    And the booking "reopened" is requested as INBOUND on door D-BOTH from 04:00 to 05:00 for ASN-1
    Then the response status is 201
    When an INBOUND booking is requested on door D-IN from 07:00 to 09:00 for ASN-2
    Then the response is the problem door-unavailable with status 409
    When a GET request is sent to "/sites/WH1/dock-calendar"
    Then the response list "operatingHours" has 1 items
    And the response field "operatingHours.0.doorCode" is "D-IN"

  Scenario: An empty declaration puts the site back to open 24x7
    Given the PUT request to "/sites/WH1/dock-calendar/operating-hours" with the JSON body {"operatingHours":[{"weekday":"MON","opensAt":"06:00","closesAt":"22:00","timezone":"UTC"}]} has been accepted
    And the PUT request to "/sites/WH1/dock-calendar/operating-hours" with the JSON body {"operatingHours":[]} has been accepted
    When the booking "night" is requested as INBOUND on door D-BOTH from 03:30 to 05:00 for ASN-1
    Then the response status is 201

  Scenario: Once a row applies, a weekday without a row is closed
    Given the PUT request to "/sites/WH1/dock-calendar/operating-hours" with the JSON body {"operatingHours":[{"weekday":"MON","opensAt":"06:00","closesAt":"22:00","timezone":"UTC"}]} has been accepted
    When a POST request is sent to "/appointments" with the JSON body {"direction":"INBOUND","doorCode":"D-BOTH","carrier":"ACME Freight","carrierScac":"ACMF","windowStart":"2030-01-08T10:00:00Z","windowEnd":"2030-01-08T12:00:00Z","asnNumbers":["ASN-1"]}
    Then the response is the problem door-unavailable with status 409
    When the booking "monday" is requested as INBOUND on door D-BOTH from 10:00 to 12:00 for ASN-1
    Then the response status is 201

  Scenario: closes_at may be 24:00 and a window may end at midnight
    Given the PUT request to "/sites/WH1/dock-calendar/operating-hours" with the JSON body {"operatingHours":[{"weekday":"MON","opensAt":"06:00","closesAt":"24:00","timezone":"UTC"}]} has been accepted
    When the booking "evening" is requested as INBOUND on door D-BOTH from 20:00 to 23:45 for ASN-1
    Then the response status is 201
    When a POST request is sent to "/appointments" with the JSON body {"direction":"INBOUND","doorCode":"D-IN","carrier":"ACME Freight","carrierScac":"ACMF","windowStart":"@21:00","windowEnd":"2030-01-08T00:00:00Z","asnNumbers":["ASN-2"]}
    Then the response status is 201
    When a POST request is sent to "/appointments" with the JSON body {"direction":"INBOUND","doorCode":"D-OUT","carrier":"ACME Freight","carrierScac":"ACMF","windowStart":"@22:00","windowEnd":"2030-01-08T00:15:00Z","shipmentRefs":[],"asnNumbers":["ASN-3"]}
    Then the response is the problem door-flow-mismatch with status 422

  Scenario: A window that spans two days is accepted when both days are open and refused when the second is not
    Given the PUT request to "/sites/WH1/dock-calendar/operating-hours" with the JSON body {"operatingHours":[{"weekday":"MON","opensAt":"00:00","closesAt":"24:00","timezone":"UTC"},{"weekday":"TUE","opensAt":"00:00","closesAt":"24:00","timezone":"UTC"}]} has been accepted
    When a POST request is sent to "/appointments" with the JSON body {"direction":"INBOUND","doorCode":"D-BOTH","carrier":"ACME Freight","carrierScac":"ACMF","windowStart":"@23:00","windowEnd":"2030-01-08T01:00:00Z","asnNumbers":["ASN-1"]}
    Then the response status is 201
    When the PUT request to "/sites/WH1/dock-calendar/operating-hours" with the JSON body {"operatingHours":[{"doorCode":"D-IN","weekday":"MON","opensAt":"00:00","closesAt":"24:00","timezone":"UTC"}]} has been accepted
    And a POST request is sent to "/appointments" with the JSON body {"direction":"INBOUND","doorCode":"D-IN","carrier":"ACME Freight","carrierScac":"ACMF","windowStart":"@23:00","windowEnd":"2030-01-08T01:00:00Z","asnNumbers":["ASN-2"]}
    Then the response is the problem door-unavailable with status 409

  Scenario Outline: A malformed operating-hours row is refused as invalid-operating-hours and nothing is declared
    When a PUT request is sent to "/sites/WH1/dock-calendar/operating-hours" with the JSON body {"operatingHours":[<rows>]}
    Then the response is the problem invalid-operating-hours with status 422
    And 0 events have been published

    Examples:
      | rows                                                                                                                                                             |
      | {"weekday":"MON","opensAt":"08:00","closesAt":"08:00","timezone":"UTC"}                                                                                          |
      | {"weekday":"MON","opensAt":"24:00","closesAt":"24:00","timezone":"UTC"}                                                                                          |
      | {"weekday":"MON","opensAt":"8:00","closesAt":"16:00","timezone":"UTC"}                                                                                           |
      | {"weekday":"FUNDAY","opensAt":"08:00","closesAt":"16:00","timezone":"UTC"}                                                                                      |
      | {"weekday":"MON","opensAt":"08:00","closesAt":"16:00","timezone":"Mars/Olympus"}                                                                                 |
      | {"weekday":"MON","opensAt":"08:00","closesAt":"12:00","timezone":"UTC"},{"weekday":"MON","opensAt":"13:00","closesAt":"16:00","timezone":"UTC"}                  |

  Scenario: The hours of a row are read in the row's own time zone
    Given the PUT request to "/sites/WH1/dock-calendar/operating-hours" with the JSON body {"operatingHours":[{"weekday":"MON","opensAt":"08:00","closesAt":"18:00","timezone":"America/Sao_Paulo"}]} has been accepted
    When an INBOUND booking is requested on door D-BOTH from 10:00 to 12:00 for ASN-1
    Then the response is the problem door-unavailable with status 409
    When the booking "inside" is requested as INBOUND on door D-BOTH from 11:00 to 13:00 for ASN-1
    Then the response status is 201

  Scenario: Declaring hours publishes the declaration with the calendar's version and each declaration bumps it
    When the PUT request to "/sites/WH1/dock-calendar/operating-hours" with the JSON body {"operatingHours":[{"weekday":"MON","opensAt":"06:00","closesAt":"22:00","timezone":"UTC"}]} has been accepted
    Then the latest DoorOperatingHoursDeclared fact carries site_id "WH1"
    And the latest DoorOperatingHoursDeclared fact carries version 1
    When the PUT request to "/sites/WH1/dock-calendar/operating-hours" with the JSON body {"operatingHours":[]} has been accepted
    Then the latest DoorOperatingHoursDeclared fact carries version 2
    And 2 DoorOperatingHoursDeclared events were published

  Scenario Outline: New hours may not strand an active appointment, but a finished or cancelled one does not count
    Given appointment "first" is booked INBOUND on door D-IN from 08:00 to 10:00 for ASN-1
    And appointment "first" has been brought to <state>
    When door D-IN of site WH1 opens Monday 12:00 to 14:00
    Then the response status is <status>

    Examples:
      | state      | status |
      | CHECKED_IN | 409    |
      | COMPLETED  | 200    |
      | CANCELLED  | 200    |
      | NO_SHOW    | 200    |

  Scenario: The refusal of new hours lists every stranded appointment
    Given appointment "first" is booked INBOUND on door D-IN from 08:00 to 09:00 for ASN-1
    And appointment "second" is booked INBOUND on door D-IN from 09:00 to 10:00 for ASN-2
    When door D-IN of site WH1 opens Monday 12:00 to 14:00
    Then the response is the problem hours-conflict-with-appointments with status 409
    And the problem detail names appointment "first"
    And the problem detail names appointment "second"

  # --- closures ----------------------------------------------------------------------------------------------------------------

  Scenario Outline: A closure is half-open: a booking may touch it but not enter it
    Given door D-BOTH of site WH1 is closed from 09:00 to 11:00 as "leveller"
    When an INBOUND booking is requested on door D-BOTH from <start> to <end> for ASN-1
    Then the response status is <status>

    Examples:
      | start | end   | status |
      | 07:00 | 09:00 | 201    |
      | 08:45 | 09:15 | 409    |
      | 10:45 | 11:15 | 409    |
      | 11:00 | 12:00 | 201    |
      | 09:30 | 10:00 | 409    |

  Scenario: A closure of one door leaves every other door of the site bookable
    Given door D-BOTH of site WH1 is closed from 09:00 to 11:00 as "leveller"
    When the booking "other door" is requested as INBOUND on door D-IN from 09:00 to 11:00 for ASN-1
    Then the response status is 201

  Scenario: A closure that only touches an active appointment is accepted
    Given appointment "first" is booked INBOUND on door D-BOTH from 08:00 to 10:00 for ASN-1
    When closing door D-BOTH of site WH1 from 10:00 to 12:00 is requested
    Then the response status is 201
    And appointment "first" is BOOKED

  Scenario Outline: Only an ACTIVE appointment blocks a closure
    Given appointment "first" is booked INBOUND on door D-BOTH from 08:00 to 10:00 for ASN-1
    And appointment "first" has been brought to <state>
    When closing door D-BOTH of site WH1 from 08:30 to 09:30 is requested
    Then the response status is <status>

    Examples:
      | state      | status |
      | CHECKED_IN | 409    |
      | COMPLETED  | 201    |
      | CANCELLED  | 201    |
      | NO_SHOW    | 201    |

  Scenario: The refusal of a closure lists every appointment it would cover
    Given appointment "first" is booked INBOUND on door D-BOTH from 08:00 to 09:00 for ASN-1
    And appointment "second" is booked OUTBOUND on door D-BOTH from 10:00 to 11:00 for WH1-1500
    When closing door D-BOTH of site WH1 from 08:30 to 10:30 is requested
    Then the response is the problem closure-conflicts-with-appointments with status 409
    And the problem detail names appointment "first"
    And the problem detail names appointment "second"

  Scenario Outline: A closure needs a door of the site, a span that ends after it starts and a reason of 1 to 200 characters
    When a POST request is sent to "/sites/WH1/dock-calendar/closures" with the JSON body {"doorCode":"<door>","from":"@09:00","to":"@<to>","reason":"<reason>"}
    Then the response is the problem <slug> with status 422

    Examples:
      | door    | to    | reason     | slug           |
      | D-BOTH  | 09:00 |  leveller  | invalid-closure |
      | D-BOTH  | 08:00 | leveller   | invalid-closure |
      | D-BOTH  | 10:00 |            | invalid-closure |
      | D-BOTH  | 10:00 | {text:201} | invalid-closure |
      | NOWHERE | 10:00 | leveller   | unknown-door   |

  Scenario: A closure reason of exactly 200 characters is accepted
    When a POST request is sent to "/sites/WH1/dock-calendar/closures" with the JSON body {"doorCode":"D-BOTH","from":"@09:00","to":"@11:00","reason":"{text:200}"}
    Then the response status is 201
    When a GET request is sent to "/sites/WH1/dock-calendar"
    Then the response list "closures" has 1 items

  Scenario: A lifted closure does not stand in the way of closing the same span again
    Given door D-BOTH of site WH1 is closed from 09:00 to 11:00 as "first"
    And closure "first" of site WH1 is lifted
    When closing door D-BOTH of site WH1 from 09:00 to 11:00 is requested
    Then the response status is 201
    And the published events are DoorClosureScheduled, DoorClosureLifted, DoorClosureScheduled

  Scenario: Lifting a closure that was never scheduled is a 404
    When a POST request is sent to "/sites/WH1/dock-calendar/closures/clo-00000000-0000-4000-8000-000000000000/lifts"
    Then the response is the problem unknown-closure with status 404

  Scenario: The closure facts carry the door, the span and the calendar version
    Given door D-BOTH of site WH1 is closed from 09:00 to 11:00 as "leveller"
    Then the latest DoorClosureScheduled fact carries door_code "D-BOTH"
    And the latest DoorClosureScheduled fact carries from "@09:00"
    And the latest DoorClosureScheduled fact carries to "@11:00"
    And the latest DoorClosureScheduled fact carries version 1
    When closure "leveller" of site WH1 is lifted
    Then the latest DoorClosureLifted fact carries door_code "D-BOTH"
    And the latest DoorClosureLifted fact carries version 2

  # --- the copies fed by the sibling contexts ---------------------------------------------------------------------------------

  Scenario: A decommissioned door blocks new bookings and leaves its existing appointment untouched (D-7)
    Given appointment "first" is booked INBOUND on door D-IN from 08:00 to 10:00 for ASN-1
    When facility-layout decommissions slot D-IN
    And an INBOUND booking is requested on door D-IN from 11:00 to 12:00 for ASN-2
    Then the response is the problem unknown-door with status 422
    And appointment "first" is BOOKED
    When the yard reports visit "visit-1" checked in for appointment "first" at 08:05
    And the yard reports the door release of appointment "first" at 09:30
    Then appointment "first" is COMPLETED
    When a GET request is sent to "/doors/D-IN/availability?from=@08:00&to=@12:00"
    Then the response is the problem door-not-found with status 404

  Scenario: A slot registered before its zone is held pending and bookable only once the zone arrives (A2)
    Given facility-layout registers slot D-LATE in zone zone-late with role Dock and dock flow Both
    When an INBOUND booking is requested on door D-LATE from 08:00 to 10:00 for ASN-1
    Then the response is the problem unknown-door with status 422
    When facility-layout registers zone zone-late for site WH1
    And the booking "now" is requested as INBOUND on door D-LATE from 08:00 to 10:00 for ASN-1
    Then the response status is 201
    And appointment "now" shows siteId "WH1"

  Scenario: A slot re-registered with another role is no longer a dock door
    When facility-layout registers slot D-IN in zone zone-WH1 with role Storage and dock flow Inbound
    And an INBOUND booking is requested on door D-IN from 08:00 to 10:00 for ASN-1
    Then the response is the problem unknown-door with status 422

  Scenario: A slot re-registered with another dock flow takes the new flow
    When facility-layout registers slot D-IN in zone zone-WH1 with role Dock and dock flow Outbound
    And an INBOUND booking is requested on door D-IN from 08:00 to 10:00 for ASN-1
    Then the response is the problem door-flow-mismatch with status 422
    When the booking "pickup" is requested as OUTBOUND on door D-IN from 08:00 to 10:00 for WH1-1500
    Then the response status is 201

  Scenario: A new CPT schedule replaces the whole copy and creates no appointment (D-3)
    When process-path-management publishes the cutoffs WH1-1700, WH1-1900 for site WH1
    And an OUTBOUND booking is requested on door D-OUT from 08:00 to 10:00 for WH1-1500
    Then the response is the problem missing-outbound-ref with status 422
    When the booking "evening" is requested as OUTBOUND on door D-OUT from 08:00 to 10:00 for WH1-1700
    Then the response status is 201
    When a GET request is sent to "/appointments"
    Then the response list "items" has 1 items

  Scenario: A cutoff of another site's schedule is not a cutoff of this site
    When process-path-management publishes the cutoffs WH2-1500 for site WH2
    And an OUTBOUND booking is requested on door D-OUT from 08:00 to 10:00 for WH2-1500
    Then the response is the problem missing-outbound-ref with status 422

  # --- the availability read model after each event ---------------------------------------------------------------------------

  Scenario: Availability lists what holds the time of a door in order, with its kind and reference
    Given the PUT request to "/sites/WH1/dock-calendar/operating-hours" with the JSON body {"operatingHours":[{"weekday":"MON","opensAt":"06:00","closesAt":"22:00","timezone":"UTC"}]} has been accepted
    And appointment "first" is booked INBOUND on door D-BOTH from 08:00 to 10:00 for ASN-1
    And door D-BOTH of site WH1 is closed from 12:00 to 13:00 as "leveller"
    When a GET request is sent to "/doors/D-BOTH/availability?from=@05:00&to=@14:00"
    Then the response list "busy" has 3 items
    And the response field "busy.0.kind" is "OUT_OF_HOURS"
    And the response field "busy.1.kind" is "APPOINTMENT"
    And the response field "busy.1.ref" is "{first}"
    And the response field "busy.2.kind" is "CLOSURE"
    And the response field "busy.2.ref" is "{closure:leveller}"
    And the response field "granularityMinutes" is 15
    And the free windows of door D-BOTH from 05:00 to 14:00 are 06:00-08:00, 10:00-12:00, 13:00-14:00

  Scenario Outline: A door is free again once its appointment is COMPLETED, CANCELLED or NO_SHOW but not while it is BOOKED or CHECKED_IN
    Given appointment "first" is booked INBOUND on door D-BOTH from 08:00 to 10:00 for ASN-1
    And appointment "first" has been brought to <state>
    Then the free windows of door D-BOTH from 08:00 to 10:00 are <free>

    Examples:
      | state      | free        |
      | CHECKED_IN | none        |
      | COMPLETED  | 08:00-10:00 |
      | CANCELLED  | 08:00-10:00 |
      | NO_SHOW    | 08:00-10:00 |

  Scenario: A reassignment moves the busy time from the booked door to the physical one
    Given appointment "first" is booked INBOUND on door D-BOTH from 08:00 to 10:00 for ASN-1
    When the yard reports the trailer of appointment "first" docked at door D-IN at 08:05
    Then the free windows of door D-BOTH from 08:00 to 10:00 are 08:00-10:00
    And the free windows of door D-IN from 08:00 to 10:00 are none

  Scenario: The minimum length drops the gaps that are too short
    Given appointment "first" is booked INBOUND on door D-BOTH from 08:00 to 10:00 for ASN-1
    And appointment "second" is booked INBOUND on door D-BOTH from 11:30 to 13:00 for ASN-2
    When a GET request is sent to "/doors/D-BOTH/availability?from=@06:00&to=@14:00"
    Then the response list "freeWindows" has 3 items
    When a GET request is sent to "/doors/D-BOTH/availability?from=@06:00&to=@14:00&minutes=120"
    Then the response list "freeWindows" has 1 items
    And the response field "freeWindows.0.start" is "@06:00"

  Scenario: A range of exactly 14 days is answered
    When a GET request is sent to "/doors/D-BOTH/availability?from=2030-01-07T06:00:00Z&to=2030-01-21T06:00:00Z"
    Then the response status is 200

  Scenario Outline: An availability query that is empty, reversed, too wide, unknown or aimed at an unknown door is refused
    When a GET request is sent to "<path>"
    Then the response is the problem <slug> with status <status>

    Examples:
      | path                                                                                | slug                       | status |
      | /doors/D-BOTH/availability?from=@10:00&to=@09:00                                    | invalid-availability-query | 400    |
      | /doors/D-BOTH/availability?from=@10:00&to=@10:00                                    | invalid-availability-query | 400    |
      | /doors/D-BOTH/availability?from=@06:00                                              | invalid-availability-query | 400    |
      | /doors/D-BOTH/availability?from=2030-01-07T06:00:00Z&to=2030-01-21T06:00:01Z        | invalid-availability-query | 400    |
      | /doors/D-BOTH/availability?from=@06:00&to=@09:00&minutes=0                          | invalid-query-parameter    | 400    |
      | /doors/D-BOTH/availability?from=@06:00&to=@09:00&minutes=241                        | invalid-query-parameter    | 400    |
      | /doors/D-BOTH/availability?from=@06:00&to=@09:00&color=red                          | unknown-query-parameter    | 400    |
      | /doors/NOWHERE/availability?from=@06:00&to=@09:00                                   | door-not-found             | 404    |

  # --- the appointment list ---------------------------------------------------------------------------------------------------

  Scenario Outline: A list query with an unknown parameter or an invalid value is rejected, never ignored
    When a GET request is sent to "/appointments?<query>"
    Then the response is the problem <slug> with status 400

    Examples:
      | query            | slug                    |
      | color=red        | unknown-query-parameter |
      | state=DONE       | invalid-query-parameter |
      | direction=UP     | invalid-query-parameter |
      | atRisk=perhaps   | invalid-query-parameter |
      | limit=0          | invalid-limit           |
      | limit=501        | invalid-limit           |
      | cursor=garbage   | invalid-cursor          |

  Scenario: The list filters read the appointment's own state, direction and risk
    Given appointment "late" is booked INBOUND on door D-BOTH from 09:00 to 10:00 for ASN-1
    And appointment "dropped" is booked INBOUND on door D-IN from 09:00 to 10:00 for ASN-2
    And appointment "pickup" is booked OUTBOUND on door D-OUT from 09:00 to 10:00 for WH1-1500
    And the carrier of appointment "late" reports an ETA of 09:40
    And appointment "dropped" is cancelled with reason CARRIER_REQUEST
    When a GET request is sent to "/appointments?atRisk=true"
    Then the response list "items" has 1 items
    And the response field "items.0.appointmentId" is "{late}"
    When a GET request is sent to "/appointments?state=CANCELLED"
    Then the response list "items" has 1 items
    And the response field "items.0.appointmentId" is "{dropped}"
    When a GET request is sent to "/appointments?direction=OUTBOUND"
    Then the response list "items" has 1 items
    And the response field "items.0.appointmentId" is "{pickup}"
    When a GET request is sent to "/appointments?limit=500"
    Then the response list "items" has 3 items
