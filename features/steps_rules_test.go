package features_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"regexp"
	"strconv"
	"strings"

	"github.com/cucumber/godog"
)

// registerRulesSteps registers the vocabulary of the documentation-derived
// features (booking_rules, lifecycle_rules, yard_rules, calendar_rules). It adds
// generic REST steps with dotted-path assertions, field-list bookings (so one
// Scenario Outline can walk a boundary), explicit-id yard deliveries (to replay
// a CloudEvents id), and the arrangement of an appointment into a lifecycle
// state. Domain-worded steps stay where the earlier features put them.
func registerRulesSteps(sc *godog.ScenarioContext, w *world) {
	st := &deliveries{sent: map[string]func() error{}}
	sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
		st.sent = map[string]func() error{}
		return ctx, nil
	})
	registerRequestSteps(sc, w)
	registerBodyAssertions(sc, w)
	registerFactAssertions(sc, w)
	registerFieldSteps(sc, w)
	registerArrangeSteps(sc, w)
	registerConsumerSteps(sc, w, st)
}

var (
	timeToken  = regexp.MustCompile(`@(\d\d:\d\d)`)
	aliasToken = regexp.MustCompile(`\{(closure:)?([A-Za-z][A-Za-z0-9 _-]*)\}`)
)

// expand resolves the tokens a step may carry: {alias} is the id of a booked
// appointment, {closure:alias} the id of a scheduled closure and @HH:MM an
// instant of the scenario day.
func (w *world) expand(s string) (string, error) {
	var missing error
	s = aliasToken.ReplaceAllStringFunc(s, func(m string) string {
		sub := aliasToken.FindStringSubmatch(m)
		ids := w.appointments
		if sub[1] != "" {
			ids = w.closures
		}
		id, ok := ids[sub[2]]
		if !ok {
			missing = fmt.Errorf("no %q was created in this scenario", sub[2])
			return m
		}
		return id
	})
	return timeToken.ReplaceAllString(s, scenarioDay+"T$1:00Z"), missing
}

func registerRequestSteps(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^an? (GET|POST|PUT) request is sent to "([^"]*)"$`, func(method, path string) error { return w.sendJSON(method, path, "") })
	sc.Step(`^an? (GET|POST|PUT) request is sent to "([^"]*)" with the JSON body (.+)$`, w.sendJSON)
	sc.Step(`^the (GET|POST|PUT) request to "([^"]*)" with the JSON body (.+) has been accepted$`, func(method, path, body string) error {
		if err := w.sendJSON(method, path, body); err != nil {
			return err
		}
		if w.last.status/100 != 2 {
			return fmt.Errorf("%s %s answered %d, want 2xx: %s", method, path, w.last.status, w.last.body)
		}
		return nil
	})
	sc.Step(`^a POST request with no Idempotency-Key is sent to "([^"]*)" with the JSON body (.+)$`, w.sendWithoutKey)
}

// sendJSON sends the request through the world's usual call (a POST gets a
// fresh Idempotency-Key); body is a JSON document or empty.
func (w *world) sendJSON(method, path, body string) error {
	p, err := w.expand(path)
	if err != nil {
		return err
	}
	if body == "" {
		return w.call(method, p, nil)
	}
	b, err := w.expand(body)
	if err != nil {
		return err
	}
	return w.call(method, p, json.RawMessage(b))
}

// sendWithoutKey POSTs without any Idempotency-Key header.
func (w *world) sendWithoutKey(path, body string) error {
	p, err := w.expand(path)
	if err != nil {
		return err
	}
	b, err := w.expand(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(w.ctx, http.MethodPost, w.server.URL+p, strings.NewReader(b))
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	w.prev = w.last
	w.last = response{status: resp.StatusCode, header: resp.Header, body: raw}
	return nil
}

func registerBodyAssertions(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^the response field "([^"]+)" is (.+)$`, w.wantBodyField)
	sc.Step(`^the response has no field "([^"]+)"$`, func(path string) error {
		got, present, err := w.bodyField(path)
		if err != nil {
			return err
		}
		if present {
			return fmt.Errorf("the response carries %s = %v, want it absent", path, got)
		}
		return nil
	})
	sc.Step(`^the response list "([^"]*)" has (\d+) items?$`, func(path string, want int) error {
		got, present, err := w.bodyField(path)
		if err != nil {
			return err
		}
		items, ok := got.([]any)
		if !present || !ok || len(items) != want {
			return fmt.Errorf("the response list %q = %v (present %v), want %d items", path, got, present, want)
		}
		return nil
	})
}

