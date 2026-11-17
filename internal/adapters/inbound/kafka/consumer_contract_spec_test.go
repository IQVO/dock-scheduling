package kafka

// Shared support for the event consumer contract tests (E1-E4), which
// validate this repo's five inbound Kafka edges — the four producer topics
// (facility-layout, inbound-receiving, process-path-management,
// yard-management) plus this service's own analytics stream — against
// PINNED copies of the producing side's AsyncAPI spec
// (contracts/pinned.json; the pinned-contracts CI job fails when a copy
// drifts from the producer's develop). The pinned copies are byte-for-byte
// producer files: never edit them — refresh = copy the producer's file
// over the pin.
//
// The parser below reads the minimal slice of AsyncAPI the contract tests
// need and tolerates BOTH fleet document shapes:
//
//   - 2.6: channels are keyed by the topic itself; a producer's own topic
//     sits under 'subscribe:' (channel-perspective) or 'publish:'
//     (app-perspective — process-path-management), with the message list
//     as a single $ref or a oneOf of $refs into components/messages;
//   - 3.0 (yard-management): channels are keyed by a logical name with the
//     topic in 'address', and the messages hang off the channel itself.
//
// The consumed side is always derived from THIS repo's reader config
// (the *Topic constants in the consumer files), never from the spec's
// operations: the pinned spec is the message catalog.

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The pinned spec copies next to this consuming adapter (all registered in
// contracts/pinned.json; the self-pin keeps the projector's decode contract
// and this repo's publisher spec from drifting apart silently).
const (
	pinnedFacilitySpec    = "testdata/facility-layout.asyncapi.yaml"
	pinnedInboundSpec     = "testdata/inbound-receiving.asyncapi.yaml"
	pinnedProcessPathSpec = "testdata/process-path-management.asyncapi.yaml"
	pinnedYardSpec        = "testdata/yard-management.asyncapi.yaml"
	pinnedOwnSpec         = "testdata/dock-scheduling.asyncapi.yaml"
)

// pinnedAsyncAPI is the minimal slice of AsyncAPI the contract tests read.
type pinnedAsyncAPI struct {
	AsyncAPI           string                   `yaml:"asyncapi"`
	DefaultContentType string                   `yaml:"defaultContentType"`
	Channels           map[string]pinnedChannel `yaml:"channels"`
	Components         pinnedComponents         `yaml:"components"`
}

// pinnedChannel covers both fleet shapes: 2.6 (the topic IS the map key,
// operations under subscribe/publish) and 3.0 (a logical key, the topic in
// Address, the messages listed on the channel itself).
type pinnedChannel struct {
	Address   string                      `yaml:"address"`
	Subscribe *pinnedOperation            `yaml:"subscribe"`
	Publish   *pinnedOperation            `yaml:"publish"`
	Messages  map[string]pinnedMessageRef `yaml:"messages"`
}

type pinnedOperation struct {
	OperationID string            `yaml:"operationId"`
	Message     *pinnedMessageRef `yaml:"message"`
}

// pinnedMessageRef is either a single $ref or a oneOf list of $refs.
type pinnedMessageRef struct {
	Ref   string             `yaml:"$ref"`
	OneOf []pinnedMessageRef `yaml:"oneOf"`
}

type pinnedComponents struct {
	Messages map[string]pinnedMessage `yaml:"messages"`
	Schemas  map[string]any           `yaml:"schemas"`
}

type pinnedMessage struct {
	Name        string          `yaml:"name"`
	ContentType string          `yaml:"contentType"`
	Payload     any             `yaml:"payload"`
	Examples    []pinnedExample `yaml:"examples"`
}

// pinnedExample is a message example: a full wire payload (a structured
// CloudEvents JSON document, as YAML) plus its optional Kafka headers
// (2.6 message examples carry them; 3.0 ones may not).
type pinnedExample struct {
	Name    string            `yaml:"name"`
	Headers map[string]string `yaml:"headers"`
	Payload map[string]any    `yaml:"payload"`
}

// loadPinnedAsyncAPI reads and parses one pinned spec copy.
func loadPinnedAsyncAPI(t *testing.T, path string) *pinnedAsyncAPI {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read pinned spec %s (copy it from the producer, never hand-write it): %v", path, err)
	}
	var doc pinnedAsyncAPI
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse pinned spec %s: %v", path, err)
	}
	return &doc
}

