package http

import (
	"time"

	"github.com/claudioed/dock-scheduling/internal/application/views"
)

// The wire shapes of apis/openapi.yaml (camelCase). Nullable fields are
// pointers; arrays are never null.

type bookRequest struct {
	Direction          string    `json:"direction"`
	DoorCode           string    `json:"doorCode"`
	Carrier            string    `json:"carrier"`
	CarrierSCAC        *string   `json:"carrierScac"`
	WindowStart        time.Time `json:"windowStart"`
	WindowEnd          time.Time `json:"windowEnd"`
	ASNNumbers         []string  `json:"asnNumbers"`
	CPTID              *string   `json:"cptId"`
	ShipmentRefs       []string  `json:"shipmentRefs"`
	ExpectedSealNumber *string   `json:"expectedSealNumber"`
}

type rescheduleRequest struct {
	DoorCode    *string   `json:"doorCode"`
	WindowStart time.Time `json:"windowStart"`
	WindowEnd   time.Time `json:"windowEnd"`
}

type cancelRequest struct {
	Reason string `json:"reason"`
}

type etaRequest struct {
	EtaAt  time.Time `json:"etaAt"`
	Source string    `json:"source"`
}

type hoursRow struct {
	DoorCode *string `json:"doorCode"`
	Weekday  string  `json:"weekday"`
	OpensAt  string  `json:"opensAt"`
	ClosesAt string  `json:"closesAt"`
	Timezone string  `json:"timezone"`
}

type operatingHoursRequest struct {
	OperatingHours []hoursRow `json:"operatingHours"`
}

type settingsBody struct {
	MaxWindowMinutes        int `json:"maxWindowMinutes"`
	SlotGranularityMinutes  int `json:"slotGranularityMinutes"`
	ArrivalToleranceMinutes int `json:"arrivalToleranceMinutes"`
	NoShowGraceMinutes      int `json:"noShowGraceMinutes"`
}

type closureRequest struct {
	DoorCode string    `json:"doorCode"`
	From     time.Time `json:"from"`
	To       time.Time `json:"to"`
	Reason   string    `json:"reason"`
}

type etaBody struct {
	EtaAt      time.Time `json:"etaAt"`
	ReportedAt time.Time `json:"reportedAt"`
	Source     string    `json:"source"`
}

type arrivalBody struct {
	CheckedInAt *time.Time `json:"checkedInAt"`
	Status      string     `json:"status"`
	VisitID     *string    `json:"visitId"`
	Implicit    bool       `json:"implicit"`
}

type appointmentBody struct {
	AppointmentID      string       `json:"appointmentId"`
	SiteID             string       `json:"siteId"`
	Direction          string       `json:"direction"`
	DoorCode           string       `json:"doorCode"`
	Carrier            string       `json:"carrier"`
	CarrierSCAC        *string      `json:"carrierScac"`
	WindowStart        time.Time    `json:"windowStart"`
	WindowEnd          time.Time    `json:"windowEnd"`
	ASNNumbers         []string     `json:"asnNumbers"`
	CPTID              *string      `json:"cptId"`
	ShipmentRefs       []string     `json:"shipmentRefs"`
	ExpectedSealNumber *string      `json:"expectedSealNumber"`
	Provenance         string       `json:"provenance"`
	State              string       `json:"state"`
	Version            int64        `json:"version"`
	Eta                *etaBody     `json:"eta"`
	Arrival            *arrivalBody `json:"arrival"`
	AtRisk             bool         `json:"atRisk"`
	AtRiskCause        *string      `json:"atRiskCause"`
	CompletedAt        *time.Time   `json:"completedAt"`
	CancellationReason *string      `json:"cancellationReason"`
	CreatedAt          time.Time    `json:"createdAt"`
	UpdatedAt          time.Time    `json:"updatedAt"`
}

type etaResultBody struct {
	Appointment appointmentBody `json:"appointment"`
	AtRisk      bool            `json:"atRisk"`
}

type timeSpanBody struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

type busySpanBody struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
	Kind  string    `json:"kind"`
	Ref   *string   `json:"ref"`
}

type availabilityBody struct {
	DoorCode           string         `json:"doorCode"`
	SiteID             string         `json:"siteId"`
	From               time.Time      `json:"from"`
	To                 time.Time      `json:"to"`
	GranularityMinutes int            `json:"granularityMinutes"`
	FreeWindows        []timeSpanBody `json:"freeWindows"`
	Busy               []busySpanBody `json:"busy"`
}

type closureBody struct {
	ClosureID string    `json:"closureId"`
	SiteID    string    `json:"siteId"`
	DoorCode  string    `json:"doorCode"`
	From      time.Time `json:"from"`
	To        time.Time `json:"to"`
	Reason    string    `json:"reason"`
	Lifted    bool      `json:"lifted"`
}