// bodyField walks a dotted path ("arrival.visitId", "busy.1.kind") through the
// last JSON body; an empty path is the body itself.
func (w *world) bodyField(path string) (value any, present bool, err error) {
	var cur any
	if err := json.Unmarshal(w.last.body, &cur); err != nil {
		return nil, false, fmt.Errorf("the last body is not JSON (%d): %s", w.last.status, w.last.body)
	}
	if path == "" {
		return cur, true, nil
	}
	for _, part := range strings.Split(path, ".") {
		switch node := cur.(type) {
		case map[string]any:
			next, ok := node[part]
			if !ok {
				return nil, false, nil
			}
			cur = next
		case []any:
			i, convErr := strconv.Atoi(part)
			if convErr != nil || i < 0 || i >= len(node) {
				return nil, false, nil
			}
			cur = node[i]
		default:
			return nil, false, nil
		}
	}
	return cur, true, nil
}

func (w *world) wantBodyField(path, literal string) error {
	want, err := w.literal(literal)
	if err != nil {
		return err
	}
	got, present, err := w.bodyField(path)
	if err != nil {
		return err
	}
	if !present || !reflect.DeepEqual(got, want) {
		return fmt.Errorf("response field %s = %v (present %v), want %v; body %s", path, got, present, want, w.last.body)
	}
	return nil
}

// literal decodes a JSON literal of a step (after expanding its tokens).
func (w *world) literal(text string) (any, error) {
	expanded, err := w.expand(text)
	if err != nil {
		return nil, err
	}
	var v any
	if err := json.Unmarshal([]byte(expanded), &v); err != nil {
		return nil, fmt.Errorf("%q is not a JSON literal: %w", text, err)
	}
	return v, nil
}

func registerFactAssertions(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^the (Dock\w+) fact of appointment "([^"]+)" carries (\w+) (.+)$`, func(event, alias, field, literal string) error {
		data, _, err := w.fact(event, alias)
		if err != nil {
			return err
		}
		return w.wantDataField(event+" of "+alias, data, field, literal)
	})
	sc.Step(`^the (Dock\w+) fact of appointment "([^"]+)" lacks the field (\w+)$`, func(event, alias, field string) error {
		data, _, err := w.fact(event, alias)
		if err != nil {
			return err
		}
		if got, present := data[field]; present {
			return fmt.Errorf("%s of %s carries %s = %v, want it absent", event, alias, field, got)
		}
		return nil
	})
	sc.Step(`^the latest (Door\w+) fact carries (\w+) (.+)$`, func(event, field, literal string) error {
		wire := w.store.PublishedWire()
		for i := len(wire) - 1; i >= 0; i-- {
			if wire[i].Event != event {
				continue
			}
			var data map[string]any
			if err := json.Unmarshal(wire[i].Data, &data); err != nil {
				return err
			}
			return w.wantDataField(event, data, field, literal)
		}
		return fmt.Errorf("no %s fact was published; published: %v", event, w.eventNames())
	})
	sc.Step(`^(\d+) (Dock\w+|Door\w+) events? (?:was|were) published$`, func(want int, event string) error {
		got := 0
		for _, name := range w.eventNames() {
			if name == event {
				got++
			}
		}
		if got != want {
			return fmt.Errorf("%d %s events published, want %d (published: %v)", got, event, want, w.eventNames())
		}
		return nil
	})
}

func (w *world) wantDataField(what string, data map[string]any, field, literal string) error {
	want, err := w.literal(literal)
	if err != nil {
		return err
	}
	got, present := data[field]
	if !present || !reflect.DeepEqual(got, want) {
		return fmt.Errorf("%s: %s = %v (present %v), want %v; data %v", what, field, got, present, want, data)
	}
	return nil
}