// channel returns the channel documenting topic, matched by map key (2.6)
// or by address (3.0).
func (d *pinnedAsyncAPI) channel(t *testing.T, topic string) pinnedChannel {
	t.Helper()
	if ch, ok := d.Channels[topic]; ok {
		return ch
	}
	for _, ch := range d.Channels {
		if ch.Address == topic {
			return ch
		}
	}
	t.Fatalf("pinned spec %s has no channel for topic %q", d.AsyncAPI, topic)
	return pinnedChannel{}
}

// messageNames returns the components/messages names a topic documents, in
// document order and de-duplicated. Both operations and the 3.0 channel
// message list are considered: this repo consumes the topic, so what
// matters is the channel's message catalog, not which side the spec's
// operations name (fleet specs are not uniform about that).
func (d *pinnedAsyncAPI) messageNames(t *testing.T, topic string) []string {
	t.Helper()
	ch := d.channel(t, topic)
	var names []string
	seen := map[string]bool{}
	add := func(refs ...pinnedMessageRef) {
		for _, r := range refs {
			if r.Ref == "" {
				continue
			}
			name := refName(t, r.Ref)
			if !seen[name] {
				seen[name] = true
				names = append(names, name)
			}
		}
	}
	for _, op := range []*pinnedOperation{ch.Subscribe, ch.Publish} {
		if op == nil || op.Message == nil {
			continue
		}
		add(*op.Message)
		add(op.Message.OneOf...)
	}
	for _, name := range sortedKeys(ch.Messages) {
		add(ch.Messages[name])
	}
	if len(names) == 0 {
		t.Fatalf("pinned spec documents topic %q without any message", topic)
	}
	return names
}

// sortedKeys is a deterministic iteration order for a message-name map.
func sortedKeys(m map[string]pinnedMessageRef) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}

// message resolves one components/messages entry, failing on a dangling
// $ref (a spec whose channel points at a message it does not define).
func (d *pinnedAsyncAPI) message(t *testing.T, name string) pinnedMessage {
	t.Helper()
	msg, ok := d.Components.Messages[name]
	if !ok {
		t.Fatalf("pinned spec references undefined components/messages/%s", name)
	}
	return msg
}

// documentedTypes returns the CloudEvents `type` each message on the topic
// documents, wherever the spec pins it: a payload `type` const (most
// producers), or the `type` of the message's own examples (inbound-receiving
// and this repo's own spec declare no consts — their examples carry the
// type). A channel that documents no type at all fails the call.
func (d *pinnedAsyncAPI) documentedTypes(t *testing.T, topic string) map[string]bool {
	t.Helper()
	documented := map[string]bool{}
	for _, name := range d.messageNames(t, topic) {
		msg := d.message(t, name) // fails the test on a dangling $ref
		if c := d.payloadConsts(t, msg.Payload)["type"]; c != "" {
			documented[c] = true
		}
		for _, ex := range msg.Examples {
			if ty, _ := ex.Payload["type"].(string); ty != "" {
				documented[ty] = true
			}
		}
	}
	if len(documented) == 0 {
		t.Fatalf("pinned spec documents no CloudEvents type on topic %q", topic)
	}
	return documented
}

// goldenExamples returns the pinned spec's own examples for one message,
// failing when it has none (E2 needs a wire document; the PR body lists
// the types whose producer documents none).
func (d *pinnedAsyncAPI) goldenExamples(t *testing.T, message string) []pinnedExample {
	t.Helper()
	msg := d.message(t, message)
	if len(msg.Examples) == 0 {
		t.Fatalf("pinned spec documents %s without examples", message)
	}
	return msg.Examples
}

// payloadConsts collects the const values a message payload declares for
// CloudEvents envelope attributes (type, dataschema, ...), wherever the
// spec keeps them: directly under properties, in an allOf overlay on the
// shared envelope schema, or behind a payload-level $ref into
// components/schemas.
func (d *pinnedAsyncAPI) payloadConsts(t *testing.T, payload any) map[string]string {
	t.Helper()
	consts := map[string]string{}
	d.collectPayloadConsts(t, payload, consts, map[string]bool{})
	return consts
}

func (d *pinnedAsyncAPI) collectPayloadConsts(t *testing.T, node any, consts map[string]string, resolving map[string]bool) {
	v, ok := node.(map[string]any)
	if !ok {
		return
	}
	if schema, ok := d.resolveSchemaRef(t, v, resolving); ok {
		d.collectPayloadConsts(t, schema, consts, resolving)
		return
	}
	for attr, c := range propertyConsts(v) {
		if _, seen := consts[attr]; !seen {
			consts[attr] = c
		}
	}
	for _, item := range allOfItems(v) {
		d.collectPayloadConsts(t, item, consts, resolving)
	}
}