type calendarBody struct {
	SiteID         string        `json:"siteId"`
	Version        int64         `json:"version"`
	Settings       settingsBody  `json:"settings"`
	OperatingHours []hoursRow    `json:"operatingHours"`
	Closures       []closureBody `json:"closures"`
}

type sweepBody struct {
	Count          int       `json:"count"`
	AppointmentIDs []string  `json:"appointmentIds"`
	RanAt          time.Time `json:"ranAt"`
}

type pageBody struct {
	Items      []appointmentBody `json:"items"`
	NextCursor *string           `json:"nextCursor,omitempty"`
}

// orNil maps "" to null.
func orNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func toAppointmentBody(v views.AppointmentView) appointmentBody {
	b := appointmentBody{
		AppointmentID: v.AppointmentID, SiteID: v.SiteID, Direction: v.Direction, DoorCode: v.DoorCode, Carrier: v.Carrier,
		CarrierSCAC: orNil(v.CarrierSCAC), WindowStart: v.WindowStart.UTC(), WindowEnd: v.WindowEnd.UTC(), ASNNumbers: nonNil(v.ASNNumbers),
		CPTID: orNil(v.CPTID), ShipmentRefs: nonNil(v.ShipmentRefs), ExpectedSealNumber: orNil(v.ExpectedSealNumber),
		Provenance: v.Provenance, State: v.State, Version: v.Version, AtRisk: v.AtRisk, AtRiskCause: orNil(v.AtRiskCause),
		CompletedAt: utcPtr(v.CompletedAt), CancellationReason: orNil(v.CancellationReason),
		CreatedAt: v.CreatedAt.UTC(), UpdatedAt: v.UpdatedAt.UTC(),
	}
	if e := v.Eta; e != nil {
		b.Eta = &etaBody{EtaAt: e.EtaAt.UTC(), ReportedAt: e.ReportedAt.UTC(), Source: e.Source}
	}
	if a := v.Arrival; a != nil {
		at := a.CheckedInAt.UTC()
		arrival := &arrivalBody{Status: a.Status, VisitID: orNil(a.VisitID), Implicit: a.Implicit}
		if !a.CheckedInAt.IsZero() {
			arrival.CheckedInAt = &at
		}
		b.Arrival = arrival
	}
	return b
}

func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

func toCalendarBody(v views.DockCalendarView) calendarBody {
	b := calendarBody{
		SiteID: v.SiteID, Version: v.Version,
		Settings: settingsBody{
			MaxWindowMinutes: v.Settings.MaxWindowMinutes, SlotGranularityMinutes: v.Settings.SlotGranularityMinutes,
			ArrivalToleranceMinutes: v.Settings.ArrivalToleranceMinutes, NoShowGraceMinutes: v.Settings.NoShowGraceMinutes,
		},
		OperatingHours: make([]hoursRow, 0, len(v.OperatingHours)), Closures: make([]closureBody, 0, len(v.Closures)),
	}
	for _, h := range v.OperatingHours {
		b.OperatingHours = append(b.OperatingHours, hoursRow{DoorCode: orNil(h.DoorCode), Weekday: h.Weekday, OpensAt: h.OpensAt, ClosesAt: h.ClosesAt, Timezone: h.Timezone})
	}
	for _, k := range v.Closures {
		b.Closures = append(b.Closures, toClosureBody(k))
	}
	return b
}

func toClosureBody(k views.ClosureView) closureBody {
	return closureBody{ClosureID: k.ClosureID, SiteID: k.SiteID, DoorCode: k.DoorCode, From: k.From.UTC(), To: k.To.UTC(), Reason: k.Reason, Lifted: k.Lifted}
}

func toAvailabilityBody(v views.DoorAvailabilityView) availabilityBody {
	b := availabilityBody{
		DoorCode: v.DoorCode, SiteID: v.SiteID, From: v.From.UTC(), To: v.To.UTC(), GranularityMinutes: v.GranularityMinutes,
		FreeWindows: make([]timeSpanBody, 0, len(v.FreeWindows)), Busy: make([]busySpanBody, 0, len(v.Busy)),
	}
	for _, s := range v.FreeWindows {
		b.FreeWindows = append(b.FreeWindows, timeSpanBody{Start: s.Start.UTC(), End: s.End.UTC()})
	}
	for _, s := range v.Busy {
		b.Busy = append(b.Busy, busySpanBody{Start: s.Start.UTC(), End: s.End.UTC(), Kind: s.Kind, Ref: orNil(s.Ref)})
	}
	return b
}
