package dockappointment

import (
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/claudioed/dock-scheduling/internal/domain/shared"
)

const maxRefLength = 64

// DockAppointment is the aggregate root.
//
// Invariants: the id, site, door code and carrier are valid; the window ends
// after it starts, lasts at most the site maximum, sits on the slot grid and
// does not start in the past when booked or rescheduled; INBOUND covers at
// least one distinct ASN, OUTBOUND a cpt id or a shipment reference; only a
// BOOKED appointment can be rescheduled, arrive, be marked no-show or take an
// ETA, only an active one can be cancelled or reassigned; Version starts at 1
// and increases by one per accepted change.
type DockAppointment struct {
	id         ID
	site       shared.SiteID
	door       shared.DoorCode
	direction  shared.Direction
	carrier    shared.Carrier
	window     Window
	asns       []string
	cptID      string
	shipments  []string
	seal       string
	provenance Provenance
	override   bool
	state      State
	version    int64
	eta        *Eta
	arrival    *Arrival
	atRisk     AtRiskCause
	completed  time.Time
	reason     Reason
	createdAt  time.Time
	updatedAt  time.Time
}

// BookParams are the inputs of Book.
type BookParams struct {
	ID           string
	SiteID       string
	DoorCode     string
	Direction    string
	Carrier      string
	CarrierSCAC  string
	WindowStart  time.Time
	WindowEnd    time.Time
	ASNNumbers   []string
	CPTID        string
	ShipmentRefs []string
	ExpectedSeal string
	Rules        Rules
	Now          time.Time
}

// Book creates a BOOKED appointment at version 1 and raises
// DockAppointmentBooked. The window may start at now but not before it. The
// door, the calendar and the references are checked by the domain services.
func Book(p BookParams) (*DockAppointment, []Event, error) {
	a, err := build(p)
	if err != nil {
		return nil, nil, err
	}
	if a.window.start.Before(p.Now) {
		return nil, nil, ErrWindowInPast
	}
	a.createdAt, a.updatedAt = p.Now.UTC(), p.Now.UTC()
	return a, []Event{a.bookedEvent(p.Rules, p.Now)}, nil
}

// LegacyParams are the inputs of ImportLegacy.
type LegacyParams struct {
	ID          string
	SiteID      string
	DoorCode    string
	Carrier     string
	WindowStart time.Time
	WindowEnd   time.Time
	ASNNumbers  []string
	Rules       Rules
	Now         time.Time
}

// ImportLegacy builds an INBOUND appointment from a legacy inbound-receiving
// booking (strangler stage A, D-2). It enforces none of window-in-past, hours,
// closures, overlap or known-ASN, and the carrier has no SCAC.
func ImportLegacy(p LegacyParams) (*DockAppointment, []Event, error) {
	a, err := buildParts(p.ID, p.SiteID, p.DoorCode, string(shared.Inbound), p.Carrier, "")
	if err != nil {
		return nil, nil, err
	}
	if a.window, err = newBareWindow(p.WindowStart, p.WindowEnd); err != nil {
		return nil, nil, err
	}
	if a.asns, err = cleanAsns(p.ASNNumbers); err != nil {
		return nil, nil, err
	}
	a.provenance = LegacyImport
	a.createdAt, a.updatedAt = p.Now.UTC(), p.Now.UTC()
	return a, []Event{a.bookedEvent(p.Rules, p.Now)}, nil
}

func build(p BookParams) (*DockAppointment, error) {
	a, err := buildParts(p.ID, p.SiteID, p.DoorCode, p.Direction, p.Carrier, p.CarrierSCAC)
	if err != nil {
		return nil, err
	}
	if a.window, err = NewWindow(p.WindowStart, p.WindowEnd, p.Rules); err != nil {
		return nil, err
	}
	if err = a.setRefs(p.ASNNumbers, p.CPTID, p.ShipmentRefs); err != nil {
		return nil, err
	}
	if strings.IndexFunc(p.ExpectedSeal, unicode.IsControl) >= 0 || utf8.RuneCountInString(p.ExpectedSeal) > maxRefLength {
		return nil, ErrInvalidRefs
	}
	a.seal = p.ExpectedSeal
	return a, nil
}

func buildParts(id, site, door, direction, carrier, scac string) (*DockAppointment, error) {
	apptID, err := NewID(id)
	if err != nil {
		return nil, err
	}
	siteID, err := shared.NewSiteID(site)
	if err != nil {
		return nil, err
	}
	doorCode, err := shared.NewDoorCode(door)
	if err != nil {
		return nil, err
	}
	dir, err := shared.ParseDirection(direction)
	if err != nil {
		return nil, err
	}
	c, err := shared.NewCarrier(carrier, scac)
	if err != nil {
		return nil, err
	}
	return &DockAppointment{id: apptID, site: siteID, door: doorCode, direction: dir, carrier: c, provenance: Native, state: Booked, version: 1}, nil
}