// resolveSchemaRef resolves node's top-level $ref into components/schemas
// (cycle-guarded). ok is false when node carries no $ref, or the ref is
// already being resolved.
func (d *pinnedAsyncAPI) resolveSchemaRef(t *testing.T, node map[string]any, resolving map[string]bool) (any, bool) {
	ref, ok := node["$ref"].(string)
	if !ok {
		return nil, false
	}
	name := refName(t, ref)
	if resolving[name] {
		return nil, false
	}
	resolving[name] = true
	schema, ok := d.Components.Schemas[name]
	if !ok {
		t.Fatalf("pinned spec: unresolved schema $ref %q", ref)
	}
	return schema, true
}

// propertyConsts returns the const string each directly-declared property
// of node pins (empty when node pins none).
func propertyConsts(node map[string]any) map[string]string {
	consts := map[string]string{}
	props, ok := node["properties"].(map[string]any)
	if !ok {
		return consts
	}
	for attr, p := range props {
		if pm, ok := p.(map[string]any); ok {
			if c, ok := pm["const"].(string); ok {
				consts[attr] = c
			}
		}
	}
	return consts
}

// allOfItems returns node's allOf entries (nil when none).
func allOfItems(node map[string]any) []any {
	items, _ := node["allOf"].([]any)
	return items
}

// envelopeRequired returns the required attribute names declared by a
// message payload's envelope schema (resolving top-level $refs into
// components/schemas). The subset naming CloudEvents attributes is what
// E3 asserts on.
func (d *pinnedAsyncAPI) envelopeRequired(t *testing.T, payload any) map[string]bool {
	t.Helper()
	required := map[string]bool{}
	d.collectRequired(t, payload, required, map[string]bool{})
	return required
}

func (d *pinnedAsyncAPI) collectRequired(t *testing.T, node any, required map[string]bool, resolving map[string]bool) {
	v, ok := node.(map[string]any)
	if !ok {
		return
	}
	for _, r := range requiredList(v) {
		required[r] = true
	}
	if schema, ok := d.resolveSchemaRef(t, v, resolving); ok {
		d.collectRequired(t, schema, required, resolving)
		return
	}
	for _, item := range allOfItems(v) {
		d.collectRequired(t, item, required, resolving)
	}
}

// requiredList returns node's required attribute names (nil when none).
func requiredList(node map[string]any) []string {
	raw, ok := node["required"].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, r := range raw {
		if s, ok := r.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// refName extracts the last path segment of a local $ref
// ("#/components/messages/ZoneRegistered" -> "ZoneRegistered").
func refName(t *testing.T, ref string) string {
	t.Helper()
	i := strings.LastIndex(ref, "/")
	if i < 0 || i == len(ref)-1 {
		t.Fatalf("malformed local $ref %q", ref)
	}
	return ref[i+1:]
}

// goldenJSON marshals a pinned example's YAML-decoded payload into the
// JSON bytes a Kafka consumer actually receives (structured-mode
// CloudEvents: the message value IS the JSON envelope).
func goldenJSON(t *testing.T, payload map[string]any) []byte {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal golden payload: %v", err)
	}
	return raw
}

// clonePayload deep-copies a golden example payload so a test can mutate
// it without touching the shared fixture.
func clonePayload(p map[string]any) map[string]any {
	return cloneMap(p).(map[string]any)
}

func cloneMap(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[k] = cloneMap(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = cloneMap(val)
		}
		return out
	default:
		return v
	}
}

// goldenID returns a golden example's CloudEvents id ("" when absent).
func goldenID(ex pinnedExample) string {
	id, _ := ex.Payload["id"].(string)
	return id
}

// goldenData returns a golden example's data map (nil when absent).
func goldenData(ex pinnedExample) map[string]any {
	data, _ := ex.Payload["data"].(map[string]any)
	return data
}

// assertEnvelopeRequired asserts the pinned spec's envelope schema for a
// message requires the full fleet-mandatory CloudEvents attribute set.
func assertEnvelopeRequired(t *testing.T, doc *pinnedAsyncAPI, message string) {
	t.Helper()
	msg := doc.message(t, message)
	for _, attr := range []string{"specversion", "id", "source", "type", "subject", "time", "datacontenttype", "dataschema", "data"} {
		if !doc.envelopeRequired(t, msg.Payload)[attr] {
			t.Errorf("pinned spec's %s envelope does not require %q", message, attr)
		}
	}
}
