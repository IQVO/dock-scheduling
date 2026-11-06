package features_test

import (
	"fmt"
	"net/url"
	"reflect"
	"strings"
	"time"

	"github.com/cucumber/godog"
)

// registerAssertions registers the Then steps.
func registerAssertions(sc *godog.ScenarioContext, w *world) {
	registerResponseAssertions(sc, w)
	registerAppointmentAssertions(sc, w)
	registerSystemAssertions(sc, w)
}

func registerResponseAssertions(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^the response status is (\d+)$`, w.wantStatus)
	sc.Step(`^the response is the problem (\S+) with status (\d+)$`, w.wantProblem)
	sc.Step(`^the problem detail names appointment "([^"]+)"$`, w.wantDetailNames)
	sc.Step(`^the response carries the header (\S+) (.+)$`, w.wantHeader)
	sc.Step(`^the second response replays the first$`, w.wantReplay)
}

func (w *world) wantStatus(want int) error {
	if w.last.status != want {
		return fmt.Errorf("status = %d, want %d; body: %s", w.last.status, want, w.last.body)
	}
	return nil
}

func (w *world) wantProblem(slug string, status int) error {
	if err := w.wantStatus(status); err != nil {
		return err
	}
	if ct := w.last.header.Get("Content-Type"); ct != "application/problem+json" {
		return fmt.Errorf("content type = %q, want application/problem+json", ct)
	}
	m, err := w.json()
	if err != nil {
		return err
	}
	if want := "https://errors.dock-scheduling.warehouse-systems.dev/" + slug; m["type"] != want {
		return fmt.Errorf("problem type = %v, want %s", m["type"], want)
	}
	return nil
}

func (w *world) wantDetailNames(alias string) error {
	id, err := w.id(alias)
	if err != nil {
		return err
	}
	if !strings.Contains(string(w.last.body), id) {
		return fmt.Errorf("the problem does not name %s: %s", id, w.last.body)
	}
	return nil
}

func (w *world) wantHeader(name, want string) error {
	if got := w.last.header.Get(name); got != want {
		return fmt.Errorf("header %s = %q, want %q", name, got, want)
	}
	return nil
}

func (w *world) wantReplay() error {
	if w.last.header.Get("Idempotency-Replayed") != "true" {
		return fmt.Errorf("the second response is not marked Idempotency-Replayed: true")
	}
	if w.prev.header.Get("Idempotency-Replayed") != "" {
		return fmt.Errorf("the first response must not be marked as a replay")
	}
	if w.last.status != w.prev.status || string(w.last.body) != string(w.prev.body) {
		return fmt.Errorf("the replay differs from the first answer:\n%s\n%s", w.prev.body, w.last.body)
	}
	return nil
}

func registerAppointmentAssertions(sc *godog.ScenarioContext, w *world) {
	field := func(pattern string, name string, want any) {
		sc.Step(pattern, func(alias string) error { return w.wantField(alias, name, want) })
	}
	for _, s := range []string{"BOOKED", "CHECKED_IN", "COMPLETED", "CANCELLED", "NO_SHOW"} {
		field(`^appointment "([^"]+)" is `+s+`$`, "state", s)
	}
	field(`^appointment "([^"]+)" is not at risk$`, "atRisk", false)
	field(`^appointment "([^"]+)" has no arrival$`, "arrival", nil)
	for _, cause := range []string{"LATE_ETA", "DOOR_REASSIGNED_OVERLAP"} {
		field(`^appointment "([^"]+)" is at risk because `+cause+`$`, "atRiskCause", cause)
	}
	for _, reason := range []string{"CARRIER_REQUEST", "SITE_REQUEST", "ASN_CANCELLED", "TURNED_AWAY"} {
		field(`^appointment "([^"]+)" was cancelled for reason `+reason+`$`, "cancellationReason", reason)
	}
	sc.Step(`^appointment "([^"]+)" is on door (\S+)$`, func(alias, door string) error { return w.wantField(alias, "doorCode", door) })
	sc.Step(`^appointment "([^"]+)" is at version (\d+)$`, func(alias string, v int) error { return w.wantField(alias, "version", float64(v)) })
	sc.Step(`^appointment "([^"]+)" has arrival status (EARLY|ON_TIME|LATE)( implicitly)?$`, w.wantArrival)
	sc.Step(`^appointment "([^"]+)" runs from (\d\d:\d\d) to (\d\d:\d\d)$`, w.wantWindow)
}

func (w *world) wantArrival(alias, status, implicit string) error {
	m, err := w.appointment(alias)
	if err != nil {
		return err
	}
	arrival, _ := m["arrival"].(map[string]any)
	if arrival == nil || arrival["status"] != status || arrival["implicit"] != (implicit != "") {
		return fmt.Errorf("appointment %q arrival = %v, want status %s implicit=%v", alias, arrival, status, implicit != "")
	}
	return nil
}

