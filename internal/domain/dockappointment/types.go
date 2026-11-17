// Package dockappointment holds the DockAppointment aggregate: one booked
// door window for one carrier visit and one trailer, inbound or outbound
// (ADR 0002). The aggregate guards its own lifecycle and window; the rules
// that span aggregates (a door's flow, the no-overlap rule, the known ASNs)
// live in the DoorSchedule and References domain services of this package.
package dockappointment

import (
	"errors"
	"regexp"
	"time"
)

const (
	// HardMaxWindow is the longest bookable window: 4 hours, in nanoseconds
	// (not `4 * time.Hour`) so the mutation gate has no uncoverable constant
	// arithmetic; a test pins the value. A site may lower it, never raise it.
	HardMaxWindow time.Duration = 14_400_000_000_000
	// DefaultMaxWindow is the site default, equal to the hard cap.
	DefaultMaxWindow = HardMaxWindow
)

var (
	// ErrInvalidAppointmentID is returned when an id is not `appt-<uuid>`.
	ErrInvalidAppointmentID = errors.New("appointment id must be appt-<uuid>")
	// ErrInvalidWindow is returned when the window does not end after it
	// starts, lasts longer than the site maximum or is off the slot grid.
	ErrInvalidWindow = errors.New("window must end after it starts, last at most the site maximum and sit on the slot granularity")
	// ErrWindowInPast is returned when a window starts before the clock's now.
	ErrWindowInPast = errors.New("window must not start in the past")
	// ErrUnknownDoor is returned when the door is not a known, active Dock slot.
	ErrUnknownDoor = errors.New("door is not a known active dock slot")
	// ErrDoorFlowMismatch is returned when the door's dock flow does not allow the direction.
	ErrDoorFlowMismatch = errors.New("door does not take this direction")
	// ErrDoorDoubleBooked is returned when another active appointment overlaps on the door.
	ErrDoorDoubleBooked = errors.New("door is already booked in an overlapping window")
	// ErrUnknownAsn is returned when an INBOUND appointment names no ASN or an ASN that is unknown or cancelled.
	ErrUnknownAsn = errors.New("inbound appointment must cover known, not cancelled asns")
	// ErrAsnAlreadyScheduled is returned when an ASN already sits on another active appointment.
	ErrAsnAlreadyScheduled = errors.New("asn is already scheduled on another active appointment")
	// ErrMissingOutboundRef is returned when an OUTBOUND appointment names neither a known cpt id nor a shipment reference.
	ErrMissingOutboundRef = errors.New("outbound appointment needs a known cpt id or at least one shipment reference")
	// ErrDuplicateAsn is returned when an ASN number is listed twice.
	ErrDuplicateAsn = errors.New("asn numbers must be distinct")
	// ErrInvalidRefs is returned for references that do not fit the direction or are blank.
	ErrInvalidRefs = errors.New("references do not fit the direction")
	// ErrIllegalTransition is returned when a command is not legal in the appointment's state.
	ErrIllegalTransition = errors.New("command is not legal in the appointment state")
	// ErrConcurrentUpdate is returned by a repository when the version the caller loaded is stale.
	ErrConcurrentUpdate = errors.New("appointment was modified concurrently")
	// ErrNoShowTooEarly is returned when the no-show grace has not elapsed.
	ErrNoShowTooEarly = errors.New("window end plus no-show grace has not passed")
	// ErrInvalidReason is returned for a cancellation reason outside the closed set.
	ErrInvalidReason = errors.New("cancellation reason must be CARRIER_REQUEST, SITE_REQUEST, ASN_CANCELLED or TURNED_AWAY")
	// ErrInvalidEta is returned for a zero ETA or a too long source label.
	ErrInvalidEta = errors.New("eta must be set and its source at most 64 characters")
	// ErrInvalidState is returned when a persisted state is not a known one.
	ErrInvalidState = errors.New("unknown appointment state")
	// ErrInvalidVersion is returned when a persisted version is below 1.
	ErrInvalidVersion = errors.New("appointment version must be at least 1")
	// ErrInvalidSnapshot is returned when a persisted row breaks an invariant.
	ErrInvalidSnapshot = errors.New("appointment snapshot is inconsistent")
)

