package features_test

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/cucumber/godog"
)

// registerCarrierSteps registers the carrier registry commands of the
// carrier_registry feature, all over REST.
func registerCarrierSteps(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^carrier (\S+) named "([^"]*)" is registered with USDOT "([^"]*)" and MC "([^"]*)"$`, w.registerCarrier)
	sc.Step(`^carrier (\S+) is suspended with reason "([^"]*)"$`, func(scac, reason string) error { return w.suspendCarrier(scac, reason) })
	sc.Step(`^carrier (\S+) is suspended with reason "([^"]*)" if its version is (\d+)$`, func(scac, reason, version string) error {
		w.ifMatch = fmt.Sprintf(`W/"%s"`, version)
		return w.suspendCarrier(scac, reason)
	})
	sc.Step(`^carrier (\S+) is reinstated$`, func(scac string) error { return w.call("POST", "/carriers/"+scac+"/reinstatements", nil) })
	sc.Step(`^carrier (\S+) is renamed to "([^"]*)"$`, func(scac, name string) error {
		return w.call("PATCH", "/carriers/"+scac, map[string]string{"name": name})
	})
	sc.Step(`^carrier (\S+) is fetched$`, func(scac string) error { return w.call("GET", "/carriers/"+scac, nil) })
	sc.Step(`^the carriers are listed$`, func() error { return w.call("GET", "/carriers", nil) })
	sc.Step(`^the carriers with status (ACTIVE|SUSPENDED) are listed$`, func(status string) error {
		return w.call("GET", "/carriers?"+url.Values{"status": {status}}.Encode(), nil)
	})
	sc.Step(`^an? (INBOUND|OUTBOUND) booking naming no carrier SCAC is requested on door (\S+) from (\d\d:\d\d) to (\d\d:\d\d) for (\S+)$`,
		func(direction, door, from, to, ref string) error {
			body := bookingBody(direction, door, from, to, ref)
			delete(body, "carrierScac")
			return w.call("POST", "/appointments", body)
		})

	sc.Step(`^the carrier has (name|usdot|mc|status|suspensionReason) "([^"]*)"$`, w.wantCarrierText)
	sc.Step(`^the carrier is at version (\d+)$`, func(v int) error { return w.wantCarrierField("version", float64(v)) })
	sc.Step(`^the carrier list is (.+)$`, w.wantCarrierList)
	sc.Step(`^the response body mentions "([^"]+)"$`, func(text string) error {
		if !strings.Contains(string(w.last.body), text) {
			return fmt.Errorf("the response body does not mention %q: %s", text, w.last.body)
		}
		return nil
	})
}

func (w *world) registerCarrier(scac, name, usdot, mc string) error {
	body := map[string]any{"name": name}
	if usdot != "" {
		body["usdot"] = usdot
	}
	if mc != "" {
		body["mc"] = mc
	}
	return w.call("PUT", "/carriers/"+scac, body)
}

func (w *world) suspendCarrier(scac, reason string) error {
	return w.call("POST", "/carriers/"+scac+"/suspensions", map[string]string{"reason": reason})
}

// wantCarrierField checks one field of the last body; an absent or null
// optional value reads as the empty string.
func (w *world) wantCarrierField(field string, want any) error {
	m, err := w.json()
	if err != nil {
		return err
	}
	got := m[field]
	if got == nil && want == "" {
		return nil
	}
	if got != want {
		return fmt.Errorf("carrier %s = %v, want %v (body %s)", field, got, want, w.last.body)
	}
	return nil
}

func (w *world) wantCarrierText(field, want string) error { return w.wantCarrierField(field, want) }

// wantCarrierList checks the SCACs of the last list page, in order.
func (w *world) wantCarrierList(raw string) error {
	m, err := w.json()
	if err != nil {
		return err
	}
	items, _ := m["items"].([]any)
	var got []string
	for _, it := range items {
		c, _ := it.(map[string]any)
		scac, _ := c["scac"].(string)
		got = append(got, scac)
	}
	if want := strings.Join(strings.Split(raw, ", "), ","); strings.Join(got, ",") != want {
		return fmt.Errorf("carrier list = %v, want %s", got, raw)
	}
	return nil
}