func (w *world) wantWindow(alias, from, to string) error {
	if err := w.wantField(alias, "windowStart", at(from)); err != nil {
		return err
	}
	return w.wantField(alias, "windowEnd", at(to))
}

func registerSystemAssertions(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^door (\S+) has (\d+) active appointments?$`, w.wantActive)
	sc.Step(`^the free windows of door (\S+) from (\d\d:\d\d) to (\d\d:\d\d) are (.+)$`, w.wantFreeWindows)
	sc.Step(`^the sweep marked (\d+) appointments?$`, w.wantSweepCount)
	sc.Step(`^the sweep marked appointment "([^"]+)"$`, w.wantSweepMarked)
	sc.Step(`^(\d+) events? (?:has|have) been published$`, w.wantEventCount)
	sc.Step(`^the published events are (.+)$`, w.wantEvents)
	sc.Step(`^the ignored-fact count for (\S+) is (\d+)$`, w.wantIgnored)
	sc.Step(`^(\d+) implicit arrivals? (?:was|were) counted$`, w.wantImplicit)
}

func (w *world) wantActive(door string, want int) error {
	got, err := w.activeOn(door)
	if err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("door %s has %d active appointments, want %d", door, got, want)
	}
	return nil
}

func (w *world) wantFreeWindows(door, from, to, want string) error {
	q := url.Values{"from": {at(from)}, "to": {at(to)}}
	if err := w.call("GET", "/doors/"+door+"/availability?"+q.Encode(), nil); err != nil {
		return err
	}
	m, err := w.json()
	if err != nil {
		return err
	}
	var spans []string
	free, _ := m["freeWindows"].([]any)
	for _, f := range free {
		s := f.(map[string]any)
		spans = append(spans, hhmm(s["start"])+"-"+hhmm(s["end"]))
	}
	got := "none"
	if len(spans) > 0 {
		got = strings.Join(spans, ", ")
	}
	if got != want {
		return fmt.Errorf("free windows of %s = %s, want %s", door, got, want)
	}
	return nil
}

func (w *world) wantSweepCount(want int) error {
	if len(w.sweep) != want {
		return fmt.Errorf("the sweep marked %d appointments (%v), want %d", len(w.sweep), w.sweep, want)
	}
	return nil
}

func (w *world) wantSweepMarked(alias string) error {
	id, err := w.id(alias)
	if err != nil {
		return err
	}
	for _, marked := range w.sweep {
		if marked == id {
			return nil
		}
	}
	return fmt.Errorf("the sweep marked %v, not %s", w.sweep, id)
}

func (w *world) wantEventCount(want int) error {
	if got := len(w.store.Published()); got != want {
		return fmt.Errorf("%d events published (%v), want %d", got, w.eventNames(), want)
	}
	return nil
}

func (w *world) wantEvents(raw string) error {
	var want []string
	for _, n := range strings.Split(raw, ",") {
		want = append(want, strings.TrimSpace(n))
	}
	if got := w.eventNames(); !reflect.DeepEqual(got, want) {
		return fmt.Errorf("published events = %v, want %v", got, want)
	}
	return nil
}

func (w *world) wantIgnored(reason string, want int) error {
	if got := w.metrics.Ignored(reason); got != want {
		return fmt.Errorf("ignored facts %q = %d, want %d", reason, got, want)
	}
	return nil
}

func (w *world) wantImplicit(want int) error {
	if got := w.metrics.Implicit(); got != want {
		return fmt.Errorf("implicit arrivals = %d, want %d", got, want)
	}
	return nil
}

// appointment fetches an aliased appointment over REST without disturbing
// the last response a scenario asserts on.
func (w *world) appointment(alias string) (map[string]any, error) {
	id, err := w.id(alias)
	if err != nil {
		return nil, err
	}
	last, prev := w.last, w.prev
	defer func() { w.last, w.prev = last, prev }()
	if err := w.call("GET", "/appointments/"+id, nil); err != nil {
		return nil, err
	}
	return w.json()
}

func (w *world) wantField(alias, name string, want any) error {
	m, err := w.appointment(alias)
	if err != nil {
		return err
	}
	if got := m[name]; !reflect.DeepEqual(got, want) {
		return fmt.Errorf("appointment %q %s = %v, want %v", alias, name, got, want)
	}
	return nil
}

// eventNames lists the names of the domain events enqueued so far.
func (w *world) eventNames() []string {
	var out []string
	for _, ev := range w.store.Published() {
		if named, ok := ev.(interface{ EventName() string }); ok {
			out = append(out, named.EventName())
		}
	}
	return out
}

// hhmm renders an RFC 3339 JSON value as HH:MM.
func hhmm(v any) string {
	t, err := time.Parse(time.RFC3339, fmt.Sprint(v))
	if err != nil {
		return fmt.Sprint(v)
	}
	return t.UTC().Format("15:04")
}