// setRefs checks the shape of the references for the appointment's direction.
// Whether the ASNs and the cpt id are KNOWN is References.Check's job.
func (a *DockAppointment) setRefs(asns []string, cptID string, shipments []string) error {
	var err error
	if a.direction == shared.Inbound {
		if cptID != "" || len(shipments) > 0 {
			return ErrInvalidRefs
		}
		a.asns, err = cleanAsns(asns)
		return err
	}
	if len(asns) > 0 {
		return ErrInvalidRefs
	}
	if cptID == "" && len(shipments) == 0 {
		return ErrMissingOutboundRef
	}
	if cptID != "" && !refToken(cptID) {
		return ErrInvalidRefs
	}
	for _, s := range shipments {
		if !refToken(s) {
			return ErrInvalidRefs
		}
	}
	a.cptID = cptID
	a.shipments = append([]string(nil), shipments...)
	return nil
}

func cleanAsns(values []string) ([]string, error) {
	if len(values) == 0 {
		return nil, ErrUnknownAsn
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, v := range values {
		if !refToken(v) {
			return nil, ErrUnknownAsn
		}
		if _, dup := seen[v]; dup {
			return nil, ErrDuplicateAsn
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out, nil
}

func refToken(v string) bool {
	if strings.TrimSpace(v) == "" || utf8.RuneCountInString(v) > maxRefLength {
		return false
	}
	return strings.IndexFunc(v, unicode.IsControl) < 0
}

func (a *DockAppointment) header(at time.Time) Header {
	return Header{Appointment: string(a.id), Site: string(a.site), Version: a.version, At: at.UTC()}
}

func (a *DockAppointment) touch(at time.Time) {
	a.version++
	a.updatedAt = at.UTC()
}

func (a *DockAppointment) bookedEvent(r Rules, now time.Time) BookedEvent {
	return BookedEvent{
		Header: a.header(now), Direction: a.direction, DoorCode: string(a.door), Carrier: a.carrier.Name(), CarrierSCAC: a.carrier.SCAC(),
		WindowStart: a.window.start, WindowEnd: a.window.end, ASNNumbers: a.ASNNumbers(), CPTID: a.cptID, ShipmentRefs: a.ShipmentRefs(),
		ExpectedSeal: a.seal, ToleranceMinutes: r.ToleranceMinutes(), Provenance: a.provenance,
	}
}

// ID returns the identity.
func (a *DockAppointment) ID() ID { return a.id }

// SiteID returns the site of the door.
func (a *DockAppointment) SiteID() shared.SiteID { return a.site }

// DoorCode returns the door the appointment is booked at.
func (a *DockAppointment) DoorCode() shared.DoorCode { return a.door }

// Direction returns INBOUND or OUTBOUND.
func (a *DockAppointment) Direction() shared.Direction { return a.direction }

// Carrier returns the carrier.
func (a *DockAppointment) Carrier() shared.Carrier { return a.carrier }

// Window returns the booked window.
func (a *DockAppointment) Window() Window { return a.window }

// ASNNumbers returns a copy of the covered ASN numbers, in booking order.
func (a *DockAppointment) ASNNumbers() []string { return append([]string(nil), a.asns...) }

// CPTID returns the cutoff id of an OUTBOUND appointment, or "".
func (a *DockAppointment) CPTID() string { return a.cptID }

// ShipmentRefs returns a copy of the shipment references.
func (a *DockAppointment) ShipmentRefs() []string { return append([]string(nil), a.shipments...) }

// ExpectedSeal returns the seal number the carrier declared, or "".
func (a *DockAppointment) ExpectedSeal() string { return a.seal }

// Provenance returns where the appointment came from.
func (a *DockAppointment) Provenance() Provenance { return a.provenance }

// State returns the lifecycle state.
func (a *DockAppointment) State() State { return a.state }

// Version returns the current version.
func (a *DockAppointment) Version() int64 { return a.version }

// AtRisk returns the at-risk cause, or "" when the appointment is not at risk.
func (a *DockAppointment) AtRisk() AtRiskCause { return a.atRisk }

// Eta returns the latest ETA report, or nil.
func (a *DockAppointment) Eta() *Eta {
	if a.eta == nil {
		return nil
	}
	e := *a.eta
	return &e
}

// Arrival returns the recorded arrival, or nil.
func (a *DockAppointment) Arrival() *Arrival {
	if a.arrival == nil {
		return nil
	}
	v := *a.arrival
	return &v
}

// DoorOverride reports whether the door was set by a physical fact (ReassignDoor) rather
// than by a checked booking; such an appointment is outside the storage overlap constraint.
func (a *DockAppointment) DoorOverride() bool { return a.override }

// CancellationReason returns the reason of a CANCELLED appointment, or "".
func (a *DockAppointment) CancellationReason() Reason { return a.reason }

// CompletedAt returns when the door was released, or the zero time.
func (a *DockAppointment) CompletedAt() time.Time { return a.completed }

// CreatedAt returns when the appointment was created.
func (a *DockAppointment) CreatedAt() time.Time { return a.createdAt }

// UpdatedAt returns when the appointment last changed.
func (a *DockAppointment) UpdatedAt() time.Time { return a.updatedAt }
