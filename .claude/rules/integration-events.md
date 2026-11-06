---
paths:
  - "internal/adapters/**/kafka/**"
  - "internal/adapters/outbound/events/**"
  - "apis/asyncapi*"
---

<!-- TEMPLATE (warehouse-harness-template v2): fill in every "FILL IN" for
     THIS repo, or delete this file if the repo publishes/consumes no Kafka
     events. The "CloudEvents 1.0 is MANDATORY" section is NOT a
     placeholder: keep it verbatim, only substitute the per-repo values. -->
# Cross-service integration events (Kafka)

FILL IN: state whether this service PUBLISHES, CONSUMES, or both, and
to/from which topic(s). Fleet naming: `warehouse.<context>.events`
(integration) and `warehouse.<context>.analytics` (consumed only by this
service's own analytics projector).

## Events: CloudEvents 1.0 is MANDATORY

Every Kafka message this service produces or consumes (integration
`warehouse.<ctx>.events` AND analytics `warehouse.<ctx>.analytics`) is a
CloudEvents 1.0 event in structured content mode. This is a hard fleet rule,
not a preference — there is nothing to "choose" here:

- No flat envelope (`event_id`/`event_type`/`occurred_at`), no dual-write,
  no dual-read, no envelope toggle env var (`EVENT_ENVELOPE_MODE` is gone
  fleet-wide). `internal/architecture/fitness_test.go`'s
  `TestNoEventEnvelopeToggleOrFlatEnvelope` fails CI on any of them.
- Build/validate/(un)marshal with `github.com/cloudevents/sdk-go/v2/event`
  (latest v2) via ONE helper package,
  `internal/adapters/kafka/cloudevents/` — copy it from this template's
  `templates/cloudevents/cloudevents.go.tmpl` (+ its test) and change only
  the per-repo constants. Transport stays `segmentio/kafka-go` (no sdk-go
  protocol/client packages, no hand-rolled CloudEvent structs).
- Every produced message carries the Kafka header
  `content-type: application/cloudevents+json; charset=UTF-8`
  (`cloudevents.ContentTypeHeader()`), next to the W3C trace headers
  (`traceparent`/`tracestate` stay in headers, never duplicated into
  extension attributes). Message key = aggregate id, `kafkago.Hash{}`
  balancer.
- Required attributes: `specversion=1.0`; `id` (UUID v4 minted ONCE per
  domain event and persisted with the outbox row, so redelivery carries
  the same id); `source=/warehouse/<repo>`; `type`; `subject` (aggregate
  instance id, never empty); `time` (domain occurred-at, UTC);
  `datacontenttype=application/json`;
  `dataschema=urn:warehouse:<repo>:<events|analytics>:<EventName>:v<N>`.
  No custom extension attributes without an ADR.
- `type` = `com.warehouse.<subdomain>.<bounded-context>.<entity>.<EventName>`
  (subdomain `wms` or `wes`; FILL IN this repo's exact prefix, e.g.
  `com.warehouse.wes.order-management`). The SAME `type` names the
  occurrence on both the integration and the analytics topic; `dataschema`
  names the payload shape. Breaking payload change => new `.v2` type + new
  dataschema version, never mutate an existing one.
- Consumers decode with `cloudevents.Decode` (validates), dispatch on the
  FULL `type` string (never a short name or suffix match), ignore unknown
  types, read `time`/`subject` from attributes and the payload via
  `DataAs`, dedupe on `id`, and DLQ/skip — WARN log + commit past, never
  crash, never block the partition, never fall back to parsing a legacy
  shape — anything that fails CloudEvents validation.
- Tests: a golden exact-JSON test per published `type` (all attributes +
  the `content-type` header); a legacy-flat-message-rejected test per
  consumer; Kafka integration tests via testcontainers only.

Full standard, subdomain table and the fleet's cross-service type
catalogue: warehouse-docs `docs/strategic-design/event-standard-cloudevents.md`.
Record it in this repo as its own ADR "CloudEvents 1.0 as the mandatory
event envelope" under `docs/docs/adr/`.

### Published types

FILL IN: one row per published event, exact strings.

| `type` | topic(s) | `subject` | `dataschema` |
| --- | --- | --- | --- |
| `com.warehouse.<sub>.<ctx>.<entity>.<EventName>` | `warehouse.<ctx>.events` | aggregate id | `urn:warehouse:<repo>:events:<EventName>:v1` |

### Consumed types

FILL IN: one row per consumed event — the EXACT `type` string from the
producer's catalogue (byte-identical; see the cross-service catalogue on the
Event Standard page).

| `type` | topic | producer |
| --- | --- | --- |

## Consumer group id

If this service consumes Kafka: state where the consumer group id comes
from. It MUST be env-configurable, never a hardcoded string literal --
`internal/architecture/fitness_test.go`'s
TestKafkaConsumerGroupNeverHardcodedInline enforces this (a real incident:
wes-work-planning's hardcoded group id let a locally-run e2e-tests process
silently collide with the live in-cluster Deployment's consumer group on
the shared fleet Kafka broker).

If this consumer replays from FirstOffset on every start to build an
in-memory read model (rather than resuming from a committed offset), the
group id must additionally be UNIQUE PER PROCESS INSTANCE (hostname+PID+
timestamp), not just configurable -- see HARNESS.md's Kafka section for
why a shared group breaks that pattern specifically.
