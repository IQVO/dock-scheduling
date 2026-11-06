package usecases

import (
	"github.com/claudioed/dock-scheduling/internal/application/views"
	"github.com/claudioed/dock-scheduling/internal/domain/dockappointment"
	"github.com/claudioed/dock-scheduling/internal/domain/dockcalendar"
)

func toView(a *dockappointment.DockAppointment) views.AppointmentView {
	w := a.Window()
	v := views.AppointmentView{
		AppointmentID: a.ID().String(), SiteID: a.SiteID().String(), Direction: string(a.Direction()), DoorCode: a.DoorCode().String(),
		Carrier: a.Carrier().Name(), CarrierSCAC: a.Carrier().SCAC(), WindowStart: w.Start(), WindowEnd: w.End(), ASNNumbers: a.ASNNumbers(),
		CPTID: a.CPTID(), ShipmentRefs: a.ShipmentRefs(), ExpectedSealNumber: a.ExpectedSeal(), Provenance: string(a.Provenance()),
		State: string(a.State()), Version: a.Version(), AtRisk: a.AtRisk() != "", AtRiskCause: string(a.AtRisk()),
		CancellationReason: string(a.CancellationReason()), CreatedAt: a.CreatedAt(), UpdatedAt: a.UpdatedAt(),
	}
	if e := a.Eta(); e != nil {
		v.Eta = &views.EtaView{EtaAt: e.At, ReportedAt: e.ReportedAt, Source: e.Source}
	}
	if ar := a.Arrival(); ar != nil {
		v.Arrival = &views.ArrivalView{CheckedInAt: ar.At, Status: string(ar.Status), VisitID: ar.VisitID, Implicit: ar.Implicit}
	}
	if t := a.CompletedAt(); !t.IsZero() {
		v.CompletedAt = &t
	}
	return v
}

func toCalendarView(c *dockcalendar.DockCalendar) views.DockCalendarView {
	s := c.Settings()
	v := views.DockCalendarView{
		SiteID: c.SiteID().String(), Version: c.Version(), OperatingHours: []views.HoursView{}, Closures: []views.ClosureView{},
		Settings: views.SettingsView{
			MaxWindowMinutes: s.MaxWindowMinutes, SlotGranularityMinutes: s.SlotGranularityMinutes,
			ArrivalToleranceMinutes: s.ArrivalToleranceMinutes, NoShowGraceMinutes: s.NoShowGraceMinutes,
		},
	}
	for _, h := range c.OperatingHours() {
		v.OperatingHours = append(v.OperatingHours, views.HoursView{
			DoorCode: h.DoorCode().String(), Weekday: string(h.Weekday()), OpensAt: h.OpensAt(), ClosesAt: h.ClosesAt(), Timezone: h.Timezone(),
		})
	}
	for _, k := range c.Closures() {
		if !k.Lifted {
			v.Closures = append(v.Closures, toClosureView(v.SiteID, k))
		}
	}
	return v
}

func toClosureView(site string, k dockcalendar.Closure) views.ClosureView {
	return views.ClosureView{ClosureID: k.ID, SiteID: site, DoorCode: k.Door.String(), From: k.From, To: k.To, Reason: k.Reason, Lifted: k.Lifted}
}

func toSpanViews(spans []dockcalendar.Span) []views.SpanView {
	out := make([]views.SpanView, 0, len(spans))
	for _, s := range spans {
		out = append(out, views.SpanView{Start: s.Start, End: s.End, Kind: s.Kind, Ref: s.Ref})
	}
	return out
}
