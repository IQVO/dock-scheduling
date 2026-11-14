# yard_rules.feature - the facts yard-management owns (use-cases.md RecordArrival, ReassignDoor, RecordDoorRelease; policies
# ArrivalFromGate, ActualDoorWins, CompleteOnDoorRelease, OutOfOrderYardFacts, CancelOnVisitRejected). The consumers are idempotent
# on the CloudEvents id (inbox), keyed by visit or trailer so they arrive in any order, and refuse by IGNORING AND COUNTING. The
# earlier yard_facts.feature walks the happy order; these pin redelivery, every ignored fact and the site's own tolerance.
Feature: Yard facts are applied once, ignored when they cannot apply and never recorded over REST

  Background:
    Given the clock is 2030-01-07T06:00:00Z
    And site WH1 has door D-BOTH taking Both flows
    And site WH1 has door D-IN taking Inbound flows
    And ASN ASN-1 is registered
    And ASN ASN-2 is registered
    And ASN ASN-3 is registered
    And ASN ASN-4 is registered
    And ASN ASN-5 is registered

  # --- redelivery (inbox on the CloudEvents id) ------------------------------------------------------------------------------

  Scenario: A check-in redelivered under the same CloudEvents id is applied once and not even counted
    Given appointment "first" is booked INBOUND on door D-BOTH from 09:00 to 11:00 for ASN-1
    When the yard delivers event "evt-gate-1" reporting visit "visit-1" checked in for appointment "first" at 09:05
    And the yard redelivers event "evt-gate-1"
    Then appointment "first" is CHECKED_IN
    And appointment "first" is at version 2
    And 1 DockAppointmentCheckedIn events were published
    And the ignored-fact count for already_applied is 0
    And the ignored-fact count for terminal_appointment is 0

  Scenario: A second check-in under another id finds the appointment already CHECKED_IN, leaves it and counts it
    Given appointment "first" is booked INBOUND on door D-BOTH from 09:00 to 11:00 for ASN-1
    When the yard delivers event "evt-gate-1" reporting visit "visit-1" checked in for appointment "first" at 09:05
    And the yard delivers event "evt-gate-2" reporting visit "visit-2" checked in for appointment "first" at 09:40
    Then appointment "first" is CHECKED_IN
    And appointment "first" is at version 2
    And appointment "first" shows arrival.visitId "visit-1"
    And appointment "first" has arrival status ON_TIME
    And 1 DockAppointmentCheckedIn events were published
    And the ignored-fact count for already_applied is 1

  Scenario: A door release redelivered under the same id completes once and counts one implicit arrival
    Given appointment "first" is booked INBOUND on door D-BOTH from 09:00 to 11:00 for ASN-1
    When the yard delivers event "evt-release-1" reporting the door release of appointment "first" at 10:30
    And the yard redelivers event "evt-release-1"
    Then appointment "first" is COMPLETED
    And appointment "first" is at version 2
    And 1 DockAppointmentCompleted events were published
    And 1 implicit arrival was counted
    And the ignored-fact count for terminal_appointment is 0

  Scenario: A second door release under another id finds the appointment COMPLETED and is counted as terminal
    Given appointment "first" is booked INBOUND on door D-BOTH from 09:00 to 11:00 for ASN-1
    When the yard delivers event "evt-release-1" reporting the door release of appointment "first" at 10:30
    And the yard delivers event "evt-release-2" reporting the door release of appointment "first" at 10:45
    Then appointment "first" is COMPLETED
    And appointment "first" shows completedAt "@10:30"
    And 1 DockAppointmentCompleted events were published
    And the ignored-fact count for terminal_appointment is 1
    And 1 implicit arrival was counted

  Scenario: A docking redelivered under the same id reassigns the door once
    Given appointment "first" is booked INBOUND on door D-BOTH from 09:00 to 11:00 for ASN-1
    When the yard delivers event "evt-dock-1" reporting the trailer of appointment "first" docked at door D-IN at 09:10
    And the yard redelivers event "evt-dock-1"
    Then appointment "first" is on door D-IN
    And appointment "first" is at version 2
    And 1 DockAppointmentDoorReassigned events were published

  Scenario: A rejection redelivered under the same id cancels once
    Given appointment "first" is booked INBOUND on door D-BOTH from 09:00 to 11:00 for ASN-1
    When the yard delivers event "evt-reject-1" turning the carrier of appointment "first" away at the gate
    And the yard redelivers event "evt-reject-1"
    Then appointment "first" is CANCELLED
    And appointment "first" is at version 2
    And 1 DockAppointmentCancelled events were published
    And the ignored-fact count for terminal_appointment is 0

  # --- the arrival status uses the SITE's tolerance (X6) -----------------------------------------------------------------------

  Scenario Outline: The arrival is classified against the window start with the site's own tolerance
    Given site WH1 declares the settings window 240, granularity 15, tolerance <tolerance> and no-show grace 60 minutes
    And appointment "first" is booked INBOUND on door D-BOTH from 09:00 to 11:00 for ASN-1
    When the yard reports visit "visit-1" checked in for appointment "first" at <arrival>
    Then appointment "first" has arrival status <status>

    Examples:
      | tolerance | arrival | status  |
      | 0         | 08:59   | EARLY   |
      | 0         | 09:00   | ON_TIME |
      | 0         | 09:01   | LATE    |
      | 5         | 08:54   | EARLY   |
      | 5         | 08:55   | ON_TIME |
      | 5         | 09:05   | ON_TIME |
      | 5         | 09:06   | LATE    |
      | 30        | 08:30   | ON_TIME |
      | 30        | 09:31   | LATE    |

  Scenario: The CheckedIn fact carries the arrival status, the visit and the instant, and the appointment shows them
    Given appointment "first" is booked INBOUND on door D-BOTH from 09:00 to 11:00 for ASN-1
    When the yard reports visit "visit-77" checked in for appointment "first" at 09:40
    Then the DockAppointmentCheckedIn fact of appointment "first" carries arrival_status "LATE"
    And the DockAppointmentCheckedIn fact of appointment "first" carries visit_id "visit-77"
    And the DockAppointmentCheckedIn fact of appointment "first" carries checked_in_at "@09:40"
    And appointment "first" shows arrival.status "LATE"
    And appointment "first" shows arrival.implicit false
    And appointment "first" shows arrival.checkedInAt "@09:40"

  # --- facts that name nothing the context can apply -------------------------------------------------------------------------

  Scenario Outline: A fact naming an appointment that is unknown or malformed is ignored and counted, a walk-in is not even counted
    Given appointment "first" is booked INBOUND on door D-BOTH from 09:00 to 11:00 for ASN-1
    When the yard reports a door release at site WH1 naming the appointment id "<id>"
    Then the ignored-fact count for unknown_appointment is <counted>
    And appointment "first" is BOOKED
    And the published events are DockAppointmentBooked

    Examples:
      | id                                        | counted |
      | appt-00000000-0000-4000-8000-000000000000 | 1       |
      | not-an-appointment                        | 1       |
      | <none>                                    | 0       |

  Scenario: Every yard fact for a NO_SHOW appointment is ignored and counted
    Given appointment "first" is booked INBOUND on door D-BOTH from 09:00 to 11:00 for ASN-1
    And appointment "first" has been brought to NO_SHOW
    When the yard reports visit "visit-1" checked in for appointment "first" at 12:05
    And the yard reports the trailer of appointment "first" docked at door D-IN at 12:10
    And the yard reports the door release of appointment "first" at 12:30
    And the yard turns the carrier of appointment "first" away at the gate
    Then appointment "first" is NO_SHOW
    And appointment "first" is on door D-BOTH
    And the ignored-fact count for terminal_appointment is 4
    And the published events are DockAppointmentBooked, DockAppointmentNoShow

  Scenario: A docking and a rejection for a CANCELLED appointment are ignored and counted
    Given appointment "first" is booked INBOUND on door D-BOTH from 09:00 to 11:00 for ASN-1
    And appointment "first" has been brought to CANCELLED
    When the yard reports the trailer of appointment "first" docked at door D-IN at 09:10
    And the yard turns the carrier of appointment "first" away at the gate
    Then appointment "first" is CANCELLED
    And appointment "first" was cancelled for reason SITE_REQUEST
    And appointment "first" is on door D-BOTH
    And the ignored-fact count for terminal_appointment is 2

  Scenario: A rejection after the release is ignored: the carrier was not turned away from a COMPLETED visit
    Given appointment "first" is booked INBOUND on door D-BOTH from 09:00 to 11:00 for ASN-1
    And appointment "first" has been brought to COMPLETED
    When the yard turns the carrier of appointment "first" away at the gate
    Then appointment "first" is COMPLETED
    And the ignored-fact count for terminal_appointment is 1

  # --- ActualDoorWins ----------------------------------------------------------------------------------------------------------

  Scenario: A docking at the booked door changes nothing
    Given appointment "first" is booked INBOUND on door D-BOTH from 09:00 to 11:00 for ASN-1
    When the yard reports the trailer of appointment "first" docked at door D-BOTH at 09:10
    Then appointment "first" is on door D-BOTH
    And appointment "first" is at version 1
    And the published events are DockAppointmentBooked

  Scenario: A docking at another door announces the reassignment with both doors and the new version
    Given appointment "first" is booked INBOUND on door D-BOTH from 09:00 to 11:00 for ASN-1
    When the yard reports the trailer of appointment "first" docked at door D-IN at 09:10
    Then appointment "first" is on door D-IN
    And appointment "first" is BOOKED
    And the DockAppointmentDoorReassigned fact of appointment "first" carries from_door_code "D-BOTH"
    And the DockAppointmentDoorReassigned fact of appointment "first" carries to_door_code "D-IN"
    And the DockAppointmentDoorReassigned fact of appointment "first" carries version 2
    And door D-BOTH has 0 active appointments
    And door D-IN has 1 active appointment

  Scenario: A CHECKED_IN appointment can be reassigned too
    Given appointment "first" is booked INBOUND on door D-BOTH from 09:00 to 11:00 for ASN-1
    And appointment "first" has been brought to CHECKED_IN
    When the yard reports the trailer of appointment "first" docked at door D-IN at 09:10
    Then appointment "first" is CHECKED_IN
    And appointment "first" is on door D-IN
    And appointment "first" is at version 3

  Scenario: The physical door wins even over a window that is taken, and flags only the BOOKED appointments it now overlaps
    Given appointment "first" is booked INBOUND on door D-BOTH from 09:00 to 11:00 for ASN-1
    And appointment "before" is booked INBOUND on door D-IN from 07:00 to 09:00 for ASN-2
    And appointment "arrived" is booked INBOUND on door D-IN from 09:00 to 10:00 for ASN-3
    And appointment "arrived" has been brought to CHECKED_IN
    And appointment "overlapped" is booked INBOUND on door D-IN from 10:00 to 12:00 for ASN-4
    And appointment "after" is booked INBOUND on door D-IN from 12:00 to 14:00 for ASN-5
    When the yard reports the trailer of appointment "first" docked at door D-IN at 09:10
    Then appointment "first" is on door D-IN
    And appointment "overlapped" is at risk because DOOR_REASSIGNED_OVERLAP
    And appointment "before" is not at risk
    And appointment "arrived" is not at risk
    And appointment "after" is not at risk
    And appointment "first" is not at risk
    And 1 DockAppointmentAtRisk events were published
    And the DockAppointmentAtRisk fact of appointment "overlapped" carries cause "DOOR_REASSIGNED_OVERLAP"
    And the DockAppointmentAtRisk fact of appointment "overlapped" lacks the field version

  # --- CompleteOnDoorRelease ---------------------------------------------------------------------------------------------------

  Scenario: A door release after the gate check-in completes the appointment and the arrival stays the real one
    Given appointment "first" is booked INBOUND on door D-BOTH from 09:00 to 11:00 for ASN-1
    And the yard reports visit "visit-1" checked in for appointment "first" at 09:50
    When the yard reports the door release of appointment "first" at 10:40
    Then appointment "first" is COMPLETED
    And appointment "first" has arrival status LATE
    And appointment "first" shows arrival.implicit false
    And appointment "first" shows completedAt "@10:40"
    And 0 implicit arrivals were counted
    And the DockAppointmentCompleted fact of appointment "first" carries completed_at "@10:40"
    And the DockAppointmentCompleted fact of appointment "first" carries version 3

  Scenario: The implicit arrival of an out-of-order release raises no CheckedIn fact
    Given appointment "first" is booked INBOUND on door D-BOTH from 09:00 to 11:00 for ASN-1
    When the yard reports the door release of appointment "first" at 10:40
    Then appointment "first" shows arrival.implicit true
    And appointment "first" shows arrival.status "ON_TIME"
    And 0 DockAppointmentCheckedIn events were published
    And 1 DockAppointmentCompleted events were published

  Scenario: A check-in that arrives after the release is ignored and cannot reopen the visit
    Given appointment "first" is booked INBOUND on door D-BOTH from 09:00 to 11:00 for ASN-1
    And the yard reports the door release of appointment "first" at 10:40
    When the yard reports visit "visit-1" checked in for appointment "first" at 09:05
    Then appointment "first" is COMPLETED
    And appointment "first" shows arrival.implicit true
    And appointment "first" is at version 2
    And door D-BOTH has 0 active appointments

  # --- CancelOnVisitRejected ---------------------------------------------------------------------------------------------------

  Scenario: A turned-away carrier frees the ASN and the Cancelled fact says TURNED_AWAY
    Given appointment "first" is booked INBOUND on door D-BOTH from 09:00 to 11:00 for ASN-1
    When the yard turns the carrier of appointment "first" away at the gate
    Then the DockAppointmentCancelled fact of appointment "first" carries reason "TURNED_AWAY"
    And the DockAppointmentCancelled fact of appointment "first" carries version 2
    When the booking "again" is requested as INBOUND on door D-IN from 09:00 to 11:00 for ASN-1
    Then the response status is 201

  # --- applied, not recorded ---------------------------------------------------------------------------------------------------

  Scenario Outline: No REST route records a check-in, a docking or a door release: those are applied from the yard's events
    Given appointment "first" is booked INBOUND on door D-BOTH from 09:00 to 11:00 for ASN-1
    When a POST request is sent to "/appointments/{first}/<route>" with the JSON body {"doorCode":"D-IN","checkedInAt":"@09:05"}
    Then the response status is 404
    And appointment "first" is BOOKED
    And appointment "first" is at version 1

    Examples:
      | route          |
      | check-ins      |
      | arrivals       |
      | dockings       |
      | door-releases  |
