// Package repository holds the non-interface vocabulary of the application's
// ports: value types the ports trade in that are not interfaces. It is
// separate from package ports because ports may contain interfaces only
// (internal/architecture's "ports package only contains interfaces" rule).
package repository

import "time"

// IdempotencyRecord is the stored answer of a POST or PUT made with an
// Idempotency-Key: 2xx and 4xx only, never 5xx. Fingerprint identifies the
// request (method, path, body) so the same key with a different request can
// be told apart. Body is the encoded stored answer (headers plus payload) so
// a replay is byte-identical.
type IdempotencyRecord struct {
	Key         string
	Fingerprint string
	Status      int
	ContentType string
	Body        []byte
	CreatedAt   time.Time
}
