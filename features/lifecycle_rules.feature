# lifecycle_rules.feature - the DockAppointment state machine (aggregate canvas "State Transitions", ADR 0002 A5/A6/A10/A13): every
# command is legal only in the states its edge names, an ETA is advisory, the no-show sweep reads the SITE's grace, and each
# accepted change bumps the version that the facts carry. The earlier features walk the happy edges; these pin the refused ones.
Feature: Every command is legal only on the edges of the lifecycle

  Background:
    Given the clock is 2030-01-07T06:00:00Z
    And site WH1 has door D-BOTH taking Both flows
    And site WH1 has door D-IN taking Inbound flows
    And site WH1 has door D-OUT taking Outbound flows
    And ASN ASN-1 is registered
    And ASN ASN-2 is registered
    And ASN ASN-3 is registered
    And CPT cutoff WH1-1500 exists at site WH1

  # --- illegal transitions, one row per state ---------------------------------------------------------------------------------

  Scenario Outline: Only a BOOKED appointment can be rescheduled
    Given appointment "first" is booked INBOUND on door D-BOTH from 08:00 to 10:00 for ASN-1
    And appointment "first" has been brought to <state>
    When appointment "first" is rescheduled to 11:00-13:00
    Then the response is the problem illegal-transition with status 409
    And appointment "first" is <state>
    And appointment "first" runs from 08:00 to 10:00
    And appointment "first" is at version <version>

    Examples:
      | state      | version |
      | CHECKED_IN | 2       |
      | COMPLETED  | 3       |
      | CANCELLED  | 2       |
      | NO_SHOW    | 2       |

  Scenario Outline: A COMPLETED, CANCELLED or NO_SHOW appointment can no longer be cancelled
    Given appointment "first" is booked INBOUND on door D-BOTH from 08:00 to 10:00 for ASN-1
    And appointment "first" has been brought to <state>
    When appointment "first" is cancelled with reason SITE_REQUEST
    Then the response is the problem illegal-transition with status 409
    And appointment "first" is <state>
    And appointment "first" is at version <version>

    Examples:
      | state     | version |
      | COMPLETED | 3       |
      | CANCELLED | 2       |
      | NO_SHOW   | 2       |

  Scenario Outline: An ETA is accepted only on a BOOKED appointment
    Given appointment "first" is booked INBOUND on door D-BOTH from 08:00 to 10:00 for ASN-1
    And appointment "first" has been brought to <state>
    When the carrier of appointment "first" reports an ETA of 08:40
    Then the response is the problem illegal-transition with status 409
    And appointment "first" is <state>
    And appointment "first" shows eta null

    Examples:
      | state      |
      | CHECKED_IN |
      | COMPLETED  |
      | CANCELLED  |
      | NO_SHOW    |

  Scenario Outline: A command naming an appointment that does not exist is a 404, one naming a malformed id is a 400
    When a POST request is sent to "<path>" with the JSON body <body>
    Then the response is the problem <slug> with status <status>

    Examples:
      | path                                                                 | body                                                 | slug                       | status |
      | /appointments/appt-00000000-0000-4000-8000-000000000000/cancellations | {"reason":"CARRIER_REQUEST"}                         | appointment-not-found      | 404    |
      | /appointments/appt-00000000-0000-4000-8000-000000000000/reschedules   | {"windowStart":"@08:00","windowEnd":"@10:00"}        | appointment-not-found      | 404    |
      | /appointments/appt-00000000-0000-4000-8000-000000000000/eta-reports   | {"etaAt":"@08:40","source":"carrier-portal"}        | appointment-not-found      | 404    |
      | /appointments/not-an-appointment/cancellations                        | {"reason":"CARRIER_REQUEST"}                         | invalid-path-parameter     | 400    |

  # --- cancel -----------------------------------------------------------------------------------------------------------------

  Scenario: A CHECKED_IN appointment can still be cancelled, and that frees its door and its ASN
    Given appointment "first" is booked INBOUND on door D-BOTH from 08:00 to 10:00 for ASN-1
    And appointment "first" has been brought to CHECKED_IN
    When appointment "first" is cancelled with reason SITE_REQUEST
    Then the response status is 200
    And appointment "first" is CANCELLED
    And appointment "first" was cancelled for reason SITE_REQUEST
    And door D-BOTH has 0 active appointments
    When the booking "again" is requested as INBOUND on door D-IN from 08:00 to 10:00 for ASN-1
    Then the response status is 201

  Scenario Outline: A cancellation reason outside CARRIER_REQUEST and SITE_REQUEST is refused over REST
    Given appointment "first" is booked INBOUND on door D-BOTH from 08:00 to 10:00 for ASN-1
    When a POST request is sent to "/appointments/{first}/cancellations" with the JSON body {"reason":"<reason>"}
    Then the response is the problem invalid-reason with status 422
    And appointment "first" is BOOKED
    And appointment "first" is at version 1

    Examples:
      | reason          |
      | WEATHER         |
      | ASN_CANCELLED   |
      | carrier_request |
      |                 |

  Scenario: The Cancelled fact carries the reason and the version
    Given appointment "first" is booked INBOUND on door D-BOTH from 08:00 to 10:00 for ASN-1
    When appointment "first" is cancelled with reason CARRIER_REQUEST
    Then the DockAppointmentCancelled fact of appointment "first" carries reason "CARRIER_REQUEST"
    And the DockAppointmentCancelled fact of appointment "first" carries version 2
    And the DockAppointmentCancelled fact of appointment "first" carries door_code "D-BOTH"

  # --- ETA: advisory, once per report -----------------------------------------------------------------------------------------

  Scenario Outline: An ETA is late only when it is after the window start plus the arrival tolerance
    Given appointment "first" is booked INBOUND on door D-BOTH from 09:00 to 11:00 for ASN-1
    When the carrier of appointment "first" reports an ETA of <eta>
    Then the response status is 201
    And the response field "atRisk" is <late>
    And the response field "appointment.atRisk" is <late>

    Examples:
      | eta   | late  |
      | 08:30 | false |
      | 09:15 | false |
      | 09:16 | true  |
      | 12:00 | true  |

  Scenario Outline: The tolerance a late ETA is measured against is the site's own
    Given site WH1 declares the settings window 240, granularity 15, tolerance <tolerance> and no-show grace 60 minutes
    And appointment "first" is booked INBOUND on door D-BOTH from 09:00 to 11:00 for ASN-1
    When the carrier of appointment "first" reports an ETA of <eta>
    Then the response field "atRisk" is <late>

    Examples:
      | tolerance | eta   | late  |
      | 0         | 09:00 | false |
      | 0         | 09:01 | true  |
      | 30        | 09:30 | false |
      | 30        | 09:31 | true  |

  Scenario: Every late ETA report raises one AtRisk fact and an on-time one raises none
    Given appointment "first" is booked INBOUND on door D-BOTH from 09:00 to 11:00 for ASN-1
    When the carrier of appointment "first" reports an ETA of 09:40
    And the carrier of appointment "first" reports an ETA of 09:50
    Then 2 DockAppointmentAtRisk events were published
    And the DockAppointmentAtRisk fact of appointment "first" carries cause "LATE_ETA"
    And the DockAppointmentAtRisk fact of appointment "first" carries eta_at "@09:50"
    And the DockAppointmentAtRisk fact of appointment "first" lacks the field version
    When the carrier of appointment "first" reports an ETA of 09:05
    Then 2 DockAppointmentAtRisk events were published
    And appointment "first" is not at risk
    And appointment "first" is at version 4

  Scenario: An ETA report is stored and changes nothing else because nothing reschedules automatically
    Given appointment "first" is booked INBOUND on door D-BOTH from 09:00 to 11:00 for ASN-1
    When a POST request is sent to "/appointments/{first}/eta-reports" with the JSON body {"etaAt":"@10:30","source":"transport-feed"}
    Then the response status is 201
    And appointment "first" shows eta.source "transport-feed"
    And appointment "first" shows eta.etaAt "@10:30"
    And appointment "first" runs from 09:00 to 11:00
    And appointment "first" is BOOKED
    And appointment "first" is on door D-BOTH
    And the published events are DockAppointmentBooked, DockAppointmentAtRisk

  Scenario: An ETA report without a source is recorded as coming from the api
    Given appointment "first" is booked INBOUND on door D-BOTH from 09:00 to 11:00 for ASN-1
    When a POST request is sent to "/appointments/{first}/eta-reports" with the JSON body {"etaAt":"@09:05"}
    Then the response status is 201
    And appointment "first" shows eta.source "api"

  Scenario Outline: An ETA report needs an instant and a source of at most 64 characters
    Given appointment "first" is booked INBOUND on door D-BOTH from 09:00 to 11:00 for ASN-1
    When a POST request is sent to "/appointments/{first}/eta-reports" with the JSON body <body>
    Then the response is the problem invalid-eta with status 422
    And appointment "first" shows eta null
    And appointment "first" is at version 1

    Examples:
      | body                                            |
      | {"source":"carrier-portal"}                     |
      | {"etaAt":"@09:05","source":"   "}               |
      | {"etaAt":"@09:05","source":"{text:65}"}         |

  Scenario: A reschedule clears the ETA and the at-risk flag of the old window
    Given appointment "first" is booked INBOUND on door D-BOTH from 09:00 to 11:00 for ASN-1
    And the carrier of appointment "first" reports an ETA of 10:00
    And appointment "first" is at risk because LATE_ETA
    When appointment "first" is rescheduled to 10:00-12:00
    Then the response status is 200
    And appointment "first" is not at risk
    And appointment "first" shows eta null

  # --- version and facts through a whole life ---------------------------------------------------------------------------------

  Scenario: Each accepted change bumps the version by one and every fact except AtRisk carries it
    Given appointment "first" is booked INBOUND on door D-BOTH from 09:00 to 11:00 for ASN-1
    And appointment "first" is rescheduled to 09:30-11:30
    And the carrier of appointment "first" reports an ETA of 10:00
    And the yard reports visit "visit-1" checked in for appointment "first" at 10:05
    And the yard reports the door release of appointment "first" at 11:00
    Then appointment "first" is at version 5
    And the DockAppointmentBooked fact of appointment "first" carries version 1
    And the DockAppointmentRescheduled fact of appointment "first" carries version 2
    And the DockAppointmentAtRisk fact of appointment "first" lacks the field version
    And the DockAppointmentCheckedIn fact of appointment "first" carries version 4
    And the DockAppointmentCompleted fact of appointment "first" carries version 5
    And the published events are DockAppointmentBooked, DockAppointmentRescheduled, DockAppointmentAtRisk, DockAppointmentCheckedIn, DockAppointmentCompleted

  # --- the no-show sweep ------------------------------------------------------------------------------------------------------

  Scenario: A site with a grace of zero marks the no-show at the window end itself
    Given site WH1 declares the settings window 240, granularity 15, tolerance 15 and no-show grace 0 minutes
    And appointment "first" is booked INBOUND on door D-BOTH from 08:00 to 10:00 for ASN-1
    When the clock is 09:59
    And the no-show sweep runs
    Then the sweep marked 0 appointments
    When the clock is 10:00
    And the no-show sweep runs
    Then the sweep marked appointment "first"
    And appointment "first" is NO_SHOW

  Scenario: Each site's own grace decides which of its appointments is a no-show
    Given site WH2 has door D-W2 taking Both flows
    And site WH2 declares the settings window 240, granularity 15, tolerance 15 and no-show grace 10 minutes
    And appointment "wh1" is booked INBOUND on door D-BOTH from 08:00 to 10:00 for ASN-1
    And appointment "wh2" is booked INBOUND on door D-W2 from 08:00 to 10:00 for ASN-2
    When the clock is 10:30
    And the no-show sweep runs
    Then the sweep marked 1 appointment
    And the sweep marked appointment "wh2"
    And appointment "wh1" is BOOKED
    And appointment "wh2" is NO_SHOW

  Scenario: One sweep marks only the BOOKED appointments past their grace
    Given appointment "late" is booked INBOUND on door D-BOTH from 07:00 to 07:30 for ASN-1
    And appointment "within grace" is booked INBOUND on door D-IN from 07:00 to 08:30 for ASN-2
    And appointment "arrived" is booked OUTBOUND on door D-OUT from 07:00 to 07:30 for WH1-1500
    And appointment "arrived" has been brought to CHECKED_IN
    And appointment "missed" is booked OUTBOUND on door D-BOTH from 07:30 to 08:00 for WH1-1500
    When the clock is 09:00
    And the no-show sweep runs
    Then the sweep marked 2 appointments
    And the sweep marked appointment "late"
    And the sweep marked appointment "missed"
    And appointment "within grace" is BOOKED
    And appointment "arrived" is CHECKED_IN
    And 2 DockAppointmentNoShow events were published

  Scenario: The NoShow fact carries the direction, the SCAC, the window end and the version
    Given appointment "missed" is booked OUTBOUND on door D-OUT from 07:00 to 08:00 for WH1-1500
    When the clock is 09:00
    And the no-show sweep runs
    Then the DockAppointmentNoShow fact of appointment "missed" carries direction "OUTBOUND"
    And the DockAppointmentNoShow fact of appointment "missed" carries carrier_scac "ACMF"
    And the DockAppointmentNoShow fact of appointment "missed" carries window_end "@08:00"
    And the DockAppointmentNoShow fact of appointment "missed" carries version 2

  Scenario: An appointment completed by a door release that outran the gate is never swept
    Given appointment "first" is booked INBOUND on door D-BOTH from 07:00 to 08:00 for ASN-1
    And the yard reports the door release of appointment "first" at 07:50
    When the clock is 12:00
    And the no-show sweep runs
    Then the sweep marked 0 appointments
    And appointment "first" is COMPLETED

  Scenario: A sweep repeated with one Idempotency-Key replays the first answer even after more time has passed
    Given appointment "first" is booked INBOUND on door D-BOTH from 07:00 to 08:00 for ASN-1
    And appointment "second" is booked INBOUND on door D-IN from 11:00 to 12:00 for ASN-2
    When the clock is 09:30
    And the next request carries the Idempotency-Key "sweep-0930"
    And a POST request is sent to "/no-show-sweeps"
    And the clock is 13:30
    And the next request carries the Idempotency-Key "sweep-0930"
    And a POST request is sent to "/no-show-sweeps"
    Then the second response replays the first
    And the response field "count" is 1
    And appointment "second" is BOOKED

  Scenario: A sweep without a key is refused and marks nothing
    Given appointment "first" is booked INBOUND on door D-BOTH from 07:00 to 08:00 for ASN-1
    When the clock is 12:00
    And a POST request with no Idempotency-Key is sent to "/no-show-sweeps" with the JSON body {}
    Then the response is the problem idempotency-key-required with status 400
    And appointment "first" is BOOKED
