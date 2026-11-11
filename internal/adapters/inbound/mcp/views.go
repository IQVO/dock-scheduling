package mcp

import (
	"time"

	"github.com/claudioed/dock-scheduling/internal/application/usecases"
	"github.com/claudioed/dock-scheduling/internal/application/views"
)

// Tool outputs mirror the REST bodies (camelCase, UTC RFC 3339 times, null for
// an absent optional value) so a client sees one shape over both surfaces.

type etaOut struct {
	EtaAt      time.Time `json:"etaAt"`
	ReportedAt time.Time `json:"reportedAt"`
	Source     string    `json:"source"`
}

type arrivalOut struct {
	CheckedInAt *time.Time `json:"checkedInAt"`
	Status      string     `json:"status"`
	VisitID     *string    `json:"visitId"`
	Implicit    bool       `json:"implicit"`
}

type appointmentOut struct {
	AppointmentID      string      `json:"appointmentId"`
	SiteID             string      `json:"siteId"`
	Direction          string      `json:"direction"`
	DoorCode           string      `json:"doorCode"`
	Carrier            string      `json:"carrier"`
	CarrierSCAC        *string     `json:"carrierScac"`
	WindowStart        time.Time   `json:"windowStart"`
	WindowEnd          time.Time   `json:"windowEnd"`
	ASNNumbers         []string    `json:"asnNumbers"`
	CPTID              *string     `json:"cptId"`
	ShipmentRefs       []string    `json:"shipmentRefs"`
	ExpectedSealNumber *string     `json:"expectedSealNumber"`
	Provenance         string      `json:"provenance"`
	State              string      `json:"state"`
	Version            int64       `json:"version"`
	Eta                *etaOut     `json:"eta"`
	Arrival            *arrivalOut `json:"arrival"`
	AtRisk             bool        `json:"atRisk"`
	AtRiskCause        *string     `json:"atRiskCause"`
	CompletedAt        *time.Time  `json:"completedAt"`
	CancellationReason *string     `json:"cancellationReason"`
	CreatedAt          time.Time   `json:"createdAt"`
	UpdatedAt          time.Time   `json:"updatedAt"`
}

type appointmentPageOut struct {
	Items      []appointmentOut `json:"items"`
	NextCursor *string          `json:"nextCursor"`
}

type spanOut struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
	Kind  *string   `json:"kind"`
	Ref   *string   `json:"ref"`
}

type availabilityOut struct {
	DoorCode           string    `json:"doorCode"`
	SiteID             string    `json:"siteId"`
	From               time.Time `json:"from"`
	To                 time.Time `json:"to"`
	GranularityMinutes int       `json:"granularityMinutes"`
	FreeWindows        []spanOut `json:"freeWindows"`
	Busy               []spanOut `json:"busy"`
}

type settingsOut struct {
	MaxWindowMinutes        int `json:"maxWindowMinutes"`
	SlotGranularityMinutes  int `json:"slotGranularityMinutes"`
	ArrivalToleranceMinutes int `json:"arrivalToleranceMinutes"`
	NoShowGraceMinutes      int `json:"noShowGraceMinutes"`
}

type hoursOut struct {
	DoorCode *string `json:"doorCode"`
	Weekday  string  `json:"weekday"`
	OpensAt  string  `json:"opensAt"`
	ClosesAt string  `json:"closesAt"`
	Timezone string  `json:"timezone"`
}

type closureOut struct {
	ClosureID string    `json:"closureId"`
	SiteID    string    `json:"siteId"`
	DoorCode  string    `json:"doorCode"`
	From      time.Time `json:"from"`
	To        time.Time `json:"to"`
	Reason    string    `json:"reason"`
	Lifted    bool      `json:"lifted"`
}

type calendarOut struct {
	SiteID         string       `json:"siteId"`
	Version        int64        `json:"version"`
	Settings       settingsOut  `json:"settings"`
	OperatingHours []hoursOut   `json:"operatingHours"`
	Closures       []closureOut `json:"closures"`
}

// initiatedByOut echoes the attribution a write recorded: the surface (always
// mcp here, set by the adapter) and the caller's optional labels.
type initiatedByOut struct {
	Via        string `json:"via"`
	ProposalID string `json:"proposalId,omitempty"`
	ApprovedBy string `json:"approvedBy,omitempty"`
	AgentID    string `json:"agentId,omitempty"`
}

// writeOut is the answer of every write tool.
type writeOut struct {
	Appointment appointmentOut `json:"appointment"`
	// AtRisk is report_eta only: true when THIS ETA report flagged the
	// appointment at risk (it raised DockAppointmentAtRisk); false for the others.
	AtRisk      bool           `json:"atRisk"`
	InitiatedBy initiatedByOut `json:"initiatedBy"`
	// Replayed is true when the answer is the stored one of an earlier call
	// with the same idempotencyKey: nothing was written this time.
	Replayed bool `json:"replayed"`
}

