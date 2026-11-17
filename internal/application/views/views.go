// Package views holds the read DTOs the inbound adapters (REST, MCP) consume,
// so they never touch domain aggregates. Times are UTC.
package views

import "time"

// EtaView is the latest ETA report of an appointment.
type EtaView struct {
	EtaAt      time.Time
	ReportedAt time.Time
	Source     string
}

// ArrivalView is the recorded arrival of an appointment.
type ArrivalView struct {
	CheckedInAt time.Time
	Status      string
	VisitID     string
	Implicit    bool
}

// AppointmentView is one appointment as the REST and MCP adapters show it.
type AppointmentView struct {
	AppointmentID      string
	SiteID             string
	Direction          string
	DoorCode           string
	Carrier            string
	CarrierSCAC        string
	WindowStart        time.Time
	WindowEnd          time.Time
	ASNNumbers         []string
	CPTID              string
	ShipmentRefs       []string
	ExpectedSealNumber string
	Provenance         string
	State              string
	Version            int64
	AtRisk             bool
	AtRiskCause        string
	Eta                *EtaView
	Arrival            *ArrivalView
	CompletedAt        *time.Time
	CancellationReason string
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// AppointmentPage is one keyset page of appointments.
type AppointmentPage struct {
	Items      []AppointmentView
	NextCursor string
}

// EtaResult is the answer of an ETA report.
type EtaResult struct {
	Appointment AppointmentView
	AtRisk      bool
}

// SpanView is a time span, optionally typed and referenced.
type SpanView struct {
	Start time.Time
	End   time.Time
	Kind  string
	Ref   string
}

// DoorAvailabilityView answers the free windows of a door.
type DoorAvailabilityView struct {
	DoorCode           string
	SiteID             string
	From               time.Time
	To                 time.Time
	GranularityMinutes int
	FreeWindows        []SpanView
	Busy               []SpanView
}

// SettingsView mirrors the calendar settings.
type SettingsView struct {
	MaxWindowMinutes        int
	SlotGranularityMinutes  int
	ArrivalToleranceMinutes int
	NoShowGraceMinutes      int
}

// HoursView is one operating-hours row.
type HoursView struct {
	DoorCode string
	Weekday  string
	OpensAt  string
	ClosesAt string
	Timezone string
}

// ClosureView is a door closure.
type ClosureView struct {
	ClosureID string
	SiteID    string
	DoorCode  string
	From      time.Time
	To        time.Time
	Reason    string
	Lifted    bool
}

// DockCalendarView is a site's calendar with defaults applied.
type DockCalendarView struct {
	SiteID         string
	Version        int64
	Settings       SettingsView
	OperatingHours []HoursView
	Closures       []ClosureView
}

// SweepResult is the answer of a no-show sweep.
type SweepResult struct {
	Count          int
	AppointmentIDs []string
	RanAt          time.Time
}

// CarrierView is one registered carrier as the REST and MCP adapters show it.
type CarrierView struct {
	SCAC             string
	Name             string
	USDOT            string
	MC               string
	Status           string
	SuspensionReason string
	SuspendedAt      *time.Time
	Version          int64
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// CarrierPage is one keyset page of carriers.
type CarrierPage struct {
	Items      []CarrierView
	NextCursor string
}

// CarrierResult is the answer of a registration: the carrier and whether this
// call created it (false: it was already registered with the same data).
type CarrierResult struct {
	Carrier CarrierView
	Created bool
}
