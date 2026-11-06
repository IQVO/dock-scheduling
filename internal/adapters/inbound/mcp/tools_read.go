package mcp

import (
	"context"
	"time"

	"github.com/claudioed/dock-scheduling/internal/application/repository"
	"github.com/claudioed/dock-scheduling/internal/application/usecases"
	"github.com/claudioed/dock-scheduling/internal/domain/dockappointment"
	"github.com/claudioed/dock-scheduling/internal/domain/shared"
)

// parseTime reads an RFC 3339 timestamp argument. A blank value is the zero
// time (the caller decides whether that is allowed); anything else that does
// not parse is refused, never defaulted.
func parseTime(name, v string) (time.Time, error) {
	if v == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return time.Time{}, argError("%s must be an RFC 3339 timestamp, got %q", name, v)
	}
	return t.UTC(), nil
}

// requireTime is parseTime for a mandatory argument.
func requireTime(name, v string) (time.Time, error) {
	if v == "" {
		return time.Time{}, argError("%s is required (RFC 3339)", name)
	}
	return parseTime(name, v)
}

// checkLimit refuses a page size outside 0..MaxListLimit (0 = the default page).
func checkLimit(limit int) error {
	if limit < 0 || limit > usecases.MaxListLimit {
		return argError("limit must be 1..%d (omit it for the default page of %d)", usecases.MaxListLimit, usecases.DefaultListLimit)
	}
	return nil
}

func validState(s string) bool {
	switch dockappointment.State(s) {
	case dockappointment.Booked, dockappointment.CheckedIn, dockappointment.Completed, dockappointment.Cancelled, dockappointment.NoShow:
		return true
	}
	return false
}

// --- list_appointments --------------------------------------------------------

type listAppointmentsInput struct {
	SiteID      string `json:"siteId,omitempty" jsonschema:"optional site code to narrow to"`
	DoorCode    string `json:"doorCode,omitempty" jsonschema:"optional dock door code to narrow to"`
	State       string `json:"state,omitempty" jsonschema:"optional state: BOOKED, CHECKED_IN, COMPLETED, CANCELLED or NO_SHOW"`
	Direction   string `json:"direction,omitempty" jsonschema:"optional direction: INBOUND or OUTBOUND"`
	CarrierSCAC string `json:"carrierScac,omitempty" jsonschema:"optional carrier SCAC to narrow to"`
	AtRisk      bool   `json:"atRisk,omitempty" jsonschema:"optional; true lists only appointments flagged at risk"`
	From        string `json:"from,omitempty" jsonschema:"optional RFC 3339 start of the range; with to, appointments whose window overlaps [from, to)"`
	To          string `json:"to,omitempty" jsonschema:"optional RFC 3339 end of the range"`
	Limit       int    `json:"limit,omitempty" jsonschema:"optional page size 1..500 (default 100)"`
	Cursor      string `json:"cursor,omitempty" jsonschema:"optional nextCursor of the previous page"`
}

func (in listAppointmentsInput) filter() (repository.AppointmentFilter, error) {
	f := repository.AppointmentFilter{SiteID: in.SiteID, DoorCode: in.DoorCode, State: in.State, Direction: in.Direction, CarrierSCAC: in.CarrierSCAC, AtRiskOnly: in.AtRisk}
	if f.State != "" && !validState(f.State) {
		return f, argError("state must be BOOKED, CHECKED_IN, COMPLETED, CANCELLED or NO_SHOW, got %q", f.State)
	}
	if f.Direction != "" {
		if _, err := shared.ParseDirection(f.Direction); err != nil {
			return f, argError("direction must be INBOUND or OUTBOUND, got %q", f.Direction)
		}
	}
	var err error
	if f.From, err = parseTime("from", in.From); err != nil {
		return f, err
	}
	f.To, err = parseTime("to", in.To)
	return f, err
}

func (d Deps) listAppointments(ctx context.Context, in listAppointmentsInput) (appointmentPageOut, error) {
	f, err := in.filter()
	if err != nil {
		return appointmentPageOut{}, err
	}
	if err := checkLimit(in.Limit); err != nil {
		return appointmentPageOut{}, err
	}
	page, err := d.Service.ListAppointments(ctx, f, in.Limit, in.Cursor)
	if err != nil {
		return appointmentPageOut{}, err
	}
	return toAppointmentPage(page), nil
}

// --- list_at_risk_appointments --------------------------------------------------

type listAtRiskInput struct {
	SiteID string `json:"siteId,omitempty" jsonschema:"optional site code to narrow to"`
	Limit  int    `json:"limit,omitempty" jsonschema:"optional page size 1..500 (default 100)"`
	Cursor string `json:"cursor,omitempty" jsonschema:"optional nextCursor of the previous page"`
}

func (d Deps) listAtRisk(ctx context.Context, in listAtRiskInput) (appointmentPageOut, error) {
	if err := checkLimit(in.Limit); err != nil {
		return appointmentPageOut{}, err
	}
	page, err := d.Service.ListAtRisk(ctx, in.SiteID, in.Limit, in.Cursor)
	if err != nil {
		return appointmentPageOut{}, err
	}
	return toAppointmentPage(page), nil
}

// --- get_appointment ------------------------------------------------------------

type getAppointmentInput struct {
	AppointmentID string `json:"appointmentId" jsonschema:"the appointment id, appt-<uuid>"`
}

func (d Deps) getAppointment(ctx context.Context, in getAppointmentInput) (appointmentOut, error) {
	if err := requireText("appointmentId", in.AppointmentID); err != nil {
		return appointmentOut{}, err
	}
	v, err := d.Service.GetAppointment(ctx, in.AppointmentID)
	if err != nil {
		return appointmentOut{}, err
	}
	return toAppointment(v), nil
}

// --- get_door_availability ------------------------------------------------------

type doorAvailabilityInput struct {
	DoorCode string `json:"doorCode" jsonschema:"the dock door code"`
	From     string `json:"from" jsonschema:"RFC 3339 start of the range"`
	To       string `json:"to" jsonschema:"RFC 3339 end of the range, after from and at most 14 days later"`
	Minutes  int    `json:"minutes,omitempty" jsonschema:"optional length of each free window in minutes, 1..240 (default the site's slot granularity)"`
}

func (d Deps) getDoorAvailability(ctx context.Context, in doorAvailabilityInput) (availabilityOut, error) {
	if err := requireText("doorCode", in.DoorCode); err != nil {
		return availabilityOut{}, err
	}
	from, err := requireTime("from", in.From)
	if err != nil {
		return availabilityOut{}, err
	}
	to, err := requireTime("to", in.To)
	if err != nil {
		return availabilityOut{}, err
	}
	v, err := d.Service.GetDoorAvailability(ctx, in.DoorCode, from, to, in.Minutes)
	if err != nil {
		return availabilityOut{}, err
	}
	return toAvailability(v), nil
}

// --- get_dock_calendar ----------------------------------------------------------

type dockCalendarInput struct {
	SiteID string `json:"siteId" jsonschema:"the site code (facility-layout siteCode)"`
}

func (d Deps) getDockCalendar(ctx context.Context, in dockCalendarInput) (calendarOut, error) {
	if err := requireText("siteId", in.SiteID); err != nil {
		return calendarOut{}, err
	}
	v, err := d.Service.GetDockCalendar(ctx, in.SiteID)
	if err != nil {
		return calendarOut{}, err
	}
	return toCalendar(v), nil
}
