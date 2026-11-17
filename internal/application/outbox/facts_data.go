package outbox

import "time"

// The wire shapes of the appointment and calendar facts (ADR 0004). Nullable
// fields are PRESENT and null when unset. The embedded standardFields are the
// optional ADR 0013 fields: omitted when absent, so a fact with no standard
// meaning and no door GLN serialises exactly as it did before they existed.

type bookedData struct {
	AppointmentID           string    `json:"appointment_id"`
	SiteID                  string    `json:"site_id"`
	Direction               string    `json:"direction"`
	DoorCode                string    `json:"door_code"`
	Carrier                 string    `json:"carrier"`
	CarrierSCAC             *string   `json:"carrier_scac"`
	WindowStart             time.Time `json:"window_start"`
	WindowEnd               time.Time `json:"window_end"`
	ASNNumbers              []string  `json:"asn_numbers"`
	CPTID                   *string   `json:"cpt_id"`
	ShipmentRefs            []string  `json:"shipment_refs"`
	ExpectedSealNumber      *string   `json:"expected_seal_number"`
	ArrivalToleranceMinutes int       `json:"arrival_tolerance_minutes"`
	Provenance              string    `json:"provenance"`
	Version                 int64     `json:"version"`
	standardFields
}

type rescheduledData struct {
	AppointmentID           string    `json:"appointment_id"`
	SiteID                  string    `json:"site_id"`
	DoorCode                string    `json:"door_code"`
	PreviousDoorCode        string    `json:"previous_door_code"`
	WindowStart             time.Time `json:"window_start"`
	WindowEnd               time.Time `json:"window_end"`
	PreviousWindowStart     time.Time `json:"previous_window_start"`
	PreviousWindowEnd       time.Time `json:"previous_window_end"`
	ArrivalToleranceMinutes int       `json:"arrival_tolerance_minutes"`
	Version                 int64     `json:"version"`
	standardFields
}

type cancelledData struct {
	AppointmentID string `json:"appointment_id"`
	SiteID        string `json:"site_id"`
	DoorCode      string `json:"door_code"`
	Reason        string `json:"reason"`
	Version       int64  `json:"version"`
	standardFields
}

type noShowData struct {
	AppointmentID string    `json:"appointment_id"`
	SiteID        string    `json:"site_id"`
	DoorCode      string    `json:"door_code"`
	Direction     string    `json:"direction"`
	CarrierSCAC   *string   `json:"carrier_scac"`
	WindowEnd     time.Time `json:"window_end"`
	Version       int64     `json:"version"`
	standardFields
}

type checkedInData struct {
	AppointmentID string    `json:"appointment_id"`
	SiteID        string    `json:"site_id"`
	DoorCode      string    `json:"door_code"`
	CheckedInAt   time.Time `json:"checked_in_at"`
	ArrivalStatus string    `json:"arrival_status"`
	VisitID       *string   `json:"visit_id"`
	Version       int64     `json:"version"`
	standardFields
}

type doorReassignedData struct {
	AppointmentID string `json:"appointment_id"`
	SiteID        string `json:"site_id"`
	FromDoorCode  string `json:"from_door_code"`
	ToDoorCode    string `json:"to_door_code"`
	Version       int64  `json:"version"`
	standardFields
}

type completedData struct {
	AppointmentID string    `json:"appointment_id"`
	SiteID        string    `json:"site_id"`
	DoorCode      string    `json:"door_code"`
	CompletedAt   time.Time `json:"completed_at"`
	Version       int64     `json:"version"`
	standardFields
}

// atRiskData has no version: the advisory does not change the aggregate beyond
// the ETA report that caused it (ADR 0004).
type atRiskData struct {
	AppointmentID string     `json:"appointment_id"`
	SiteID        string     `json:"site_id"`
	DoorCode      string     `json:"door_code"`
	EtaAt         *time.Time `json:"eta_at"`
	WindowStart   time.Time  `json:"window_start"`
	WindowEnd     time.Time  `json:"window_end"`
	Cause         string     `json:"cause"`
	standardFields
}

type closureScheduledData struct {
	SiteID    string    `json:"site_id"`
	ClosureID string    `json:"closure_id"`
	DoorCode  string    `json:"door_code"`
	From      time.Time `json:"from"`
	To        time.Time `json:"to"`
	Reason    string    `json:"reason"`
	Version   int64     `json:"version"`
	standardFields
}

type closureLiftedData struct {
	SiteID    string `json:"site_id"`
	ClosureID string `json:"closure_id"`
	DoorCode  string `json:"door_code"`
	Version   int64  `json:"version"`
	standardFields
}

type operatingHoursRow struct {
	DoorCode *string `json:"door_code"`
	Weekday  string  `json:"weekday"`
	OpensAt  string  `json:"opens_at"`
	ClosesAt string  `json:"closes_at"`
	Timezone string  `json:"timezone"`
}

// operatingHoursDeclaredData: the event-level door_code is the schema's
// required-but-nullable field and is always null (see calendarFactData).
type operatingHoursDeclaredData struct {
	SiteID         string              `json:"site_id"`
	DoorCode       *string             `json:"door_code"`
	OperatingHours []operatingHoursRow `json:"operating_hours"`
	Version        int64               `json:"version"`
}
