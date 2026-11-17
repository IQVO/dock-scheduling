package features_test

import (
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/cucumber/godog"

	"github.com/claudioed/dock-scheduling/internal/application/repository"
)

// deliveries remembers what a scenario delivered under an explicit CloudEvents
// id, so a later step can redeliver the very same message (at-least-once).
type deliveries struct{ sent map[string]func() error }

func (d *deliveries) deliver(eventID string, run func(id string) error) error {
	d.sent[eventID] = func() error { return run(eventID) }
	return d.sent[eventID]()
}

func (d *deliveries) redeliver(eventID string) error {
	again, ok := d.sent[eventID]
	if !ok {
		return fmt.Errorf("no event %q was delivered in this scenario", eventID)
	}
	return again()
}

// registerFieldSteps registers the bookings that override one field of the
// usual body, and the generic assertions on the appointment's own JSON.
func registerFieldSteps(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^an? (INBOUND|OUTBOUND) booking is requested on door (\S+) from (\d\d:\d\d) to (\d\d:\d\d) for (\S+) with (\w+) (.+)$`,
		func(direction, door, from, to, ref, field, literal string) error {
			value, err := w.literal(literal)
			if err != nil {
				return err
			}
			body := bookingBody(direction, door, from, to, ref)
			body[field] = value
			return w.call("POST", "/appointments", body)
		})
	sc.Step(`^an? (INBOUND|OUTBOUND) booking is requested on door (\S+) from (\d\d:\d\d) to (\d\d:\d\d) for (\S+) without (\w+)$`,
		func(direction, door, from, to, ref, field string) error {
			body := bookingBody(direction, door, from, to, ref)
			delete(body, field)
			return w.call("POST", "/appointments", body)
		})
	sc.Step(`^appointment "([^"]+)" shows ([\w.]+) (.+)$`, func(alias, path, literal string) error {
		want, err := w.literal(literal)
		if err != nil {
			return err
		}
		return w.wantAppointmentPath(alias, path, want)
	})
}

// wantAppointmentPath fetches the aliased appointment over REST and compares
// the value at a dotted path; the last response stays untouched.
func (w *world) wantAppointmentPath(alias, path string, want any) error {
	id, err := w.id(alias)
	if err != nil {
		return err
	}
	last, prev := w.last, w.prev
	defer func() { w.last, w.prev = last, prev }()
	if err := w.call("GET", "/appointments/"+id, nil); err != nil {
		return err
	}
	got, present, err := w.bodyField(path)
	if err != nil {
		return err
	}
	if !present || !reflect.DeepEqual(got, want) {
		return fmt.Errorf("appointment %q %s = %v (present %v), want %v", alias, path, got, present, want)
	}
	return nil
}

// registerArrangeSteps registers the arrangement of an appointment into a
// lifecycle state through the same doors the product has: the yard's facts, the
// REST cancel and the no-show sweep.
func registerArrangeSteps(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^appointment "([^"]+)" has been brought to (CHECKED_IN|COMPLETED|CANCELLED|NO_SHOW)$`, w.bringTo)
}

func (w *world) bringTo(alias, state string) error {
	id, site, start, err := w.yardFactAtStart(alias)
	if err != nil {
		return err
	}
	switch state {
	case "CHECKED_IN":
		return w.svc.ConsumeVisitCheckedIn(w.ctx, w.eventID(), "visit-"+alias, site, id, start)
	case "COMPLETED":
		if err := w.svc.ConsumeVisitCheckedIn(w.ctx, w.eventID(), "visit-"+alias, site, id, start); err != nil {
			return err
		}
		return w.svc.ConsumeTrailerUndocked(w.ctx, w.eventID(), site, id, start.Add(time.Hour))
	case "CANCELLED":
		return w.cancelAs(alias, "SITE_REQUEST")
	default:
		return w.sweepOut(alias)
	}
}

// yardFactAtStart is yardFact at the appointment's own window start.
func (w *world) yardFactAtStart(alias string) (id, site string, start time.Time, err error) {
	if id, site, _, err = w.yardFact(alias, "00:00"); err != nil {
		return "", "", time.Time{}, err
	}
	m, err := w.appointment(alias)
	if err != nil {
		return "", "", time.Time{}, err
	}
	start, err = time.Parse(time.RFC3339, fmt.Sprint(m["windowStart"]))
	return id, site, start, err
}

func (w *world) cancelAs(alias, reason string) error {
	last, prev := w.last, w.prev
	defer func() { w.last, w.prev = last, prev }()
	if err := w.cancel(alias, reason); err != nil {
		return err
	}
	if w.last.status != 200 {
		return fmt.Errorf("cancelling %q answered %d: %s", alias, w.last.status, w.last.body)
	}
	return nil
}

