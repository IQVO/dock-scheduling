# no_show_sweep.feature - POST /no-show-sweeps (NoShowSweep, D-9): a BOOKED appointment whose window end plus the site's no-show grace
# (default 60 minutes) has passed becomes NO_SHOW, each in its own transaction. The sweep is naturally idempotent and NO_SHOW is terminal.
Feature: The no-show sweep marks BOOKED appointments past window end plus grace

  Background:
    Given the clock is 2030-01-07T06:00:00Z
    And site WH1 has door D-BOTH taking Both flows
    And ASN ASN-1 is registered
    And ASN ASN-2 is registered

  Scenario: The grace period is honoured to the minute
    Given appointment "first" is booked INBOUND on door D-BOTH from 08:00 to 10:00 for ASN-1
    When the clock is 10:59
    And the no-show sweep runs
    Then the sweep marked 0 appointments
    And appointment "first" is BOOKED
    When time advances 1 minutes
    And the no-show sweep runs
    Then the sweep marked 1 appointment
    And the sweep marked appointment "first"
    And appointment "first" is NO_SHOW
    And the published events are DockAppointmentBooked, DockAppointmentNoShow

  Scenario: A second sweep marks nothing new
    Given appointment "first" is booked INBOUND on door D-BOTH from 08:00 to 10:00 for ASN-1
    And the clock is 12:00
    When the no-show sweep runs
    Then the sweep marked 1 appointment
    When the no-show sweep runs
    Then the sweep marked 0 appointments
    And the published events are DockAppointmentBooked, DockAppointmentNoShow

  Scenario: The site's own grace period applies
    Given site WH1 declares the settings window 240, granularity 15, tolerance 15 and no-show grace 10 minutes
    And appointment "first" is booked INBOUND on door D-BOTH from 08:00 to 10:00 for ASN-1
    When the clock is 10:10
    And the no-show sweep runs
    Then the sweep marked appointment "first"

  Scenario: An appointment the carrier showed up for is never a no-show
    Given appointment "first" is booked INBOUND on door D-BOTH from 08:00 to 10:00 for ASN-1
    And the yard reports visit "visit-1" checked in for appointment "first" at 08:05
    And the clock is 12:00
    When the no-show sweep runs
    Then the sweep marked 0 appointments
    And appointment "first" is CHECKED_IN

  Scenario: A cancelled appointment is not swept
    Given appointment "first" is booked INBOUND on door D-BOTH from 08:00 to 10:00 for ASN-1
    And appointment "first" is cancelled with reason SITE_REQUEST
    And the clock is 12:00
    When the no-show sweep runs
    Then the sweep marked 0 appointments
    And appointment "first" is CANCELLED

  Scenario: NO_SHOW is terminal and frees the door
    Given appointment "first" is booked INBOUND on door D-BOTH from 08:00 to 10:00 for ASN-1
    And the clock is 12:00
    And the no-show sweep runs
    When appointment "first" is cancelled with reason CARRIER_REQUEST
    Then the response is the problem illegal-transition with status 409
    And appointment "first" is NO_SHOW
    And door D-BOTH has 0 active appointments

  Scenario: A yard fact for a no-show appointment is ignored and counted (D-9)
    Given appointment "first" is booked INBOUND on door D-BOTH from 08:00 to 10:00 for ASN-1
    And the clock is 12:00
    And the no-show sweep runs
    When the yard reports visit "late-visit" checked in for appointment "first" at 12:05
    Then appointment "first" is NO_SHOW
    And the ignored-fact count for terminal_appointment is 1
    And the published events are DockAppointmentBooked, DockAppointmentNoShow
