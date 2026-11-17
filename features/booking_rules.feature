# booking_rules.feature - BookAppointment (use-cases.md, aggregate canvas "Enforced Invariants", ADR 0002): every value-object rule, the
# cap and grid boundaries, the references a direction must carry, and what the Booked and Rescheduled facts tell the world. The
# earlier features pin the cross-aggregate rules (overlap, flow, ASN uniqueness); these pin the shape of a booking at its edges.
Feature: A booking is accepted at the documented limits and refused one step beyond

  Background:
    Given the clock is 2030-01-07T06:00:00Z
    And site WH1 has door D-BOTH taking Both flows
    And site WH1 has door D-IN taking Inbound flows
    And site WH1 has door D-OUT taking Outbound flows
    And ASN ASN-1 is registered
    And ASN ASN-2 is registered
    And ASN ASN-3 is registered
    And CPT cutoff WH1-1500 exists at site WH1

  # --- the window ---------------------------------------------------------------------------------------------------------

  Scenario: A window of exactly the 4 hour cap is accepted
    When an INBOUND booking is requested on door D-BOTH from 08:00 to 12:00 for ASN-1
    Then the response status is 201
    And the response field "version" is 1
    And the response field "state" is "BOOKED"

  Scenario Outline: A window that is empty, reversed or longer than the cap is refused as invalid-window
    When an INBOUND booking is requested on door D-BOTH from <start> to <end> for ASN-1
    Then the response is the problem invalid-window with status 422
    And door D-BOTH has 0 active appointments
    And 0 events have been published

    Examples:
      | start | end   |
      | 08:00 | 12:15 |
      | 08:00 | 08:00 |
      | 10:00 | 08:00 |

  Scenario: A site may lower the maximum window but never raise it
    Given site WH1 declares the settings window 120, granularity 15, tolerance 15 and no-show grace 60 minutes
    When the booking "two hours" is requested as INBOUND on door D-BOTH from 08:00 to 10:00 for ASN-1
    Then the response status is 201
    When an INBOUND booking is requested on door D-BOTH from 10:00 to 12:15 for ASN-2
    Then the response is the problem invalid-window with status 422
    When a PUT request is sent to "/sites/WH1/dock-calendar/settings" with the JSON body {"maxWindowMinutes":241,"slotGranularityMinutes":15,"arrivalToleranceMinutes":15,"noShowGraceMinutes":60}
    Then the response is the problem invalid-settings with status 422

  Scenario Outline: The start and the end sit on the site's slot grid
    Given site WH1 declares the settings window 240, granularity <grain>, tolerance 15 and no-show grace 60 minutes
    When an INBOUND booking is requested on door D-BOTH from <start> to <end> for ASN-1
    Then the response status is <status>

    Examples:
      | grain | start | end   | status |
      | 15    | 08:10 | 09:00 | 422    |
      | 15    | 08:00 | 08:50 | 422    |
      | 15    | 08:15 | 09:00 | 201    |
      | 30    | 08:15 | 09:00 | 422    |
      | 30    | 08:30 | 09:00 | 201    |
      | 5     | 08:05 | 08:55 | 201    |

  Scenario: A window may start exactly now but not a minute before
    When an INBOUND booking is requested on door D-BOTH from 05:45 to 07:00 for ASN-1
    Then the response is the problem window-in-past with status 422
    When the booking "now" is requested as INBOUND on door D-BOTH from 06:00 to 07:00 for ASN-1
    Then the response status is 201
    And the published events are DockAppointmentBooked

  Scenario: A reschedule may not move the window into the past either
    Given appointment "first" is booked INBOUND on door D-BOTH from 08:00 to 10:00 for ASN-1
    And the clock is 07:30
    When appointment "first" is rescheduled to 07:00-09:00
    Then the response is the problem window-in-past with status 422
    And appointment "first" runs from 08:00 to 10:00
    And appointment "first" is at version 1
    When appointment "first" is rescheduled to 07:30-09:00
    Then the response status is 200

  # --- the door code and the carrier -------------------------------------------------------------------------------------

  Scenario Outline: A door code with whitespace, a slash or no characters is refused, a 64 character one is merely unknown
    When an INBOUND booking is requested on door D-BOTH from 08:00 to 10:00 for ASN-1 with doorCode "<code>"
    Then the response is the problem <slug> with status 422

    Examples:
      | code      | slug              |
      | D 1       | invalid-door-code |
      | D/1       | invalid-door-code |
      |           | invalid-door-code |
      | {text:65} | invalid-door-code |
      | {text:64} | unknown-door      |

  Scenario Outline: A carrier needs a name of 1 to 100 characters and a SCAC of 2 to 4 upper-case letters
    When an INBOUND booking is requested on door D-BOTH from 08:00 to 10:00 for ASN-1 with <field> "<value>"
    Then the response is the problem invalid-carrier with status 422
    And door D-BOTH has 0 active appointments

    Examples:
      | field       | value      |
      | carrier     |            |
      | carrier     | {text:101} |
      | carrierScac | acmf       |
      | carrierScac | A          |
      | carrierScac | ACMFX      |

  Scenario: A carrier name of 100 characters and a SCAC of 2 letters are accepted
    When an INBOUND booking is requested on door D-BOTH from 08:00 to 10:00 for ASN-1 with carrier "{text:100}"
    Then the response status is 201
    When an INBOUND booking is requested on door D-IN from 08:00 to 10:00 for ASN-2 with carrierScac "AC"
    Then the response status is 201
    And the response field "carrierScac" is "AC"

  # --- the references a direction carries --------------------------------------------------------------------------------

  Scenario Outline: An inbound booking must cover distinct ASNs and carries no outbound reference
    When an INBOUND booking is requested on door D-BOTH from 08:00 to 10:00 for ASN-1 with <field> <value>
    Then the response is the problem <slug> with status 422
    And door D-BOTH has 0 active appointments

    Examples:
      | field        | value                | slug          |
      | asnNumbers   | []                   | unknown-asn   |
      | asnNumbers   | [" "]                | unknown-asn   |
      | asnNumbers   | ["ASN-1","ASN-1"]    | duplicate-asn |
      | cptId        | "WH1-1500"           | invalid-refs  |
      | shipmentRefs | ["SHP-9"]            | invalid-refs  |

  Scenario Outline: An outbound booking needs a known CPT or a shipment reference and carries no ASN
    When an OUTBOUND booking is requested on door D-OUT from 08:00 to 10:00 for <cpt> with <field> <value>
    Then the response is the problem <slug> with status 422

    Examples:
      | cpt        | field        | value      | slug                 |
      | WH1-1500   | asnNumbers   | ["ASN-1"]  | invalid-refs         |
      | WH9-0900   | shipmentRefs | []         | missing-outbound-ref |

  Scenario: An outbound booking that names no CPT and no shipment is refused
    When an OUTBOUND booking is requested on door D-OUT from 08:00 to 10:00 for WH1-1500 without cptId
    Then the response is the problem missing-outbound-ref with status 422

  Scenario: A shipment reference alone is enough for an outbound booking whose CPT is unknown
    When an OUTBOUND booking is requested on door D-OUT from 08:00 to 10:00 for WH9-0900 with shipmentRefs ["SHP-1","SHP-2"]
    Then the response status is 201
    And the response field "shipmentRefs.1" is "SHP-2"
    And the response field "state" is "BOOKED"

  Scenario: One appointment may cover several ASNs
    When an INBOUND booking is requested on door D-BOTH from 08:00 to 10:00 for ASN-1 with asnNumbers ["ASN-1","ASN-2"]
    Then the response status is 201
    And the response field "asnNumbers.0" is "ASN-1"
    And the response field "asnNumbers.1" is "ASN-2"

  Scenario: One ASN already scheduled elsewhere refuses the whole booking and claims nothing
    Given appointment "first" is booked INBOUND on door D-IN from 08:00 to 10:00 for ASN-2
    When an INBOUND booking is requested on door D-BOTH from 11:00 to 13:00 for ASN-1 with asnNumbers ["ASN-1","ASN-2"]
    Then the response is the problem asn-already-scheduled with status 409
    And the problem detail names appointment "first"
    When the booking "retry" is requested as INBOUND on door D-BOTH from 11:00 to 13:00 for ASN-1
    Then the response status is 201

  Scenario: One cancelled ASN among several makes the booking unknown-asn
    Given inbound-receiving cancels ASN ASN-2
    When an INBOUND booking is requested on door D-BOTH from 08:00 to 10:00 for ASN-1 with asnNumbers ["ASN-1","ASN-2"]
    Then the response is the problem unknown-asn with status 422
    And door D-BOTH has 0 active appointments

  Scenario: The expected seal declared at booking is kept and published
    When an INBOUND booking is requested on door D-BOTH from 08:00 to 10:00 for ASN-1 with expectedSealNumber "SEAL-884213"
    Then the response status is 201
    And the response field "expectedSealNumber" is "SEAL-884213"
    And the latest DockAppointmentBooked fact carries expected_seal_number "SEAL-884213"

  # --- what the facts say -------------------------------------------------------------------------------------------------

  Scenario: The Booked fact carries version 1 and the site's arrival tolerance at that moment (X1)
    Given appointment "default" is booked INBOUND on door D-BOTH from 08:00 to 10:00 for ASN-1
    And site WH1 declares the settings window 240, granularity 15, tolerance 30 and no-show grace 60 minutes
    And appointment "relaxed" is booked INBOUND on door D-IN from 08:00 to 10:00 for ASN-2
    Then the DockAppointmentBooked fact of appointment "default" carries arrival_tolerance_minutes 15
    And the DockAppointmentBooked fact of appointment "default" carries version 1
    And the DockAppointmentBooked fact of appointment "relaxed" carries arrival_tolerance_minutes 30

  Scenario: The Rescheduled fact carries the previous door and window and the new version
    Given appointment "first" is booked INBOUND on door D-BOTH from 08:00 to 10:00 for ASN-1
    When appointment "first" is rescheduled to door D-IN from 11:00 to 13:00
    Then the response status is 200
    And the DockAppointmentRescheduled fact of appointment "first" carries previous_door_code "D-BOTH"
    And the DockAppointmentRescheduled fact of appointment "first" carries door_code "D-IN"
    And the DockAppointmentRescheduled fact of appointment "first" carries previous_window_start "@08:00"
    And the DockAppointmentRescheduled fact of appointment "first" carries previous_window_end "@10:00"
    And the DockAppointmentRescheduled fact of appointment "first" carries window_start "@11:00"
    And the DockAppointmentRescheduled fact of appointment "first" carries version 2

  # --- reschedule: the same checks as a booking --------------------------------------------------------------------------

  Scenario: A reschedule to a door the facility does not know is refused
    Given appointment "first" is booked INBOUND on door D-BOTH from 08:00 to 10:00 for ASN-1
    When a POST request is sent to "/appointments/{first}/reschedules" with the JSON body {"doorCode":"NOWHERE","windowStart":"@08:00","windowEnd":"@10:00"}
    Then the response is the problem unknown-door with status 422
    And appointment "first" is on door D-BOTH

  Scenario: A reschedule outside the door's operating hours is refused and changes nothing
    Given site WH1 opens Monday 06:00 to 22:00
    And appointment "first" is booked INBOUND on door D-BOTH from 08:00 to 10:00 for ASN-1
    When appointment "first" is rescheduled to 21:00-23:00
    Then the response is the problem door-unavailable with status 409
    And appointment "first" runs from 08:00 to 10:00
    And appointment "first" is at version 1
    And the published events are DoorOperatingHoursDeclared, DockAppointmentBooked

  Scenario: A reschedule into a closure is refused
    Given appointment "first" is booked INBOUND on door D-BOTH from 08:00 to 10:00 for ASN-1
    And door D-BOTH of site WH1 is closed from 12:00 to 13:00 as "leveller"
    When appointment "first" is rescheduled to 12:30-13:30
    Then the response is the problem door-unavailable with status 409
    And appointment "first" runs from 08:00 to 10:00

  Scenario: A reschedule with a stale If-Match is refused and a matching one is applied
    Given appointment "first" is booked INBOUND on door D-BOTH from 08:00 to 10:00 for ASN-1
    When the next request carries If-Match W/"3"
    And a POST request is sent to "/appointments/{first}/reschedules" with the JSON body {"windowStart":"@09:00","windowEnd":"@11:00"}
    Then the response is the problem precondition-failed with status 412
    And appointment "first" runs from 08:00 to 10:00
    When the next request carries If-Match W/"1"
    And a POST request is sent to "/appointments/{first}/reschedules" with the JSON body {"windowStart":"@09:00","windowEnd":"@11:00"}
    Then the response status is 200
    And the response carries the header ETag W/"2"

  Scenario: An If-Match that is not an ETag is a bad request
    Given appointment "first" is booked INBOUND on door D-BOTH from 08:00 to 10:00 for ASN-1
    When the next request carries If-Match banana
    And a POST request is sent to "/appointments/{first}/cancellations" with the JSON body {"reason":"CARRIER_REQUEST"}
    Then the response is the problem invalid-if-match with status 400
    And appointment "first" is BOOKED

  # --- idempotency and the REST edge -------------------------------------------------------------------------------------

  Scenario: A booking without an Idempotency-Key is refused before anything is booked
    When a POST request with no Idempotency-Key is sent to "/appointments" with the JSON body {"direction":"INBOUND","doorCode":"D-BOTH","carrier":"ACME Freight","windowStart":"@08:00","windowEnd":"@10:00","asnNumbers":["ASN-1"]}
    Then the response is the problem idempotency-key-required with status 400
    And door D-BOTH has 0 active appointments
    And 0 events have been published

  Scenario Outline: An Idempotency-Key is 1 to 128 printable characters
    When the next request carries the Idempotency-Key "{text:<length>}"
    And an INBOUND booking is requested on door D-BOTH from 08:00 to 10:00 for ASN-1
    Then the response status is <status>

    Examples:
      | length | status |
      | 128    | 201    |
      | 129    | 400    |

  Scenario: Cancelling twice with one Idempotency-Key replays the first answer instead of answering illegal-transition
    Given appointment "first" is booked INBOUND on door D-BOTH from 08:00 to 10:00 for ASN-1
    When the next request carries the Idempotency-Key "cancel-1"
    And a POST request is sent to "/appointments/{first}/cancellations" with the JSON body {"reason":"CARRIER_REQUEST"}
    And the next request carries the Idempotency-Key "cancel-1"
    And a POST request is sent to "/appointments/{first}/cancellations" with the JSON body {"reason":"CARRIER_REQUEST"}
    Then the second response replays the first
    And the response status is 200
    And the published events are DockAppointmentBooked, DockAppointmentCancelled

  Scenario: A PUT needs no Idempotency-Key because it is idempotent by method
    When a PUT request is sent to "/sites/WH1/dock-calendar/settings" with the JSON body {"maxWindowMinutes":180,"slotGranularityMinutes":15,"arrivalToleranceMinutes":15,"noShowGraceMinutes":60}
    And a PUT request is sent to "/sites/WH1/dock-calendar/settings" with the JSON body {"maxWindowMinutes":180,"slotGranularityMinutes":15,"arrivalToleranceMinutes":15,"noShowGraceMinutes":60}
    Then the response status is 200
    And the response field "settings.maxWindowMinutes" is 180
