package features_test

import (
	"fmt"

	"github.com/cucumber/godog"
)

// registerCalendarSteps registers the operating-hours and closure commands of
// the dock planner.
func registerCalendarSteps(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^site (\S+) opens Monday (\d\d:\d\d) to (\d\d:\d\d)$`, func(site, from, to string) error {
		return w.declareHours(site, map[string]any{"weekday": "MON", "opensAt": from, "closesAt": to, "timezone": "UTC"})
	})

	sc.Step(`^door (\S+) of site (\S+) opens Monday (\d\d:\d\d) to (\d\d:\d\d)$`, func(door, site, from, to string) error {
		return w.declareHours(site, map[string]any{"doorCode": door, "weekday": "MON", "opensAt": from, "closesAt": to, "timezone": "UTC"})
	})

	sc.Step(`^door (\S+) of site (\S+) is closed from (\d\d:\d\d) to (\d\d:\d\d) as "([^"]+)"$`, func(door, site, from, to, alias string) error {
		if err := w.scheduleClosure(site, door, from, to); err != nil {
			return err
		}
		if w.last.status != 201 {
			return fmt.Errorf("closure %q answered %d, want 201: %s", alias, w.last.status, w.last.body)
		}
		m, err := w.json()
		if err != nil {
			return err
		}
		w.closures[alias], _ = m["closureId"].(string)
		return nil
	})

	sc.Step(`^closing door (\S+) of site (\S+) from (\d\d:\d\d) to (\d\d:\d\d) is requested$`, func(door, site, from, to string) error {
		return w.scheduleClosure(site, door, from, to)
	})

	sc.Step(`^closure "([^"]+)" of site (\S+) is lifted$`, func(alias, site string) error {
		id, ok := w.closures[alias]
		if !ok {
			return fmt.Errorf("no closure %q was scheduled in this scenario", alias)
		}
		return w.call("POST", "/sites/"+site+"/dock-calendar/closures/"+id+"/lifts", nil)
	})
}

// declareHours adds a row to the site's operating hours and PUTs the whole
// set (the declaration replaces it). A refused declaration leaves the
// scenario's copy of the rows as it was.
func (w *world) declareHours(site string, row map[string]any) error {
	rows := append(append([]map[string]any(nil), w.hours[site]...), row)
	if err := w.call("PUT", "/sites/"+site+"/dock-calendar/operating-hours", map[string]any{"operatingHours": rows}); err != nil {
		return err
	}
	if w.last.status == 200 {
		w.hours[site] = rows
	}
	return nil
}

func (w *world) scheduleClosure(site, door, from, to string) error {
	return w.call("POST", "/sites/"+site+"/dock-calendar/closures", map[string]string{
		"doorCode": door, "from": at(from), "to": at(to), "reason": "maintenance",
	})
}
