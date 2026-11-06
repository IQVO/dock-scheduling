package features_test

import (
	"fmt"
	"time"

	"github.com/cucumber/godog"
)

// registerYardSteps registers the physical facts yard-management owns. They
// reach the use cases through the Consume* methods, which is where the Kafka
// consumers (a later step) hand over the decoded events.
func registerYardSteps(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^the yard reports visit "([^"]+)" checked in for appointment "([^"]+)" at (\d\d:\d\d)$`, func(visit, alias, hhmm string) error {
		id, site, arrived, err := w.yardFact(alias, hhmm)
		if err != nil {
			return err
		}
		return w.svc.ConsumeVisitCheckedIn(w.ctx, w.eventID(), visit, site, id, arrived)
	})

	sc.Step(`^the yard reports the trailer of appointment "([^"]+)" docked at door (\S+) at (\d\d:\d\d)$`, func(alias, door, hhmm string) error {
		id, site, docked, err := w.yardFact(alias, hhmm)
		if err != nil {
			return err
		}
		return w.svc.ConsumeTrailerDocked(w.ctx, w.eventID(), site, id, door, docked)
	})

	sc.Step(`^the yard reports the door release of appointment "([^"]+)" at (\d\d:\d\d)$`, func(alias, hhmm string) error {
		id, site, undocked, err := w.yardFact(alias, hhmm)
		if err != nil {
			return err
		}
		return w.svc.ConsumeTrailerUndocked(w.ctx, w.eventID(), site, id, undocked)
	})

	sc.Step(`^the yard of site (\S+) reports a door release naming appointment "([^"]+)"$`, func(site, alias string) error {
		id, err := w.id(alias)
		if err != nil {
			return err
		}
		return w.svc.ConsumeTrailerUndocked(w.ctx, w.eventID(), site, id, w.clock.now)
	})

	sc.Step(`^the yard turns the carrier of appointment "([^"]+)" away at the gate$`, func(alias string) error {
		id, site, _, err := w.yardFact(alias, "00:00")
		if err != nil {
			return err
		}
		return w.svc.ConsumeVisitRejected(w.ctx, w.eventID(), site, id)
	})
}

// yardFact resolves the appointment id and site of an alias and the time of a
// fact. Reading the site goes through REST, like any other client would.
func (w *world) yardFact(alias, hhmm string) (id, site string, at time.Time, err error) {
	if id, err = w.id(alias); err != nil {
		return "", "", time.Time{}, err
	}
	last, prev := w.last, w.prev
	defer func() { w.last, w.prev = last, prev }()
	if err = w.call("GET", "/appointments/"+id, nil); err != nil {
		return "", "", time.Time{}, err
	}
	m, err := w.json()
	if err != nil {
		return "", "", time.Time{}, err
	}
	site, ok := m["siteId"].(string)
	if !ok {
		return "", "", time.Time{}, fmt.Errorf("appointment %q has no site: %s", alias, w.last.body)
	}
	at, err = atTime(hhmm)
	return id, site, at, err
}