func (o *writeOut) attribute(by initiatedByOut) { o.InitiatedBy = by }
func (o *writeOut) markReplayed(r bool)         { o.Replayed = r }
func (o *writeOut) logFields() []any {
	return []any{"appointment_id", o.Appointment.AppointmentID, "state", o.Appointment.State}
}

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

func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

func toAppointment(v views.AppointmentView) appointmentOut {
	out := appointmentOut{
		AppointmentID: v.AppointmentID, SiteID: v.SiteID, Direction: v.Direction, DoorCode: v.DoorCode, Carrier: v.Carrier,
		CarrierSCAC: orNil(v.CarrierSCAC), WindowStart: v.WindowStart.UTC(), WindowEnd: v.WindowEnd.UTC(), ASNNumbers: nonNil(v.ASNNumbers),
		CPTID: orNil(v.CPTID), ShipmentRefs: nonNil(v.ShipmentRefs), ExpectedSealNumber: orNil(v.ExpectedSealNumber),
		Provenance: v.Provenance, State: v.State, Version: v.Version, AtRisk: v.AtRisk, AtRiskCause: orNil(v.AtRiskCause),
		CompletedAt: utcPtr(v.CompletedAt), CancellationReason: orNil(v.CancellationReason),
		CreatedAt: v.CreatedAt.UTC(), UpdatedAt: v.UpdatedAt.UTC(),
	}
	if e := v.Eta; e != nil {
		out.Eta = &etaOut{EtaAt: e.EtaAt.UTC(), ReportedAt: e.ReportedAt.UTC(), Source: e.Source}
	}
	if a := v.Arrival; a != nil {
		arrival := &arrivalOut{Status: a.Status, VisitID: orNil(a.VisitID), Implicit: a.Implicit}
		if !a.CheckedInAt.IsZero() {
			at := a.CheckedInAt.UTC()
			arrival.CheckedInAt = &at
		}
		out.Arrival = arrival
	}
	return out
}

func toAppointmentPage(p views.AppointmentPage) appointmentPageOut {
	out := appointmentPageOut{Items: make([]appointmentOut, 0, len(p.Items)), NextCursor: orNil(p.NextCursor)}
	for _, v := range p.Items {
		out.Items = append(out.Items, toAppointment(v))
	}
	return out
}

func toSpans(spans []views.SpanView) []spanOut {
	out := make([]spanOut, 0, len(spans))
	for _, s := range spans {
		out = append(out, spanOut{Start: s.Start.UTC(), End: s.End.UTC(), Kind: orNil(s.Kind), Ref: orNil(s.Ref)})
	}
	return out
}

func toAvailability(v views.DoorAvailabilityView) availabilityOut {
	return availabilityOut{
		DoorCode: v.DoorCode, SiteID: v.SiteID, From: v.From.UTC(), To: v.To.UTC(), GranularityMinutes: v.GranularityMinutes,
		FreeWindows: toSpans(v.FreeWindows), Busy: toSpans(v.Busy),
	}
}

func toCalendar(v views.DockCalendarView) calendarOut {
	out := calendarOut{
		SiteID: v.SiteID, Version: v.Version,
		Settings: settingsOut{
			MaxWindowMinutes: v.Settings.MaxWindowMinutes, SlotGranularityMinutes: v.Settings.SlotGranularityMinutes,
			ArrivalToleranceMinutes: v.Settings.ArrivalToleranceMinutes, NoShowGraceMinutes: v.Settings.NoShowGraceMinutes,
		},
		OperatingHours: make([]hoursOut, 0, len(v.OperatingHours)), Closures: make([]closureOut, 0, len(v.Closures)),
	}
	for _, h := range v.OperatingHours {
		out.OperatingHours = append(out.OperatingHours, hoursOut{DoorCode: orNil(h.DoorCode), Weekday: h.Weekday, OpensAt: h.OpensAt, ClosesAt: h.ClosesAt, Timezone: h.Timezone})
	}
	for _, k := range v.Closures {
		out.Closures = append(out.Closures, closureOut{ClosureID: k.ClosureID, SiteID: k.SiteID, DoorCode: k.DoorCode, From: k.From.UTC(), To: k.To.UTC(), Reason: k.Reason, Lifted: k.Lifted})
	}
	return out
}

func toInitiatedBy(i usecases.Initiator) initiatedByOut {
	return initiatedByOut{Via: string(i.Via()), ProposalID: i.ProposalID(), ApprovedBy: i.ApprovedBy(), AgentID: i.AgentID()}
}
