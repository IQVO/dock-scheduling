# asn_single_appointment.feature - D-12: an ASN may sit on only ONE active appointment (BOOKED or CHECKED_IN). The ASN must be known and
# not cancelled in the local copy of inbound-receiving's ASNs; when every ASN of a BOOKED appointment is cancelled the appointment is
# cancelled with reason ASN_CANCELLED (CancelWhenAllAsnsCancelled).
Feature: An ASN is scheduled on one active appointment at a time

  Background:
    Given the clock is 2030-01-07T06:00:00Z
    And site WH1 has door D-BOTH taking Both flows
    And site WH1 has door D-IN taking Inbound flows
    And ASN ASN-1 is registered
    And ASN ASN-2 is registered

  Scenario: A second active appointment for the same ASN is refused
    Given appointment "first" is booked INBOUND on door D-BOTH from 08:00 to 10:00 for ASN-1
    When an INBOUND booking is requested on door D-IN from 12:00 to 14:00 for ASN-1
    Then the response is the problem asn-already-scheduled with status 409
    And the problem detail names appointment "first"
    And door D-IN has 0 active appointments
    And the published events are DockAppointmentBooked

  Scenario: Cancelling the appointment frees its ASN
    Given appointment "first" is booked INBOUND on door D-BOTH from 08:00 to 10:00 for ASN-1
    And appointment "first" is cancelled with reason CARRIER_REQUEST
    When the booking "second" is requested as INBOUND on door D-IN from 12:00 to 14:00 for ASN-1
    Then the response status is 201

  Scenario: A no-show frees its ASN
    Given appointment "first" is booked INBOUND on door D-BOTH from 08:00 to 10:00 for ASN-1
    And the clock is 11:00
    And the no-show sweep runs
    When the booking "second" is requested as INBOUND on door D-IN from 12:00 to 14:00 for ASN-1
    Then the response status is 201

  Scenario: A reschedule keeps the appointment's own ASN
    Given appointment "first" is booked INBOUND on door D-BOTH from 08:00 to 10:00 for ASN-1
    When appointment "first" is rescheduled to 09:00-11:00
    Then the response status is 200

  Scenario: An ASN the local copy does not know is refused
    When an INBOUND booking is requested on door D-BOTH from 08:00 to 10:00 for ASN-404
    Then the response is the problem unknown-asn with status 422

  Scenario: An ASN inbound-receiving cancelled is refused
    Given inbound-receiving cancels ASN ASN-2
    When an INBOUND booking is requested on door D-BOTH from 08:00 to 10:00 for ASN-2
    Then the response is the problem unknown-asn with status 422

  Scenario: Cancelling the only ASN of a BOOKED appointment cancels it
    Given appointment "first" is booked INBOUND on door D-BOTH from 08:00 to 10:00 for ASN-1
    When inbound-receiving cancels ASN ASN-1
    Then appointment "first" is CANCELLED
    And appointment "first" was cancelled for reason ASN_CANCELLED
    And door D-BOTH has 0 active appointments
    And the published events are DockAppointmentBooked, DockAppointmentCancelled
