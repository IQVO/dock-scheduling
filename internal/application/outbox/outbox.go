// Package outbox holds the neutral types of the transactional outbox's relay
// side: what a persisted outbox row looks like once decoded (Row/Fact) and
// what a Kafka-bound message looks like (Message). The types live here — not
// in ports (interfaces only) and not in an adapter — so the Postgres store,
// the Kafka encoder/sink and the relay itself share them without any adapter
// importing another adapter (only cmd/ wires adapters together).
//
// Delivery is at-least-once: a row is only removed after its message was
// accepted by the broker, and the CloudEvents id is minted ONCE at enqueue
// (OutboxPublisher) and persisted with the row, so a relay retry republishes
// the same id and consumers dedupe on it.
package outbox

import (
	"encoding/json"
	"time"
)

// Header is one Kafka header of a message.
type Header struct {
	Key   string
	Value string
}

// Message is one Kafka-ready message: the CloudEvents 1.0 JSON value plus the
// columns the writer publishes it with. Topic is per message (the Writer
// itself carries none — kafka-go allows exactly one of the two).
type Message struct {
	EventID    string
	Topic      string
	EventType  string
	Subject    string
	DataSchema string
	Key        []byte
	Value      []byte
	Headers    []Header
}

// Fact is one domain fact as persisted in an outbox row's payload: which
// event happened, to which aggregate, when, and the fact's data already in
// the wire shape of ADR 0004. The relay never re-derives domain state from
// it; the encoder wraps it in the CloudEvents envelope.
type Fact struct {
	// EventID is the CloudEvents id, minted once at enqueue.
	EventID string
	// Entity is the aggregate segment of `type` (dockappointment or dockcalendar): the outbox
	// row's aggreg_type.
	Entity string
	// Event is the domain event name (e.g. "DockAppointmentBooked"). Empty when the
	// row's payload could not be decoded — such a row is poison and is
	// dead-lettered raw.
	Event string
	// AggregateID is the subject and the Kafka key: the appointment id, or
	// the site id for the door calendar events.
	AggregateID string
	// OccurredAt is the fact's instant (UTC).
	OccurredAt time.Time
	// Data is the fact's payload, already snake_case wire JSON.
	Data json.RawMessage
}

// Row is one claimed outbox row handed to the send path.
type Row struct {
	// ID is the outbox row's bigserial id (log and dead-letter reference).
	ID int64
	// Fact is the decoded payload envelope.
	Fact Fact
	// RawPayload is the row's payload exactly as persisted — the value a
	// poison row is dead-lettered with.
	RawPayload []byte
}

// SendFailureError is the error a drain pass returns when a row's send failed: the
// row stays in the outbox with attempts bumped and next_attempt_at pushed
// forward, and the rows after it were left untouched.
type SendFailureError struct {
	// RowID is the failed outbox row's id.
	RowID int64
	// Attempts is the row's attempts count AFTER this failure.
	Attempts int
	// Row is the failed row (fact, aggregate id, raw payload) for logging.
	Row Row
	// Err is the send error itself.
	Err error
}

// Error implements error.
func (f *SendFailureError) Error() string {
	return f.Err.Error()
}

// Unwrap returns the underlying send error.
func (f *SendFailureError) Unwrap() error { return f.Err }
