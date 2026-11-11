package carrier

import "time"

// Event is a domain fact raised by the Carrier aggregate. Every event carries
// the SCAC, the carrier version AFTER the change and when it happened.
type Event interface {
	EventName() string
	SCAC() string
	CarrierVersion() int64
	OccurredAt() time.Time
}

// Header is embedded in every carrier event.
type Header struct {
	Scac    string
	Version int64
	At      time.Time
}

// SCAC returns the carrier the event is about.
func (h Header) SCAC() string { return h.Scac }

// CarrierVersion returns the carrier version after the change.
func (h Header) CarrierVersion() int64 { return h.Version }

// OccurredAt returns when the change happened.
func (h Header) OccurredAt() time.Time { return h.At }

// RegisteredEvent is raised by the first registration and by every update that
// changes something. It carries the full carrier snapshot after the change.
type RegisteredEvent struct {
	Header
	Name        string
	USDOT       string
	MC          string
	Status      Status
	Reason      string
	SuspendedAt time.Time
}

// EventName returns "CarrierRegistered".
func (RegisteredEvent) EventName() string { return "CarrierRegistered" }

// SuspendedEvent is raised by Suspend.
type SuspendedEvent struct {
	Header
	Reason      string
	SuspendedAt time.Time
}

// EventName returns "CarrierSuspended".
func (SuspendedEvent) EventName() string { return "CarrierSuspended" }

// ReinstatedEvent is raised by Reinstate.
type ReinstatedEvent struct {
	Header
	ReinstatedAt time.Time
}

// EventName returns "CarrierReinstated".
func (ReinstatedEvent) EventName() string { return "CarrierReinstated" }
