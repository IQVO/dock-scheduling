# door_calendar.feature - operating hours and closures (ADR 0002, D-1, D-8): a window lies inside the door's operating hours and outside
# every closure of that door; no hours declared means 24x7; a closure or an hours declaration may not strand an active appointment.
Feature: A window lies inside the door's hours and outside its closures

  Background:
    Given the clock is 2030-01-07T03:00:00Z
    And site WH1 has door D-BOTH taking Both flows
    And site WH1 has door D-IN taking Inbound flows
    And ASN ASN-1 is registered
    And ASN ASN-2 is registered

  Scenario: A closure refuses a booking inside it and leaves the rest of the day free
    Given door D-BOTH of site WH1 is closed from 09:00 to 11:00 as "leveller"
    When an INBOUND booking is requested on door D-BOTH from 08:00 to 10:00 for ASN-1
    Then the response is the problem door-unavailable with status 409
    And the free windows of door D-BOTH from 08:00 to 13:00 are 08:00-09:00, 11:00-13:00
    When the booking "after" is requested as INBOUND on door D-BOTH from 11:00 to 13:00 for ASN-1
    Then the response status is 201

  Scenario: Lifting the closure reopens the door
    Given door D-BOTH of site WH1 is closed from 09:00 to 11:00 as "leveller"
    When closure "leveller" of site WH1 is lifted
    Then the response status is 200
    And the free windows of door D-BOTH from 08:00 to 12:00 are 08:00-12:00
    When the booking "inside" is requested as INBOUND on door D-BOTH from 09:00 to 10:00 for ASN-1
    Then the response status is 201

  Scenario: A closure cannot cover an active appointment
    Given appointment "first" is booked INBOUND on door D-BOTH from 08:00 to 10:00 for ASN-1
    When closing door D-BOTH of site WH1 from 09:00 to 11:00 is requested
    Then the response is the problem closure-conflicts-with-appointments with status 409
    And the problem detail names appointment "first"
    And appointment "first" is BOOKED
    And the free windows of door D-BOTH from 10:00 to 12:00 are 10:00-12:00

  Scenario: Closures of one door may not overlap, closures of two doors may
    Given door D-BOTH of site WH1 is closed from 09:00 to 11:00 as "first"
    When closing door D-BOTH of site WH1 from 10:00 to 12:00 is requested
    Then the response is the problem overlapping-closure with status 409
    When closing door D-IN of site WH1 from 10:00 to 12:00 is requested
    Then the response status is 201

  Scenario: Lifting a closure twice is refused
    Given door D-BOTH of site WH1 is closed from 09:00 to 11:00 as "leveller"
    And closure "leveller" of site WH1 is lifted
    When closure "leveller" of site WH1 is lifted
    Then the response is the problem closure-already-lifted with status 409

  Scenario: Booking outside the site's operating hours is refused
    Given site WH1 opens Monday 06:00 to 22:00
    When an INBOUND booking is requested on door D-BOTH from 05:00 to 07:00 for ASN-1
    Then the response is the problem door-unavailable with status 409
    When the booking "inside" is requested as INBOUND on door D-BOTH from 06:00 to 08:00 for ASN-1
    Then the response status is 201

  Scenario: A door's own hours replace the site default
    Given site WH1 opens Monday 06:00 to 22:00
    And door D-IN of site WH1 opens Monday 08:00 to 16:00
    When an INBOUND booking is requested on door D-IN from 07:00 to 09:00 for ASN-1
    Then the response is the problem door-unavailable with status 409
    When the booking "other door" is requested as INBOUND on door D-BOTH from 07:00 to 09:00 for ASN-1
    Then the response status is 201

  Scenario: New hours may not strand an active appointment (D-8)
    Given appointment "first" is booked INBOUND on door D-IN from 08:00 to 10:00 for ASN-1
    When door D-IN of site WH1 opens Monday 12:00 to 14:00
    Then the response is the problem hours-conflict-with-appointments with status 409
    And the problem detail names appointment "first"
    And appointment "first" is BOOKED
    When the booking "next" is requested as INBOUND on door D-IN from 10:00 to 12:00 for ASN-2
    Then the response status is 201

  Scenario: Hours whose opening is not before the closing are refused
    When site WH1 opens Monday 20:00 to 08:00
    Then the response is the problem invalid-operating-hours with status 422