var idPattern = regexp.MustCompile(`^appt-[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// ID identifies a dock appointment: `appt-` followed by a lowercase UUID.
type ID string

// NewID validates an appointment id.
func NewID(value string) (ID, error) {
	if !idPattern.MatchString(value) {
		return "", ErrInvalidAppointmentID
	}
	return ID(value), nil
}

// String returns the id text.
func (id ID) String() string { return string(id) }

// State is the appointment lifecycle state.
type State string

// The lifecycle: BOOKED -> CHECKED_IN -> COMPLETED, BOOKED/CHECKED_IN ->
// CANCELLED, BOOKED -> NO_SHOW. COMPLETED, CANCELLED and NO_SHOW are terminal.
const (
	Booked    State = "BOOKED"
	CheckedIn State = "CHECKED_IN"
	Completed State = "COMPLETED"
	Cancelled State = "CANCELLED"
	NoShow    State = "NO_SHOW"
)

func (s State) valid() bool {
	switch s {
	case Booked, CheckedIn, Completed, Cancelled, NoShow:
		return true
	}
	return false
}

// Active reports whether an appointment in this state still occupies its door.
func (s State) Active() bool { return s == Booked || s == CheckedIn }

// Terminal reports whether no command can change an appointment in this state.
func (s State) Terminal() bool { return s.valid() && !s.Active() }

// Provenance tells where an appointment came from.
type Provenance string

// The two provenances.
const (
	Native       Provenance = "native"
	LegacyImport Provenance = "legacy-import"
)

// Reason is a cancellation reason.
type Reason string

// The closed set of cancellation reasons.
const (
	CarrierRequest Reason = "CARRIER_REQUEST"
	SiteRequest    Reason = "SITE_REQUEST"
	AsnCancelled   Reason = "ASN_CANCELLED"
	TurnedAway     Reason = "TURNED_AWAY"
)

// ParseReason validates a cancellation reason.
func ParseReason(value string) (Reason, error) {
	switch Reason(value) {
	case CarrierRequest, SiteRequest, AsnCancelled, TurnedAway:
		return Reason(value), nil
	}
	return "", ErrInvalidReason
}

// ArrivalStatus is how the gate arrival compares with the window.
type ArrivalStatus string

// The three arrival statuses.
const (
	Early  ArrivalStatus = "EARLY"
	OnTime ArrivalStatus = "ON_TIME"
	Late   ArrivalStatus = "LATE"
)

// AtRiskCause says why an appointment is flagged at risk.
type AtRiskCause string

// The closed set of at-risk causes.
const (
	LateEta               AtRiskCause = "LATE_ETA"
	DoorReassignedOverlap AtRiskCause = "DOOR_REASSIGNED_OVERLAP"
)

// Rules are the per-site calendar settings a window is checked against.
// Zero fields mean: hard-cap window, no grid, no tolerance, no grace.
type Rules struct {
	MaxWindow        time.Duration
	Granularity      time.Duration
	ArrivalTolerance time.Duration
	NoShowGrace      time.Duration
}

func (r Rules) maxWindow() time.Duration {
	if r.MaxWindow <= 0 {
		return HardMaxWindow
	}
	return min(r.MaxWindow, HardMaxWindow)
}

// ToleranceMinutes returns the arrival tolerance in whole minutes.
func (r Rules) ToleranceMinutes() int { return int(r.ArrivalTolerance / time.Minute) }

// Window is the half-open interval [start, end) a door is booked for.
type Window struct {
	start time.Time
	end   time.Time
}

// NewWindow validates a window against the site rules and normalises it to UTC.
func NewWindow(start, end time.Time, r Rules) (Window, error) {
	w, err := newBareWindow(start, end)
	if err != nil {
		return Window{}, err
	}
	if end.Sub(start) > r.maxWindow() {
		return Window{}, ErrInvalidWindow
	}
	if g := r.Granularity; g > 0 {
		if start.Nanosecond() != 0 || end.Nanosecond() != 0 || start.Unix()%int64(g/time.Second) != 0 || end.Unix()%int64(g/time.Second) != 0 {
			return Window{}, ErrInvalidWindow
		}
	}
	return w, nil
}

// newBareWindow only requires end after start (rehydration).
func newBareWindow(start, end time.Time) (Window, error) {
	if !end.After(start) {
		return Window{}, ErrInvalidWindow
	}
	return Window{start: start.UTC(), end: end.UTC()}, nil
}

// Start returns the inclusive start.
func (w Window) Start() time.Time { return w.start }

// End returns the exclusive end.
func (w Window) End() time.Time { return w.end }

// Overlaps reports whether the two half-open windows share any instant.
func (w Window) Overlaps(other Window) bool {
	return w.start.Before(other.end) && other.start.Before(w.end)
}

// Classify compares a gate arrival with the window: EARLY before
// start - tolerance, LATE after start + tolerance, else ON_TIME.
func Classify(w Window, tolerance time.Duration, at time.Time) ArrivalStatus {
	if at.Before(w.start.Add(-tolerance)) {
		return Early
	}
	if at.After(w.start.Add(tolerance)) {
		return Late
	}
	return OnTime
}

// Eta is the carrier's latest estimated arrival.
type Eta struct {
	At         time.Time
	ReportedAt time.Time
	Source     string
}

// Arrival is the recorded arrival of the carrier.
type Arrival struct {
	At       time.Time
	Status   ArrivalStatus
	VisitID  string
	Implicit bool
}
