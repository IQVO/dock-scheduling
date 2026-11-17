package carrier

import "time"

// Snapshot is the persisted form of a carrier. Restore validates it.
type Snapshot struct {
	SCAC        string
	Name        string
	USDOT       string
	MC          string
	Status      Status
	Reason      string
	SuspendedAt time.Time
	Version     int64
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Snapshot returns the persisted form of the carrier.
func (c *Carrier) Snapshot() Snapshot {
	return Snapshot{
		SCAC: string(c.scac), Name: c.identity.name, USDOT: c.identity.usdot, MC: c.identity.mc, Status: c.status,
		Reason: c.reason, SuspendedAt: c.suspendedAt, Version: c.version, CreatedAt: c.createdAt, UpdatedAt: c.updatedAt,
	}
}

// Restore rebuilds a persisted carrier, re-validating every invariant.
func Restore(s Snapshot) (*Carrier, error) {
	scac, err := NewScac(s.SCAC)
	if err != nil {
		return nil, err
	}
	id, err := NewIdentity(s.Name, s.USDOT, s.MC)
	if err != nil {
		return nil, err
	}
	status, err := ParseStatus(string(s.Status))
	if err != nil {
		return nil, err
	}
	if s.Version < 1 || id.name != s.Name || id.usdot != s.USDOT || id.mc != s.MC {
		return nil, ErrInvalidSnapshot
	}
	c := &Carrier{
		scac: scac, identity: id, status: status, version: s.Version,
		createdAt: s.CreatedAt.UTC(), updatedAt: s.UpdatedAt.UTC(),
	}
	if status == Suspended {
		reason, ok := validReason(s.Reason)
		if !ok || reason != s.Reason || s.SuspendedAt.IsZero() {
			return nil, ErrInvalidSnapshot
		}
		c.reason, c.suspendedAt = reason, s.SuspendedAt.UTC()
	} else if s.Reason != "" || !s.SuspendedAt.IsZero() {
		return nil, ErrInvalidSnapshot
	}
	return c, nil
}
