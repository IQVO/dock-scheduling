package postgres

import (
	"fmt"
	"time"

	"github.com/claudioed/dock-scheduling/internal/domain/dockappointment"
	"github.com/claudioed/dock-scheduling/internal/domain/dockcalendar"
)

// nullable returns a *string of s, or nil when s is empty: the wire fields
// the AsyncAPI schemas mark nullable (carrier_scac, cpt_id,
// expected_seal_number, visit_id, door_code) are PRESENT and null when unset,
// never omitted and never "".
func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// nullableTime is nullable for an instant: the zero time is null.
func nullableTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	u := t.UTC()
	return &u
}

// list keeps a nil list a present, empty JSON array.
func list(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// appointmentFactData renders one appointment fact's data in the pinned wire
// shape of ADR 0004 (snake_case keys, RFC 3339 UTC times). The domain raises
// exactly the eight facts below; anything else is a programming error, not a
// runtime condition.
func appointmentFactData(e dockappointment.Event) (any, error) {
	switch f := e.(type) {
	case dockappointment.BookedEvent:
		return bookedData{
			AppointmentID: f.AppointmentID(), SiteID: f.SiteID(), Direction: string(f.Direction), DoorCode: f.DoorCode, Carrier: f.Carrier,
			CarrierSCAC: nullable(f.CarrierSCAC), WindowStart: f.WindowStart.UTC(), WindowEnd: f.WindowEnd.UTC(),
			ASNNumbers: list(f.ASNNumbers), CPTID: nullable(f.CPTID), ShipmentRefs: list(f.ShipmentRefs),
			ExpectedSealNumber: nullable(f.ExpectedSeal), ArrivalToleranceMinutes: f.ToleranceMinutes, Provenance: string(f.Provenance),
			Version: f.AppointmentVersion(),
		}, nil
	case dockappointment.RescheduledEvent:
		return rescheduledData{
			AppointmentID: f.AppointmentID(), SiteID: f.SiteID(), DoorCode: f.DoorCode, PreviousDoorCode: f.PreviousDoorCode,
			WindowStart: f.WindowStart.UTC(), WindowEnd: f.WindowEnd.UTC(),
			PreviousWindowStart: f.PreviousWindowStart.UTC(), PreviousWindowEnd: f.PreviousWindowEnd.UTC(),
			ArrivalToleranceMinutes: f.ToleranceMinutes, Version: f.AppointmentVersion(),
		}, nil
	case dockappointment.CancelledEvent:
		return cancelledData{AppointmentID: f.AppointmentID(), SiteID: f.SiteID(), DoorCode: f.DoorCode, Reason: string(f.Reason), Version: f.AppointmentVersion()}, nil
	case dockappointment.NoShowEvent:
		return noShowData{
			AppointmentID: f.AppointmentID(), SiteID: f.SiteID(), DoorCode: f.DoorCode, Direction: string(f.Direction),
			CarrierSCAC: nullable(f.CarrierSCAC), WindowEnd: f.WindowEnd.UTC(), Version: f.AppointmentVersion(),
		}, nil
	case dockappointment.CheckedInEvent:
		return checkedInData{
			AppointmentID: f.AppointmentID(), SiteID: f.SiteID(), DoorCode: f.DoorCode, CheckedInAt: f.CheckedInAt.UTC(),
			ArrivalStatus: string(f.ArrivalStatus), VisitID: nullable(f.VisitID), Version: f.AppointmentVersion(),
		}, nil
	case dockappointment.DoorReassigned:
		return doorReassignedData{
			AppointmentID: f.AppointmentID(), SiteID: f.SiteID(), FromDoorCode: f.FromDoorCode, ToDoorCode: f.ToDoorCode, Version: f.AppointmentVersion(),
		}, nil
	case dockappointment.CompletedEvent:
		return completedData{AppointmentID: f.AppointmentID(), SiteID: f.SiteID(), DoorCode: f.DoorCode, CompletedAt: f.CompletedAt.UTC(), Version: f.AppointmentVersion()}, nil
	case dockappointment.AtRisk:
		return atRiskData{
			AppointmentID: f.AppointmentID(), SiteID: f.SiteID(), DoorCode: f.DoorCode, EtaAt: nullableTime(f.EtaAt),
			WindowStart: f.WindowStart.UTC(), WindowEnd: f.WindowEnd.UTC(), Cause: string(f.Cause),
		}, nil
	default:
		return nil, fmt.Errorf("unknown dock appointment fact %T: no wire shape is declared for it", e)
	}
}

// calendarFactData renders one calendar fact's data (ADR 0004).
func calendarFactData(e dockcalendar.Event) (any, error) {
	switch f := e.(type) {
	case dockcalendar.ClosureScheduled:
		return closureScheduledData{
			SiteID: f.SiteID(), ClosureID: f.Closure.ID, DoorCode: string(f.Closure.Door), From: f.Closure.From.UTC(), To: f.Closure.To.UTC(),
			Reason: f.Closure.Reason, Version: f.CalendarVersion(),
		}, nil
	case dockcalendar.ClosureLifted:
		return closureLiftedData{SiteID: f.SiteID(), ClosureID: f.Closure.ID, DoorCode: string(f.Closure.Door), Version: f.CalendarVersion()}, nil
	case dockcalendar.OperatingHoursDeclared:
		rows := make([]operatingHoursRow, len(f.Rows))
		for i, r := range f.Rows {
			rows[i] = operatingHoursRow{
				DoorCode: nullable(string(r.DoorCode())), Weekday: string(r.Weekday()), OpensAt: r.OpensAt(), ClosesAt: r.ClosesAt(), Timezone: r.Timezone(),
			}
		}
		// The declaration replaces every row of the site, so the event-level
		// door_code is always null; each row names its own door (null = site default).
		return operatingHoursDeclaredData{SiteID: f.SiteID(), OperatingHours: rows, Version: f.CalendarVersion()}, nil
	default:
		return nil, fmt.Errorf("unknown dock calendar fact %T: no wire shape is declared for it", e)
	}
}

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
}

