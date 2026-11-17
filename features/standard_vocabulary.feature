# standard_vocabulary.feature - the standard meaning of the published facts (ADR 0013, plan item YF-3). Every fact carries, as ADDITIVE
# optional data fields, what it means in the vocabularies partners read: x12_status (the ANSI X12 214 shipment status or appointment
# status code), cbv_biz_step (the GS1 EPCIS Core Business Vocabulary business step) and read_point_gln (the GS1 GLN of the door where
# the fact happened). A field is present only when the mapping is honest and the door has a GLN, and omitted (never null) otherwise.
# The scenarios assert the wire `data` the outbox row would carry: the in-memory twin renders it through the very function the
# Postgres outbox publisher uses.
Feature: Published facts carry their standard meaning

  Background:
    Given the clock is 2030-01-07T06:00:00Z
    And site WH1 has door D-IN taking Inbound flows
    And site WH1 has door D-OUT taking Outbound flows and the GLN 4006381333931
    And site WH1 has door D-BOTH taking Both flows and the GLN 4012345000009
    And ASN ASN-1 is registered
    And ASN ASN-2 is registered
    And CPT cutoff WH1-1500 exists at site WH1

  Scenario: An inbound appointment is a delivery appointment
    When appointment "in" is booked INBOUND on door D-IN from 08:00 to 10:00 for ASN-1
    Then the DockAppointmentBooked fact of appointment "in" has x12_status "AB"
    And the DockAppointmentBooked fact of appointment "in" has no cbv_biz_step
    And the DockAppointmentBooked fact of appointment "in" has no read_point_gln

  Scenario: An outbound appointment is a pick-up appointment, at the GLN of its door
    When appointment "out" is booked OUTBOUND on door D-OUT from 08:00 to 10:00 for WH1-1500
    Then the DockAppointmentBooked fact of appointment "out" has x12_status "AA"
    And the DockAppointmentBooked fact of appointment "out" has read_point_gln "4006381333931"
    And the DockAppointmentBooked fact of appointment "out" has no cbv_biz_step

  Scenario: A door without a GLN publishes no read point, and the rest of the fact is unchanged
    When appointment "in" is booked INBOUND on door D-IN from 08:00 to 10:00 for ASN-1
    Then the DockAppointmentBooked fact of appointment "in" has no read_point_gln
    And the DockAppointmentBooked fact of appointment "in" has x12_status "AB"

  Scenario: A check-in is an arrival at the location of its direction
    Given appointment "in" is booked INBOUND on door D-BOTH from 08:00 to 10:00 for ASN-1
    And appointment "out" is booked OUTBOUND on door D-OUT from 08:00 to 10:00 for WH1-1500
    When the clock is 08:05
    And the yard reports visit "visit-1" checked in for appointment "in" at 08:05
    And the yard reports visit "visit-2" checked in for appointment "out" at 08:05
    Then the DockAppointmentCheckedIn fact of appointment "in" has x12_status "X1"
    And the DockAppointmentCheckedIn fact of appointment "in" has cbv_biz_step "urn:epcglobal:cbv:bizstep:arriving"
    And the DockAppointmentCheckedIn fact of appointment "in" has read_point_gln "4012345000009"
    And the DockAppointmentCheckedIn fact of appointment "out" has x12_status "X3"
    And the DockAppointmentCheckedIn fact of appointment "out" has cbv_biz_step "urn:epcglobal:cbv:bizstep:arriving"
    And the DockAppointmentCheckedIn fact of appointment "out" has read_point_gln "4006381333931"

  Scenario: A reschedule restates the appointment and its read point is the new door
    Given appointment "out" is booked OUTBOUND on door D-BOTH from 08:00 to 10:00 for WH1-1500
    When appointment "out" is rescheduled to door D-OUT from 09:00 to 11:00
    Then the DockAppointmentRescheduled fact of appointment "out" has x12_status "AA"
    And the DockAppointmentRescheduled fact of appointment "out" has read_point_gln "4006381333931"

  Scenario: A fact the standards have no honest word for carries only its read point
    Given appointment "out" is booked OUTBOUND on door D-OUT from 08:00 to 10:00 for WH1-1500
    When appointment "out" is cancelled with reason CARRIER_REQUEST
    Then the DockAppointmentCancelled fact of appointment "out" has no x12_status
    And the DockAppointmentCancelled fact of appointment "out" has no cbv_biz_step
    And the DockAppointmentCancelled fact of appointment "out" has read_point_gln "4006381333931"

  Scenario: A fact with nothing to add is byte-for-byte the payload it was before the standard fields
    Given appointment "in" is booked INBOUND on door D-IN from 08:00 to 10:00 for ASN-1
    When appointment "in" is cancelled with reason CARRIER_REQUEST
    Then the DockAppointmentCancelled fact of appointment "in" has exactly the fields appointment_id, site_id, door_code, reason, version

  Scenario: A GLN registered by facility-layout reaches the facts of that door
    Given facility-layout registers dock door D-NEW in zone zone-new of site WH2 taking Inbound flows with the GLN "5412345000013"
    When appointment "in" is booked INBOUND on door D-NEW from 08:00 to 10:00 for ASN-1
    Then the DockAppointmentBooked fact of appointment "in" has read_point_gln "5412345000013"

  Scenario: A GLN that is not a valid GLN is dropped and the door stays bookable
    Given facility-layout registers dock door D-NEW in zone zone-new of site WH2 taking Inbound flows with the GLN "5412345000014"
    When appointment "in" is booked INBOUND on door D-NEW from 08:00 to 10:00 for ASN-1
    Then the response status is 201
    And the DockAppointmentBooked fact of appointment "in" has no read_point_gln

  Scenario: A re-registration that carries no GLN keeps the one the door already has
    Given facility-layout registers dock door D-NEW in zone zone-new of site WH2 taking Inbound flows with the GLN "5412345000013"
    And facility-layout registers dock door D-NEW in zone zone-new of site WH2 taking Inbound flows with the GLN ""
    When appointment "in" is booked INBOUND on door D-NEW from 08:00 to 10:00 for ASN-1
    Then the DockAppointmentBooked fact of appointment "in" has read_point_gln "5412345000013"

  Scenario: A consumer written before the standard fields decodes the new payload and ignores them
    When appointment "out" is booked OUTBOUND on door D-OUT from 08:00 to 10:00 for WH1-1500
    Then a consumer written before the standard fields decodes the DockAppointmentBooked fact of appointment "out" and ignores them
