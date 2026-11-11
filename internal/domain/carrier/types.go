// Package carrier is the CarrierRegistry aggregate of the door calendar: who
// may book a dock window. A Carrier is keyed by its SCAC (the Standard Carrier
// Alpha Code NMFTA assigns, 2..4 upper-case letters) and carries the two
// regulatory identifiers a booking never needed until now: the USDOT number
// (FMCSA, up to 8 digits) and the MC or MX operating authority number. Pure
// Go, no I/O.
package carrier

import (
	"errors"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// MaxNameLength is the longest carrier name, in characters.
	MaxNameLength = 100
	// MaxReasonLength is the longest suspension reason, in characters.
	MaxReasonLength = 200
)

var (
	// ErrInvalidScac is returned for a SCAC that is not 2..4 upper-case letters.
	ErrInvalidScac = errors.New("scac must be 2..4 upper-case letters")
	// ErrInvalidUSDOT is returned for a USDOT number that is not 1..8 digits.
	ErrInvalidUSDOT = errors.New("usdot must be 1..8 digits")
	// ErrInvalidMC is returned for an operating authority number that is not
	// 1..8 digits with an optional MC or MX prefix.
	ErrInvalidMC = errors.New("mc must be 1..8 digits with an optional MC or MX prefix")
	// ErrInvalidCarrierName is returned for a blank or over-long carrier name or one
	// holding a control character.
	ErrInvalidCarrierName = errors.New("carrier name must be 1..100 non-blank characters without control characters")
	// ErrInvalidSuspensionReason is returned for a blank or over-long
	// suspension reason or one holding a control character.
	ErrInvalidSuspensionReason = errors.New("suspension reason must be 1..200 non-blank characters without control characters")
	// ErrInvalidStatus is returned for a status other than ACTIVE or SUSPENDED.
	ErrInvalidStatus = errors.New("status must be ACTIVE or SUSPENDED")
	// ErrInvalidSnapshot is returned when a persisted carrier violates an invariant.
	ErrInvalidSnapshot = errors.New("carrier snapshot violates an invariant")

	// ErrCarrierNotFound is returned when the SCAC is not in the registry.
	ErrCarrierNotFound = errors.New("carrier is not registered")
	// ErrCarrierAlreadyRegistered is returned when the SCAC is registered with other
	// data: the registration is idempotent for the same data only, a change
	// goes through UpdateCarrier.
	ErrCarrierAlreadyRegistered = errors.New("carrier is already registered with different data; update it instead")
	// ErrCarrierAlreadySuspended is returned when a suspended carrier is suspended again.
	ErrCarrierAlreadySuspended = errors.New("carrier is already suspended")
	// ErrCarrierNotSuspended is returned when a carrier that is not suspended is reinstated.
	ErrCarrierNotSuspended = errors.New("carrier is not suspended")
	// ErrCarrierSuspended is returned when a booking names a suspended carrier.
	ErrCarrierSuspended = errors.New("carrier is suspended")
)

var (
	scacPattern  = regexp.MustCompile(`^[A-Z]{2,4}$`)
	usdotPattern = regexp.MustCompile(`^[0-9]{1,8}$`)
	mcPattern    = regexp.MustCompile(`^(MC|MX)?[ -]?([0-9]{1,8})$`)
)

// Scac is the Standard Carrier Alpha Code: 2..4 upper-case letters.
type Scac string

// NewScac validates a SCAC. Lower case is refused, never folded: one carrier
// has exactly one spelling in URLs, events and keys.
func NewScac(value string) (Scac, error) {
	if !scacPattern.MatchString(value) {
		return "", ErrInvalidScac
	}
	return Scac(value), nil
}

// String returns the SCAC text.
func (s Scac) String() string { return string(s) }

// Status is whether the carrier may book.
type Status string

// The two statuses.
const (
	Active    Status = "ACTIVE"
	Suspended Status = "SUSPENDED"
)

// ParseStatus validates a status.
func ParseStatus(value string) (Status, error) {
	switch Status(value) {
	case Active, Suspended:
		return Status(value), nil
	}
	return "", ErrInvalidStatus
}

// text validates the free text shape shared by names and reasons and returns
// it trimmed.
func text(value string, max int) (string, bool) {
	v := strings.TrimSpace(value)
	if v == "" || utf8.RuneCountInString(v) > max || strings.IndexFunc(v, unicode.IsControl) >= 0 {
		return "", false
	}
	return v, true
}

// Identity is what the registry knows about a carrier besides its SCAC and
// status: the name and the two optional regulatory numbers. The numbers are
// kept in their canonical form (usdot digits; mc as MC<digits> or MX<digits>).
type Identity struct {
	name  string
	usdot string
	mc    string
}

// NewIdentity validates a carrier identity. An empty usdot or mc means none.
// An mc without a prefix is an MC number: MC is the common operating
// authority, MX the Mexican one, and both are written back with their prefix.
func NewIdentity(name, usdot, mc string) (Identity, error) {
	n, ok := text(name, MaxNameLength)
	if !ok {
		return Identity{}, ErrInvalidCarrierName
	}
	id := Identity{name: n}
	if u := strings.TrimSpace(usdot); u != "" {
		if !usdotPattern.MatchString(u) {
			return Identity{}, ErrInvalidUSDOT
		}
		id.usdot = u
	}
	if m := strings.ToUpper(strings.TrimSpace(mc)); m != "" {
		parts := mcPattern.FindStringSubmatch(m)
		if parts == nil {
			return Identity{}, ErrInvalidMC
		}
		prefix := parts[1]
		if prefix == "" {
			prefix = "MC"
		}
		id.mc = prefix + parts[2]
	}
	return id, nil
}

// Name returns the carrier name.
func (i Identity) Name() string { return i.name }

// USDOT returns the USDOT number, or "" when none.
func (i Identity) USDOT() string { return i.usdot }

// MC returns the canonical operating authority number, or "" when none.
func (i Identity) MC() string { return i.mc }

// validReason reports whether a suspension reason is acceptable and returns it trimmed.
func validReason(value string) (string, bool) { return text(value, MaxReasonLength) }
