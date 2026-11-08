package features_test

import (
	"fmt"
	"net/url"

	"github.com/cucumber/godog"
)

// registerBookingSteps registers the appointment commands, all over REST.
func registerBookingSteps(sc *godog.ScenarioContext, w *world) {
	const when = `(\d\d:\d\d) to (\d\d:\d\d) for (\S+)$`
	sc.Step(`^appointment "([^"]+)" is booked (INBOUND|OUTBOUND) on door (\S+) from `+when, w.bookAndRemember)
	sc.Step(`^an? (INBOUND|OUTBOUND) booking is requested on door (\S+) from `+when, w.requestBooking)
	sc.Step(`^the booking "([^"]+)" is requested as (INBOUND|OUTBOUND) on door (\S+) from `+when, w.requestAndRemember)
	sc.Step(`^an? (INBOUND|OUTBOUND) booking is requested with Idempotency-Key "([^"]+)" on door (\S+) from `+when, w.requestWithKey)
	sc.Step(`^the (INBOUND|OUTBOUND) booking on door (\S+) from `+when[:len(when)-1]+` is sent twice with Idempotency-Key "([^"]+)"$`, w.sendTwice)

	sc.Step(`^appointment "([^"]+)" is rescheduled to (\d\d:\d\d)-(\d\d:\d\d)$`, w.rescheduleWindow)
	sc.Step(`^appointment "([^"]+)" is rescheduled to door (\S+) from (\d\d:\d\d) to (\d\d:\d\d)$`, w.rescheduleDoor)
	sc.Step(`^appointment "([^"]+)" is cancelled with reason (\S+)$`, w.cancel)
	sc.Step(`^appointment "([^"]+)" is cancelled with reason (\S+) if its version is (\d+)$`, w.cancelIfVersion)
	sc.Step(`^the carrier of appointment "([^"]+)" reports an ETA of (\d\d:\d\d)$`, w.reportEta)
	sc.Step(`^appointment "([^"]+)" is fetched$`, w.fetch)
	sc.Step(`^an unknown appointment is fetched$`, w.fetchUnknown)
	sc.Step(`^the no-show sweep runs$`, w.runSweep)
}

func (w *world) bookAndRemember(alias, direction, door, from, to, ref string) error {
	if err := w.call("POST", "/appointments", bookingBody(direction, door, from, to, ref)); err != nil {
		return err
	}
	return w.remember(alias)
}

func (w *world) requestBooking(direction, door, from, to, ref string) error {
	return w.call("POST", "/appointments", bookingBody(direction, door, from, to, ref))
}

// requestAndRemember books through REST and keeps the alias only when it worked.
func (w *world) requestAndRemember(alias, direction, door, from, to, ref string) error {
	if err := w.requestBooking(direction, door, from, to, ref); err != nil {
		return err
	}
	if w.last.status == 201 {
		return w.remember(alias)
	}
	return nil
}

func (w *world) requestWithKey(direction, key, door, from, to, ref string) error {
	w.nextKey = key
	return w.requestBooking(direction, door, from, to, ref)
}

func (w *world) sendTwice(direction, door, from, to, ref, key string) error {
	for range 2 {
		if err := w.requestWithKey(direction, key, door, from, to, ref); err != nil {
			return err
		}
	}
	return nil
}

func (w *world) rescheduleWindow(alias, from, to string) error {
	return w.reschedule(alias, map[string]any{"windowStart": at(from), "windowEnd": at(to)})
}

func (w *world) rescheduleDoor(alias, door, from, to string) error {
	return w.reschedule(alias, map[string]any{"doorCode": door, "windowStart": at(from), "windowEnd": at(to)})
}

// reschedule posts a reschedule of the aliased appointment.
func (w *world) reschedule(alias string, body map[string]any) error {
	id, err := w.id(alias)
	if err != nil {
		return err
	}
	return w.call("POST", "/appointments/"+id+"/reschedules", body)
}

func (w *world) cancel(alias, reason string) error {
	id, err := w.id(alias)
	if err != nil {
		return err
	}
	return w.call("POST", "/appointments/"+id+"/cancellations", map[string]string{"reason": reason})
}

func (w *world) cancelIfVersion(alias, reason string, version int) error {
	w.ifMatch = fmt.Sprintf(`W/"%d"`, version)
	return w.cancel(alias, reason)
}

func (w *world) reportEta(alias, hhmm string) error {
	id, err := w.id(alias)
	if err != nil {
		return err
	}
	return w.call("POST", "/appointments/"+id+"/eta-reports", map[string]string{"etaAt": at(hhmm), "source": "carrier-portal"})
}

func (w *world) fetch(alias string) error {
	id, err := w.id(alias)
	if err != nil {
		return err
	}
	return w.call("GET", "/appointments/"+id, nil)
}

func (w *world) fetchUnknown() error {
	return w.call("GET", "/appointments/appt-00000000-0000-0000-0000-000000000000", nil)
}

// runSweep calls POST /no-show-sweeps and keeps the ids it marked.
func (w *world) runSweep() error {
	if err := w.call("POST", "/no-show-sweeps", nil); err != nil {
		return err
	}
	m, err := w.json()
	if err != nil {
		return err
	}
	w.sweep = nil
	ids, _ := m["appointmentIds"].([]any)
	for _, id := range ids {
		w.sweep = append(w.sweep, fmt.Sprint(id))
	}
	return nil
}

// activeOn counts the BOOKED and CHECKED_IN appointments of a door over REST.
func (w *world) activeOn(door string) (int, error) {
	total := 0
	for _, state := range []string{"BOOKED", "CHECKED_IN"} {
		if err := w.call("GET", "/appointments?doorCode="+url.QueryEscape(door)+"&state="+state, nil); err != nil {
			return 0, err
		}
		m, err := w.json()
		if err != nil {
			return 0, err
		}
		items, _ := m["items"].([]any)
		total += len(items)
	}
	return total, nil
}
