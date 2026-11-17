# carrier_registry.feature - the carrier registry (ADR 0012): carriers keyed by SCAC (assigned by NMFTA), with an optional USDOT number and
# MC or MX operating authority number, ACTIVE or SUSPENDED. The registry is OPT-IN: a booking that names a registered SUSPENDED carrier is
# refused (409 carrier-suspended); a registered ACTIVE carrier, an unregistered SCAC and a booking with no SCAC are accepted as before.
# Suspending a carrier cancels nothing. Every scenario drives the REST router; "published events" are the outbox facts of the unit of work.
Feature: A carrier registry that can stop a suspended carrier from booking

  Background:
    Given the clock is 2030-01-07T06:00:00Z
    And site WH1 has door D-BOTH taking Both flows
    And ASN ASN-1 is registered
    And ASN ASN-2 is registered

  Scenario: Registering a new carrier
    When carrier ACMF named "ACME Freight" is registered with USDOT "1234567" and MC "654321"
    Then the response status is 201
    And the response carries the header Location /carriers/ACMF
    And the response carries the header ETag W/"1"
    And the carrier has status "ACTIVE"
    And the carrier has usdot "1234567"
    And the carrier has mc "MC654321"
    And the carrier is at version 1
    And the published events are CarrierRegistered

  Scenario: Registering the same data again is an idempotent no-op
    Given carrier ACMF named "ACME Freight" is registered with USDOT "1234567" and MC "MC-654321"
    When carrier ACMF named "ACME Freight" is registered with USDOT "1234567" and MC "654321"
    Then the response status is 200
    And the carrier is at version 1
    And the published events are CarrierRegistered

  Scenario: Registering other data on a registered SCAC is refused and an update is explicit
    Given carrier ACMF named "ACME Freight" is registered with USDOT "1234567" and MC "654321"
    When carrier ACMF named "ACME Freight Lines" is registered with USDOT "1234567" and MC "654321"
    Then the response is the problem carrier-already-registered with status 409
    When carrier ACMF is renamed to "ACME Freight Lines"
    Then the response status is 200
    And the carrier has name "ACME Freight Lines"
    And the carrier is at version 2
    And the published events are CarrierRegistered, CarrierRegistered

  Scenario: A SCAC is two to four upper-case letters
    When carrier ACMFX named "Too Long" is registered with USDOT "" and MC ""
    Then the response is the problem invalid-scac with status 422
    When carrier acmf named "Lower Case" is registered with USDOT "" and MC ""
    Then the response is the problem invalid-scac with status 422
    When carrier A1 named "Digit" is registered with USDOT "" and MC ""
    Then the response is the problem invalid-scac with status 422
    And 0 events have been published

  Scenario: A USDOT number is up to eight digits
    When carrier ACMF named "ACME Freight" is registered with USDOT "123456789" and MC ""
    Then the response is the problem invalid-usdot with status 422
    When carrier ACMF named "ACME Freight" is registered with USDOT "12AB" and MC ""
    Then the response is the problem invalid-usdot with status 422
    And 0 events have been published

  Scenario: An MC or MX number is digits with an optional MC or MX prefix
    When carrier ACMF named "ACME Freight" is registered with USDOT "" and MC "FF123"
    Then the response is the problem invalid-mc with status 422
    When carrier ACMF named "ACME Freight" is registered with USDOT "" and MC "mx-9876"
    Then the response status is 201
    And the carrier has mc "MX9876"
    And the carrier has usdot ""

  Scenario: A suspended carrier is refused at the calendar, with its reason
    Given carrier ACMF named "ACME Freight" is registered with USDOT "1234567" and MC "654321"
    When carrier ACMF is suspended with reason "insurance lapsed"
    Then the response status is 200
    And the carrier has status "SUSPENDED"
    And the carrier has suspensionReason "insurance lapsed"
    And the carrier is at version 2
    When an INBOUND booking is requested on door D-BOTH from 08:00 to 10:00 for ASN-1
    Then the response is the problem carrier-suspended with status 409
    And the response body mentions "insurance lapsed"
    And door D-BOTH has 0 active appointments
    And the published events are CarrierRegistered, CarrierSuspended

  Scenario: A suspended carrier cannot move an appointment either, and nothing is cancelled
    Given appointment "early" is booked INBOUND on door D-BOTH from 08:00 to 10:00 for ASN-1
    And carrier ACMF named "ACME Freight" is registered with USDOT "" and MC ""
    And carrier ACMF is suspended with reason "authority revoked"
    When appointment "early" is rescheduled to 10:00-12:00
    Then the response is the problem carrier-suspended with status 409
    And appointment "early" is BOOKED
    And appointment "early" runs from 08:00 to 10:00

  Scenario: Reinstating a carrier lets it book again
    Given carrier ACMF named "ACME Freight" is registered with USDOT "" and MC ""
    And carrier ACMF is suspended with reason "insurance lapsed"
    When carrier ACMF is reinstated
    Then the response status is 200
    And the carrier has status "ACTIVE"
    And the carrier has suspensionReason ""
    And the carrier is at version 3
    When an INBOUND booking is requested on door D-BOTH from 08:00 to 10:00 for ASN-1
    Then the response status is 201
    And the published events are CarrierRegistered, CarrierSuspended, CarrierReinstated, DockAppointmentBooked

  Scenario: Only a suspended carrier can be reinstated and only an active one suspended
    Given carrier ACMF named "ACME Freight" is registered with USDOT "" and MC ""
    When carrier ACMF is reinstated
    Then the response is the problem carrier-not-suspended with status 409
    When carrier ACMF is suspended with reason "insurance lapsed"
    And carrier ACMF is suspended with reason "again"
    Then the response is the problem carrier-already-suspended with status 409
    When carrier ACMF is suspended with reason " "
    Then the response is the problem invalid-suspension-reason with status 422

  Scenario: An active registered carrier books as usual
    Given carrier ACMF named "ACME Freight" is registered with USDOT "1234567" and MC "654321"
    When an INBOUND booking is requested on door D-BOTH from 08:00 to 10:00 for ASN-1
    Then the response status is 201

  Scenario: A SCAC that is not in the registry is accepted exactly as today
    Given carrier ZZZZ named "Somebody Else" is registered with USDOT "" and MC ""
    And carrier ZZZZ is suspended with reason "unrelated"
    When an INBOUND booking is requested on door D-BOTH from 08:00 to 10:00 for ASN-1
    Then the response status is 201
    And the published events are CarrierRegistered, CarrierSuspended, DockAppointmentBooked

  Scenario: A booking that names no SCAC is unchanged
    Given carrier ACMF named "ACME Freight" is registered with USDOT "" and MC ""
    And carrier ACMF is suspended with reason "insurance lapsed"
    When an INBOUND booking naming no carrier SCAC is requested on door D-BOTH from 08:00 to 10:00 for ASN-1
    Then the response status is 201

  Scenario: The registry is read by SCAC and listed by status
    Given carrier ACMF named "ACME Freight" is registered with USDOT "" and MC ""
    And carrier BETA named "Beta Lines" is registered with USDOT "" and MC ""
    And carrier BETA is suspended with reason "insurance lapsed"
    When carrier ACMF is fetched
    Then the response status is 200
    And the carrier has name "ACME Freight"
    When the carriers with status SUSPENDED are listed
    Then the response status is 200
    And the carrier list is BETA
    When the carriers are listed
    Then the carrier list is ACMF, BETA
    When carrier NOPE is fetched
    Then the response is the problem carrier-not-found with status 404

  Scenario: A stale If-Match is refused
    Given carrier ACMF named "ACME Freight" is registered with USDOT "" and MC ""
    When carrier ACMF is suspended with reason "insurance lapsed" if its version is 7
    Then the response is the problem precondition-failed with status 412
    When carrier ACMF is fetched
    Then the carrier has status "ACTIVE"
