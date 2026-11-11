package carrier

import (
	"fmt"
	"time"
)

// Carrier is the aggregate root, one per SCAC.
//
// Invariants: the SCAC is 2..4 upper-case letters; the name is non-blank; the
// usdot is 1..8 digits and the mc 1..8 digits behind MC or MX when present; a
// SUSPENDED carrier has a reason and a suspension time, an ACTIVE one has
// neither; Version starts at 1 and grows by one per accepted change (a
// registration or update that changes nothing is not a change).
type Carrier struct {
	scac        Scac
	identity    Identity
	status      Status
	reason      string
	suspendedAt time.Time
	version     int64
	createdAt   time.Time
	updatedAt   time.Time
}

// Register creates the carrier, ACTIVE, at version 1, and raises CarrierRegistered.
func Register(scac string, name, usdot, mc string, now time.Time) (*Carrier, []Event, error) {
	s, err := NewScac(scac)
	if err != nil {
		return nil, nil, err
	}
	id, err := NewIdentity(name, usdot, mc)
	if err != nil {
		return nil, nil, err
	}
	now = now.UTC()
	c := &Carrier{scac: s, identity: id, status: Active, version: 1, createdAt: now, updatedAt: now}
	return c, []Event{c.registered(now)}, nil
}

// SCAC returns the carrier's SCAC.
func (c *Carrier) SCAC() Scac { return c.scac }

// Identity returns the name, USDOT and MC.
func (c *Carrier) Identity() Identity { return c.identity }

// Status returns ACTIVE or SUSPENDED.
func (c *Carrier) Status() Status { return c.status }

// SuspensionReason returns why the carrier is suspended, or "" when it is not.
func (c *Carrier) SuspensionReason() string { return c.reason }

// SuspendedAt returns when the carrier was suspended, or the zero time.
func (c *Carrier) SuspendedAt() time.Time { return c.suspendedAt }

// Version returns the carrier version (the ETag).
func (c *Carrier) Version() int64 { return c.version }

// CreatedAt returns when the carrier was first registered.
func (c *Carrier) CreatedAt() time.Time { return c.createdAt }

// UpdatedAt returns when the carrier last changed.
func (c *Carrier) UpdatedAt() time.Time { return c.updatedAt }

// EnsureRegistered is the idempotent registration of an existing carrier: it
// returns nil when the registered data is exactly the one given, and
// ErrCarrierAlreadyRegistered otherwise. The status is not data: a suspended carrier
// registered again with the same data stays suspended.
func (c *Carrier) EnsureRegistered(id Identity) error {
	if c.identity != id {
		return fmt.Errorf("%w: %s", ErrCarrierAlreadyRegistered, c.scac)
	}
	return nil
}

// Change is an UpdateCarrier command: a nil field keeps the current value, a
// pointer to "" clears the optional usdot or mc (the name cannot be cleared).
type Change struct {
	Name  *string
	USDOT *string
	MC    *string
}

// Update changes the name, USDOT and/or MC. A change that leaves the identity
// as it is raises nothing and does not bump the version. Otherwise it raises
// CarrierRegistered with the new snapshot.
func (c *Carrier) Update(ch Change, now time.Time) ([]Event, error) {
	name, usdot, mc := c.identity.name, c.identity.usdot, c.identity.mc
	if ch.Name != nil {
		name = *ch.Name
	}
	if ch.USDOT != nil {
		usdot = *ch.USDOT
	}
	if ch.MC != nil {
		mc = *ch.MC
	}
	id, err := NewIdentity(name, usdot, mc)
	if err != nil {
		return nil, err
	}
	if id == c.identity {
		return nil, nil
	}
	c.identity = id
	return []Event{c.touch(now.UTC())}, nil
}

// Suspend stops the carrier booking: a booking that names its SCAC is refused
// until Reinstate. The reason is mandatory. Existing appointments are not
// touched; a human decides what happens to them.
func (c *Carrier) Suspend(reason string, now time.Time) ([]Event, error) {
	r, ok := validReason(reason)
	if !ok {
		return nil, ErrInvalidSuspensionReason
	}
	if c.status == Suspended {
		return nil, fmt.Errorf("%w: %s", ErrCarrierAlreadySuspended, c.scac)
	}
	now = now.UTC()
	c.status, c.reason, c.suspendedAt = Suspended, r, now
	c.version++
	c.updatedAt = now
	return []Event{SuspendedEvent{Header: c.header(now), Reason: r, SuspendedAt: now}}, nil
}

// Reinstate lets a suspended carrier book again.
func (c *Carrier) Reinstate(now time.Time) ([]Event, error) {
	if c.status != Suspended {
		return nil, fmt.Errorf("%w: %s", ErrCarrierNotSuspended, c.scac)
	}
	now = now.UTC()
	c.status, c.reason, c.suspendedAt = Active, "", time.Time{}
	c.version++
	c.updatedAt = now
	return []Event{ReinstatedEvent{Header: c.header(now), ReinstatedAt: now}}, nil
}

// CheckCanBook is the booking rule: a SUSPENDED carrier is refused with
// ErrCarrierSuspended (the reason in the message), an ACTIVE one is accepted.
func (c *Carrier) CheckCanBook() error {
	if c.status == Suspended {
		return fmt.Errorf("%w: %s (%s)", ErrCarrierSuspended, c.scac, c.reason)
	}
	return nil
}

// touch bumps the version for an accepted data change and returns its event.
func (c *Carrier) touch(now time.Time) Event {
	c.version++
	c.updatedAt = now
	return c.registered(now)
}

func (c *Carrier) registered(now time.Time) RegisteredEvent {
	return RegisteredEvent{
		Header: c.header(now), Name: c.identity.name, USDOT: c.identity.usdot, MC: c.identity.mc,
		Status: c.status, Reason: c.reason, SuspendedAt: c.suspendedAt,
	}
}

func (c *Carrier) header(at time.Time) Header {
	return Header{Scac: string(c.scac), Version: c.version, At: at}
}
