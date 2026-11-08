// Package shared holds the value objects both aggregates of the door
// calendar use: the door code, the site id, the carrier and the direction of a
// door window, and the dock flow a door allows. Pure Go, no I/O.
package shared

import (
	"errors"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// MaxDoorCodeLength is the longest door code, in characters.
	MaxDoorCodeLength = 64
	// MaxSiteIDLength is the longest site id, in characters.
	MaxSiteIDLength = 64
	// MaxCarrierLength is the longest carrier name, in characters.
	MaxCarrierLength = 100
)

var (
	// ErrInvalidDoorCode is returned when a door code is empty, too long or
	// contains whitespace, a control character or '/'.
	ErrInvalidDoorCode = errors.New("door code must be 1..64 characters without whitespace, control characters or '/'")
	// ErrInvalidSiteID is returned when a site id is empty, too long or
	// contains whitespace, a control character or '/'.
	ErrInvalidSiteID = errors.New("site id must be 1..64 characters without whitespace, control characters or '/'")
	// ErrInvalidCarrier is returned when the carrier name is blank, too long
	// or holds a control character, or the SCAC is not 2..4 upper-case letters.
	ErrInvalidCarrier = errors.New("carrier must be 1..100 non-blank characters and the scac 2..4 upper-case letters")
	// ErrInvalidDirection is returned for a direction other than INBOUND or OUTBOUND.
	ErrInvalidDirection = errors.New("direction must be INBOUND or OUTBOUND")
	// ErrInvalidDockFlow is returned for a dock flow other than Inbound, Outbound or Both.
	ErrInvalidDockFlow = errors.New("dock flow must be Inbound, Outbound or Both")
)

// token validates the text shape shared by door codes and site ids.
func token(value string, max int) bool {
	if value == "" || utf8.RuneCountInString(value) > max {
		return false
	}
	for _, r := range value {
		if r == '/' || unicode.IsSpace(r) || unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// DoorCode is the facility-layout location code of a dock door.
type DoorCode string

// NewDoorCode validates a door code.
func NewDoorCode(value string) (DoorCode, error) {
	if !token(value, MaxDoorCodeLength) {
		return "", ErrInvalidDoorCode
	}
	return DoorCode(value), nil
}

// String returns the door code text.
func (d DoorCode) String() string { return string(d) }

// SiteID is the facility-layout site code a door calendar belongs to.
type SiteID string

// NewSiteID validates a site id.
func NewSiteID(value string) (SiteID, error) {
	if !token(value, MaxSiteIDLength) {
		return "", ErrInvalidSiteID
	}
	return SiteID(value), nil
}

// String returns the site id text.
func (s SiteID) String() string { return string(s) }

var scacPattern = regexp.MustCompile(`^[A-Z]{2,4}$`)

// Carrier is the carrier of a visit: a name and an optional SCAC.
type Carrier struct {
	name string
	scac string
}

// NewCarrier validates a carrier; an empty scac means none.
func NewCarrier(name, scac string) (Carrier, error) {
	if strings.TrimSpace(name) == "" || utf8.RuneCountInString(name) > MaxCarrierLength {
		return Carrier{}, ErrInvalidCarrier
	}
	if strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return Carrier{}, ErrInvalidCarrier
	}
	if scac != "" && !scacPattern.MatchString(scac) {
		return Carrier{}, ErrInvalidCarrier
	}
	return Carrier{name: name, scac: scac}, nil
}

// Name returns the carrier name.
func (c Carrier) Name() string { return c.name }

// SCAC returns the SCAC, or "" when none was given.
func (c Carrier) SCAC() string { return c.scac }

// Direction is whether an appointment receives (INBOUND) or ships (OUTBOUND).
type Direction string

// The two directions.
const (
	Inbound  Direction = "INBOUND"
	Outbound Direction = "OUTBOUND"
)

// ParseDirection validates a direction.
func ParseDirection(value string) (Direction, error) {
	switch Direction(value) {
	case Inbound:
		return Inbound, nil
	case Outbound:
		return Outbound, nil
	}
	return "", ErrInvalidDirection
}

// DockFlow is the flow a facility-layout dock slot allows.
type DockFlow string

// The dock flows of facility-layout.
const (
	FlowInbound  DockFlow = "Inbound"
	FlowOutbound DockFlow = "Outbound"
	FlowBoth     DockFlow = "Both"
)

// ParseDockFlow validates a dock flow.
func ParseDockFlow(value string) (DockFlow, error) {
	switch DockFlow(value) {
	case FlowInbound, FlowOutbound, FlowBoth:
		return DockFlow(value), nil
	}
	return "", ErrInvalidDockFlow
}

// Allows reports whether a door with this flow takes appointments of d.
func (f DockFlow) Allows(d Direction) bool {
	switch f {
	case FlowBoth:
		return d == Inbound || d == Outbound
	case FlowInbound:
		return d == Inbound
	case FlowOutbound:
		return d == Outbound
	}
	return false
}
