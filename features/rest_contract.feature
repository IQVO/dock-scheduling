# rest_contract.feature - the REST conventions every route shares (ADR 0008): Idempotency-Key replays, ETag and If-Match, problem+json.
Feature: The REST edge replays, versions and explains

  Background:
    Given the clock is 2030-01-07T06:00:00Z
    And site WH1 has door D-BOTH taking Both flows
    And ASN ASN-1 is registered
    And ASN ASN-2 is registered

  Scenario: A booking sent twice with one Idempotency-Key books once
    When the INBOUND booking on door D-BOTH from 08:00 to 10:00 for ASN-1 is sent twice with Idempotency-Key "book-1"
    Then the response status is 201
    And the second response replays the first
    And door D-BOTH has 1 active appointment
    And the published events are DockAppointmentBooked

  Scenario: One key cannot stand for two different requests
    Given an INBOUND booking is requested with Idempotency-Key "book-1" on door D-BOTH from 08:00 to 10:00 for ASN-1
    When an INBOUND booking is requested with Idempotency-Key "book-1" on door D-BOTH from 11:00 to 12:00 for ASN-2
    Then the response is the problem idempotency-key-reused with status 409
    And door D-BOTH has 1 active appointment

  Scenario: A refusal is replayed too and leaves no trace
    Given appointment "first" is booked INBOUND on door D-BOTH from 08:00 to 10:00 for ASN-1
    When the INBOUND booking on door D-BOTH from 09:00 to 11:00 for ASN-2 is sent twice with Idempotency-Key "clash-1"
    Then the response is the problem door-double-booked with status 409
    And the second response replays the first
    And the published events are DockAppointmentBooked

  Scenario: The ETag is the version and If-Match guards the change
    Given appointment "first" is booked INBOUND on door D-BOTH from 08:00 to 10:00 for ASN-1
    When appointment "first" is fetched
    Then the response carries the header ETag W/"1"
    When appointment "first" is cancelled with reason SITE_REQUEST if its version is 7
    Then the response is the problem precondition-failed with status 412
    And appointment "first" is BOOKED
    When appointment "first" is cancelled with reason SITE_REQUEST if its version is 1
    Then the response status is 200
    And the response carries the header ETag W/"2"

  Scenario: The dock planner may cancel with SITE_REQUEST but not with a policy reason (D-5)
    Given appointment "first" is booked INBOUND on door D-BOTH from 08:00 to 10:00 for ASN-1
    When appointment "first" is cancelled with reason TURNED_AWAY
    Then the response is the problem invalid-reason with status 422
    And appointment "first" is BOOKED

  Scenario: An unknown appointment is a problem document
    When an unknown appointment is fetched
    Then the response is the problem appointment-not-found with status 404