// sweepOut lets the appointment's window end and its grace pass, runs the
// sweep and puts the clock back where it was.
func (w *world) sweepOut(alias string) error {
	m, err := w.appointment(alias)
	if err != nil {
		return err
	}
	end, err := time.Parse(time.RFC3339, fmt.Sprint(m["windowEnd"]))
	if err != nil {
		return err
	}
	was, last, prev := w.clock.now, w.last, w.prev
	defer func() { w.clock.now, w.last, w.prev = was, last, prev }()
	w.clock.now = end.Add(24 * time.Hour)
	if err := w.runSweep(); err != nil {
		return err
	}
	if st, _ := w.appointment(alias); st["state"] != "NO_SHOW" {
		return fmt.Errorf("the sweep did not mark %q: %v", alias, st["state"])
	}
	return nil
}

// registerConsumerSteps registers the facts the sibling contexts deliver, with
// an explicit CloudEvents id where a scenario replays one.
func registerConsumerSteps(sc *godog.ScenarioContext, w *world, st *deliveries) {
	registerYardDeliveries(sc, w, st)
	registerFeedSteps(sc, w, st)
}

func registerYardDeliveries(sc *godog.ScenarioContext, w *world, st *deliveries) {
	sc.Step(`^the yard delivers event "([^"]+)" reporting visit "([^"]+)" checked in for appointment "([^"]+)" at (\d\d:\d\d)$`,
		func(event, visit, alias, at string) error {
			id, site, arrived, err := w.yardFact(alias, at)
			if err != nil {
				return err
			}
			return st.deliver(event, func(e string) error { return w.svc.ConsumeVisitCheckedIn(w.ctx, e, visit, site, id, arrived) })
		})
	sc.Step(`^the yard delivers event "([^"]+)" reporting the trailer of appointment "([^"]+)" docked at door (\S+) at (\d\d:\d\d)$`,
		func(event, alias, door, at string) error {
			id, site, docked, err := w.yardFact(alias, at)
			if err != nil {
				return err
			}
			return st.deliver(event, func(e string) error { return w.svc.ConsumeTrailerDocked(w.ctx, e, site, id, door, docked) })
		})
	sc.Step(`^the yard delivers event "([^"]+)" reporting the door release of appointment "([^"]+)" at (\d\d:\d\d)$`,
		func(event, alias, at string) error {
			id, site, undocked, err := w.yardFact(alias, at)
			if err != nil {
				return err
			}
			return st.deliver(event, func(e string) error { return w.svc.ConsumeTrailerUndocked(w.ctx, e, site, id, undocked) })
		})
	sc.Step(`^the yard delivers event "([^"]+)" turning the carrier of appointment "([^"]+)" away at the gate$`,
		func(event, alias string) error {
			id, site, _, err := w.yardFact(alias, "00:00")
			if err != nil {
				return err
			}
			return st.deliver(event, func(e string) error { return w.svc.ConsumeVisitRejected(w.ctx, e, site, id) })
		})
	sc.Step(`^the yard reports a door release at site (\S+) naming the appointment id "([^"]*)"$`, func(site, id string) error {
		if id == "<none>" {
			id = ""
		}
		return w.svc.ConsumeTrailerUndocked(w.ctx, w.eventID(), site, id, w.clock.now)
	})
	sc.Step(`^the yard redelivers event "([^"]+)"$`, st.redeliver)
}

func registerFeedSteps(sc *godog.ScenarioContext, w *world, st *deliveries) {
	sc.Step(`^inbound-receiving delivers event "([^"]+)" cancelling ASN (\S+)$`, func(event, number string) error {
		return st.deliver(event, func(e string) error { return w.svc.ConsumeAsnCancelled(w.ctx, e, number) })
	})
	sc.Step(`^inbound-receiving redelivers event "([^"]+)"$`, st.redeliver)
	sc.Step(`^facility-layout registers zone (\S+) for site (\S+)$`, func(zone, site string) error {
		return w.svc.ConsumeZoneRegistered(w.ctx, w.eventID(), zone, site)
	})
	sc.Step(`^facility-layout registers slot (\S+) in zone (\S+) with role (\w+) and dock flow (\w+)$`, func(code, zone, role, flow string) error {
		return w.svc.ConsumeSlotRegistered(w.ctx, w.eventID(), code, zone, role, flow, "")
	})
	sc.Step(`^facility-layout decommissions slot (\S+)$`, func(code string) error {
		return w.svc.ConsumeSlotDecommissioned(w.ctx, w.eventID(), code)
	})
	sc.Step(`^process-path-management publishes the cutoffs (.+) for site (\S+)$`, func(list, site string) error {
		var cutoffs []repository.Cutoff
		for _, id := range strings.Split(list, ", ") {
			cutoffs = append(cutoffs, repository.Cutoff{ID: id})
		}
		return w.svc.ConsumeCptSchedule(w.ctx, w.eventID(), site, "UTC", cutoffs)
	})
}
