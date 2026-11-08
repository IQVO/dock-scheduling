# yard_facts.feature - the physical facts yard-management owns (ArrivalFromGate, ActualDoorWins, CompleteOnDoorRelease,
# CancelOnVisitRejected, OutOfOrderYardFacts, X6). The facts are keyed by visit or trailer, not by appointment, so they can arrive in any
# order across partitions: a door release applied to a BOOKED appointment completes it with an implicit arrival, a late check-in for a
# COMPLETED appointment is ignored, and any yard fact for a CANCELLED or NO_SHOW appointment is ignored and counted.
Feature: Yard facts are applied in any order

  Background:
    Given the clock is 2030-01-07T08:30:00Z
    And site WH1 has door D-BOTH taking Both flows
    And site WH1 has door D-IN taking Inbound flows
    And ASN ASN-1 is registered
    And ASN ASN-2 is registered
    And appointment "first" is booked INBOUND on door D-BOTH from 09:00 to 11:00 for ASN-1

  Scenario: Check-in then door release walks the appointment to COMPLETED
    When the yard reports visit "visit-1" checked in for appointment "first" at 09:05
    Then appointment "first" is CHECKED_IN
    And appointment "first" has arrival status ON_TIME
    When the yard reports the door release of appointment "first" at 10:30
    Then appointment "first" is COMPLETED
    And the published events are DockAppointmentBooked, DockAppointmentCheckedIn, DockAppointmentCompleted

  Scenario Outline: The arrival status comes from the window and the site's tolerance (X6)
    When the yard reports visit "visit-1" checked in for appointment "first" at <arrival>
    Then appointment "first" has arrival status <status>

    Examples:
      | arrival | status  |
      | 08:44   | EARLY   |
      | 08:45   | ON_TIME |
      | 09:15   | ON_TIME |
      | 09:16   | LATE    |

  Scenario: A door release that outruns the gate check-in completes the appointment with an implicit arrival
    When the yard reports the door release of appointment "first" at 10:30
    Then appointment "first" is COMPLETED
    And appointment "first" has arrival status ON_TIME implicitly
    And 1 implicit arrival was counted
    And the published events are DockAppointmentBooked, DockAppointmentCompleted

  Scenario: The late check-in of a completed appointment is ignored
    Given the yard reports the door release of appointment "first" at 10:30
    When the yard reports visit "visit-1" checked in for appointment "first" at 09:05
    Then appointment "first" is COMPLETED
    And the ignored-fact count for terminal_appointment is 1
    And the published events are DockAppointmentBooked, DockAppointmentCompleted

  Scenario: Yard facts for a cancelled appointment are ignored and counted
    Given appointment "first" is cancelled with reason CARRIER_REQUEST
    When the yard reports visit "visit-1" checked in for appointment "first" at 09:05
    And the yard reports the door release of appointment "first" at 10:30
    Then appointment "first" is CANCELLED
    And the ignored-fact count for terminal_appointment is 2
    And the published events are DockAppointmentBooked, DockAppointmentCancelled

  Scenario: A fact for another site's appointment is ignored
    Given site WH2 has door D-OTHER taking Both flows
    When the yard of site WH2 reports a door release naming appointment "first"
    Then appointment "first" is BOOKED
    And the ignored-fact count for site_mismatch is 1

  Scenario: The physical door wins and flags the appointment it now overlaps (ActualDoorWins)
    Given appointment "other" is booked INBOUND on door D-IN from 10:00 to 12:00 for ASN-2
    When the yard reports the trailer of appointment "first" docked at door D-IN at 09:10
    Then appointment "first" is on door D-IN
    And appointment "other" is at risk because DOOR_REASSIGNED_OVERLAP
    And appointment "other" is BOOKED
    And door D-IN has 2 active appointments

  Scenario: A late ETA flags the appointment and an on-time ETA clears it (EtaRisk)
    When the carrier of appointment "first" reports an ETA of 09:40
    Then the response status is 201
    And appointment "first" is at risk because LATE_ETA
    When the carrier of appointment "first" reports an ETA of 09:05
    Then appointment "first" is not at risk

  Scenario: A rejected visit cancels a BOOKED appointment as TURNED_AWAY (X2)
    When the yard turns the carrier of appointment "first" away at the gate
    Then appointment "first" is CANCELLED
    And appointment "first" was cancelled for reason TURNED_AWAY
    And door D-BOTH has 0 active appointments

  Scenario: A rejection after the carrier checked in changes nothing
    Given the yard reports visit "visit-1" checked in for appointment "first" at 09:05
    When the yard turns the carrier of appointment "first" away at the gate
    Then appointment "first" is CHECKED_IN
    And the ignored-fact count for already_applied is 1
