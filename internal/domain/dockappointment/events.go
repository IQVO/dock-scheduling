package dockappointment

import (
	"time"

	"github.com/claudioed/dock-scheduling/internal/domain/shared"
)

// Event is a domain fact raised by the DockAppointment aggregate, to be
// written to the outbox. It carries no CloudEvents or Kafka knowledge: the
// outbound adapter maps it to the wire (ADR 0004). Every event carries the
// appointment id, the aggregate version AFTER the change and when it happened
// (the caller's clock: the domain never reads a clock).
type Event interface {
	EventName() string
	AppointmentID() string
	AppointmentVersion() int64
	OccurredAt() time.Time
}

// Header is embedded in every appointment event. Flow is the appointment's
// direction (INBOUND receives, OUTBOUND ships): the standard vocabularies a
// fact maps to (ADR 0013) depend on it, so every event carries it, not only
// the two that publish it as `direction`.
type Header struct {
	Appointment string
	Site        string
	Version     int64
	At          time.Time
	Flow        shared.Direction
}

// AppointmentID returns the appointment the event is about.
func (h Header) AppointmentID() string { return h.Appointment }

// AppointmentVersion returns the aggregate version after the change.
func (h Header) AppointmentVersion() int64 { return h.Version }

// OccurredAt returns when the change happened.
func (h Header) OccurredAt() time.Time { return h.At }

// SiteID returns the site of the appointment's door.
func (h Header) SiteID() string { return h.Site }

// BookedEvent is raised by Book.
type BookedEvent struct {
	Header
	Direction        shared.Direction
	DoorCode         string
	Carrier          string
	CarrierSCAC      string
	WindowStart      time.Time
	WindowEnd        time.Time
	ASNNumbers       []string
	CPTID            string
	ShipmentRefs     []string
	ExpectedSeal     string
	ToleranceMinutes int
	Provenance       Provenance
}

// EventName returns "DockAppointmentBooked".
func (BookedEvent) EventName() string { return "DockAppointmentBooked" }

// RescheduledEvent is raised by Reschedule.
type RescheduledEvent struct {
	Header
	DoorCode            string
	PreviousDoorCode    string
	WindowStart         time.Time
	WindowEnd           time.Time
	PreviousWindowStart time.Time
	PreviousWindowEnd   time.Time
	ToleranceMinutes    int
}

// EventName returns "DockAppointmentRescheduled".
func (RescheduledEvent) EventName() string { return "DockAppointmentRescheduled" }

// CancelledEvent is raised by Cancel.
type CancelledEvent struct {
	Header
	DoorCode string
	Reason   Reason
}

// EventName returns "DockAppointmentCancelled".
func (CancelledEvent) EventName() string { return "DockAppointmentCancelled" }

// NoShowEvent is raised by MarkNoShow.
type NoShowEvent struct {
	Header
	DoorCode    string
	Direction   shared.Direction
	CarrierSCAC string
	WindowEnd   time.Time
}

// EventName returns "DockAppointmentNoShow".
func (NoShowEvent) EventName() string { return "DockAppointmentNoShow" }

// CheckedInEvent is raised by RecordArrival (analytics only).
type CheckedInEvent struct {
	Header
	DoorCode      string
	CheckedInAt   time.Time
	ArrivalStatus ArrivalStatus
	VisitID       string
}

// EventName returns "DockAppointmentCheckedIn".
func (CheckedInEvent) EventName() string { return "DockAppointmentCheckedIn" }

// DoorReassigned is raised by ReassignDoor (analytics only).
type DoorReassigned struct {
	Header
	FromDoorCode string
	ToDoorCode   string
}

// EventName returns "DockAppointmentDoorReassigned".
func (DoorReassigned) EventName() string { return "DockAppointmentDoorReassigned" }

// CompletedEvent is raised by RecordDoorRelease (analytics only).
type CompletedEvent struct {
	Header
	DoorCode    string
	CompletedAt time.Time
}

// EventName returns "DockAppointmentCompleted".
func (CompletedEvent) EventName() string { return "DockAppointmentCompleted" }

// AtRisk is the advisory raised by ReportEta and MarkAtRisk (analytics only).
// It does not carry a meaningful version on the wire.
type AtRisk struct {
	Header
	DoorCode    string
	EtaAt       time.Time
	WindowStart time.Time
	WindowEnd   time.Time
	Cause       AtRiskCause
}

// EventName returns "DockAppointmentAtRisk".
func (AtRisk) EventName() string { return "DockAppointmentAtRisk" }
