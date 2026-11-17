package features_test

import (
	"fmt"
	"time"

	"github.com/cucumber/godog"

	"github.com/claudioed/dock-scheduling/internal/application/repository"
	"github.com/claudioed/dock-scheduling/internal/domain/shared"
)

// registerSetupSteps registers the clock and the local-copy seeds: the
// facility-layout doors, the inbound-receiving ASNs and the CPT cutoffs that
// ADR 0003's Kafka consumers fill in production.
func registerSetupSteps(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^the clock is (\d{4}-\d\d-\d\dT\S+)$`, func(raw string) error {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return err
		}
		w.clock.now = t.UTC()
		return nil
	})

	sc.Step(`^the clock is (\d\d:\d\d)$`, func(hhmm string) error {
		t, err := atTime(hhmm)
		if err != nil {
			return err
		}
		w.clock.now = t
		return nil
	})

	sc.Step(`^time advances (\d+) (minutes|hours)$`, func(n int, unit string) error {
		d := time.Duration(n) * time.Minute
		if unit == "hours" {
			d = time.Duration(n) * time.Hour
		}
		w.clock.now = w.clock.now.Add(d)
		return nil
	})

	sc.Step(`^site (\S+) has door (\S+) taking (Inbound|Outbound|Both) flows$`, func(site, door, flow string) error {
		zone := "zone-" + site
		if err := w.store.Doors().UpsertZone(w.ctx, zone, site); err != nil {
			return err
		}
		f, err := shared.ParseDockFlow(flow)
		if err != nil {
			return err
		}
		return w.store.Doors().UpsertDoor(w.ctx, door, zone, f, "")
	})

	sc.Step(`^ASN (\S+) is registered$`, func(number string) error {
		return w.store.Asns().Register(w.ctx, number, w.clock.now)
	})

	sc.Step(`^CPT cutoff (\S+) exists at site (\S+)$`, func(cutoff, site string) error {
		return w.store.Cutoffs().Replace(w.ctx, site, "UTC", []repository.Cutoff{{ID: cutoff}})
	})

	sc.Step(`^inbound-receiving cancels ASN (\S+)$`, func(number string) error {
		return w.svc.ConsumeAsnCancelled(w.ctx, w.eventID(), number)
	})

	sc.Step(`^site (\S+) declares the settings window (\d+), granularity (\d+), tolerance (\d+) and no-show grace (\d+) minutes$`,
		func(site string, window, grain, tolerance, grace int) error {
			return w.call("PUT", "/sites/"+site+"/dock-calendar/settings", map[string]int{
				"maxWindowMinutes": window, "slotGranularityMinutes": grain, "arrivalToleranceMinutes": tolerance, "noShowGraceMinutes": grace,
			})
		})
}

// bookingBody is the REST body of a booking; the last token is the ASN of an
// inbound booking and the CPT cutoff of an outbound one.
func bookingBody(direction, door, from, to, ref string) map[string]any {
	b := map[string]any{
		"direction": direction, "doorCode": door, "carrier": "ACME Freight", "carrierScac": "ACMF",
		"windowStart": at(from), "windowEnd": at(to),
	}
	if direction == "INBOUND" {
		b["asnNumbers"] = []string{ref}
	} else {
		b["cptId"] = ref
	}
	return b
}

// remember keeps the id of the appointment the last response created.
func (w *world) remember(alias string) error {
	if w.last.status != 201 {
		return fmt.Errorf("booking %q answered %d, want 201: %s", alias, w.last.status, w.last.body)
	}
	m, err := w.json()
	if err != nil {
		return err
	}
	w.appointments[alias], _ = m["appointmentId"].(string)
	return nil
}

// id resolves an appointment alias.
func (w *world) id(alias string) (string, error) {
	id, ok := w.appointments[alias]
	if !ok {
		return "", fmt.Errorf("no appointment %q was booked in this scenario", alias)
	}
	return id, nil
}