type cancelledData struct {
	AppointmentID string `json:"appointment_id"`
	SiteID        string `json:"site_id"`
	DoorCode      string `json:"door_code"`
	Reason        string `json:"reason"`
	Version       int64  `json:"version"`
}

type noShowData struct {
	AppointmentID string    `json:"appointment_id"`
	SiteID        string    `json:"site_id"`
	DoorCode      string    `json:"door_code"`
	Direction     string    `json:"direction"`
	CarrierSCAC   *string   `json:"carrier_scac"`
	WindowEnd     time.Time `json:"window_end"`
	Version       int64     `json:"version"`
}

type checkedInData struct {
	AppointmentID string    `json:"appointment_id"`
	SiteID        string    `json:"site_id"`
	DoorCode      string    `json:"door_code"`
	CheckedInAt   time.Time `json:"checked_in_at"`
	ArrivalStatus string    `json:"arrival_status"`
	VisitID       *string   `json:"visit_id"`
	Version       int64     `json:"version"`
}

type doorReassignedData struct {
	AppointmentID string `json:"appointment_id"`
	SiteID        string `json:"site_id"`
	FromDoorCode  string `json:"from_door_code"`
	ToDoorCode    string `json:"to_door_code"`
	Version       int64  `json:"version"`
}

type completedData struct {
	AppointmentID string    `json:"appointment_id"`
	SiteID        string    `json:"site_id"`
	DoorCode      string    `json:"door_code"`
	CompletedAt   time.Time `json:"completed_at"`
	Version       int64     `json:"version"`
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
}

type closureScheduledData struct {
	SiteID    string    `json:"site_id"`
	ClosureID string    `json:"closure_id"`
	DoorCode  string    `json:"door_code"`
	From      time.Time `json:"from"`
	To        time.Time `json:"to"`
	Reason    string    `json:"reason"`
	Version   int64     `json:"version"`
}

type closureLiftedData struct {
	SiteID    string `json:"site_id"`
	ClosureID string `json:"closure_id"`
	DoorCode  string `json:"door_code"`
	Version   int64  `json:"version"`
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
