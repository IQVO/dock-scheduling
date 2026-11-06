# door_booking.feature - the no-double-booking rule (ADR 0002): no two ACTIVE (BOOKED or CHECKED_IN) appointments overlap on one door,
# whatever their direction. Windows are half-open, so [08:00, 10:00) and [10:00, 12:00) are compatible. A door taking Both flows
# is one calendar for inbound and outbound together. Every scenario drives the REST router.
Feature: A door is booked by one appointment at a time

  Background:
    Given the clock is 2030-01-07T06:00:00Z
    And site WH1 has door D-BOTH taking Both flows
    And site WH1 has door D-IN taking Inbound flows
    And ASN ASN-1 is registered
    And ASN ASN-2 is registered
    And CPT cutoff WH1-1500 exists at site WH1

  Scenario: An outbound window cannot overlap an inbound one on a Both door
    Given appointment "inbound" is booked INBOUND on door D-BOTH from 08:00 to 10:00 for ASN-1
    When an OUTBOUND booking is requested on door D-BOTH from 09:00 to 11:00 for WH1-1500
    Then the response is the problem door-double-booked with status 409
    And door D-BOTH has 1 active appointment
    And the published events are DockAppointmentBooked

  Scenario: An inbound window cannot overlap an outbound one on a Both door
    Given appointment "outbound" is booked OUTBOUND on door D-BOTH from 08:00 to 10:00 for WH1-1500
    When an INBOUND booking is requested on door D-BOTH from 07:00 to 08:15 for ASN-1
    Then the response is the problem door-double-booked with status 409
    And door D-BOTH has 1 active appointment

  Scenario: Back-to-back windows of both directions are compatible
    Given appointment "outbound" is booked OUTBOUND on door D-BOTH from 08:00 to 10:00 for WH1-1500
    When the booking "inbound" is requested as INBOUND on door D-BOTH from 10:00 to 12:00 for ASN-1
    Then the response status is 201
    And door D-BOTH has 2 active appointments
    And the free windows of door D-BOTH from 08:00 to 14:00 are 12:00-14:00

  Scenario: A door that takes one direction refuses the other
    When an OUTBOUND booking is requested on door D-IN from 08:00 to 10:00 for WH1-1500
    Then the response is the problem door-flow-mismatch with status 422
    And door D-IN has 0 active appointments
    And 0 events have been published

  Scenario: An unknown door is refused
    When an INBOUND booking is requested on door NOWHERE from 08:00 to 10:00 for ASN-1
    Then the response is the problem unknown-door with status 422

  Scenario: Cancelling an appointment frees its window for the other direction
    Given appointment "inbound" is booked INBOUND on door D-BOTH from 08:00 to 10:00 for ASN-1
    When appointment "inbound" is cancelled with reason CARRIER_REQUEST
    Then the response status is 200
    And appointment "inbound" is CANCELLED
    And appointment "inbound" was cancelled for reason CARRIER_REQUEST
    When the booking "outbound" is requested as OUTBOUND on door D-BOTH from 09:00 to 11:00 for WH1-1500
    Then the response status is 201
    And door D-BOTH has 1 active appointment
    And the published events are DockAppointmentBooked, DockAppointmentCancelled, DockAppointmentBooked

  Scenario: A reschedule cannot move onto another appointment's window
    Given appointment "first" is booked INBOUND on door D-BOTH from 08:00 to 10:00 for ASN-1
    And appointment "second" is booked OUTBOUND on door D-BOTH from 10:00 to 12:00 for WH1-1500
    When appointment "second" is rescheduled to 09:00-11:00
    Then the response is the problem door-double-booked with status 409
    And appointment "second" runs from 10:00 to 12:00
    And appointment "second" is at version 1

  Scenario: A reschedule into a free window keeps the appointment BOOKED and bumps its version
    Given appointment "first" is booked INBOUND on door D-BOTH from 08:00 to 10:00 for ASN-1
    When appointment "first" is rescheduled to 09:00-11:00
    Then the response status is 200
    And appointment "first" runs from 09:00 to 11:00
    And appointment "first" is at version 2
    And appointment "first" is BOOKED
    And the published events are DockAppointmentBooked, DockAppointmentRescheduled

  Scenario: A reschedule may move the appointment to another door (D-4)
    Given appointment "first" is booked INBOUND on door D-BOTH from 08:00 to 10:00 for ASN-1
    When appointment "first" is rescheduled to door D-IN from 08:00 to 10:00
    Then the response status is 200
    And appointment "first" is on door D-IN
    And door D-BOTH has 0 active appointments
    And door D-IN has 1 active appointment

  Scenario: A reschedule to a door that takes the wrong direction is refused
    Given appointment "outbound" is booked OUTBOUND on door D-BOTH from 08:00 to 10:00 for WH1-1500
    When appointment "outbound" is rescheduled to door D-IN from 08:00 to 10:00
    Then the response is the problem door-flow-mismatch with status 422
    And appointment "outbound" is on door D-BOTH
