// Package system holds the wall-clock and id-minting adapters of ports.Clock
// and ports.IDGen: the only non-deterministic inputs of the service.
package system

import (
	"time"

	"github.com/google/uuid"

	"github.com/claudioed/dock-scheduling/internal/application/ports"
)

// Clock returns the wall clock in UTC, truncated to the microsecond: that is
// the precision of Postgres' timestamptz, so a time stamped on an aggregate or
// an event reads back from the database unchanged.
type Clock struct{}

// Now implements ports.Clock.
func (Clock) Now() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }

// IDs mints "appt-<uuid v4>" and "clo-<uuid v4>" ids.
type IDs struct{}

// NewAppointmentID implements ports.IDGen.
func (IDs) NewAppointmentID() string { return "appt-" + uuid.NewString() }

// NewClosureID implements ports.IDGen.
func (IDs) NewClosureID() string { return "clo-" + uuid.NewString() }

// Compile-time proof the adapters implement the ports.
var (
	_ ports.Clock = Clock{}
	_ ports.IDGen = IDs{}
)
