package mcp_test

import (
	"strings"
	"testing"
	"time"

	"github.com/claudioed/dock-scheduling/internal/application/usecases"
)

// The tool shapes (mirrors of the REST bodies) the tests decode into.
type apptOut struct {
	AppointmentID string    `json:"appointmentId"`
	SiteID        string    `json:"siteId"`
	Direction     string    `json:"direction"`
	DoorCode      string    `json:"doorCode"`
	WindowStart   time.Time `json:"windowStart"`
	WindowEnd     time.Time `json:"windowEnd"`
	ASNNumbers    []string  `json:"asnNumbers"`
	CPTID         *string   `json:"cptId"`
	State         string    `json:"state"`
	Version       int64     `json:"version"`
	AtRisk        bool      `json:"atRisk"`
	AtRiskCause   *string   `json:"atRiskCause"`
	Eta           *struct{ Source string }
	CancelReason  *string    `json:"cancellationReason"`
	CompletedAt   *time.Time `json:"completedAt"`
}

type pageOut struct {
	Items      []apptOut `json:"items"`
	NextCursor *string   `json:"nextCursor"`
}

type writeResult struct {
	Appointment apptOut `json:"appointment"`
	AtRisk      bool    `json:"atRisk"`
	InitiatedBy struct {
		Via        string `json:"via"`
		ProposalID string `json:"proposalId"`
		ApprovedBy string `json:"approvedBy"`
		AgentID    string `json:"agentId"`
	} `json:"initiatedBy"`
	Replayed bool `json:"replayed"`
}

// seedTwo books an inbound 08:00 on D-IN and an outbound 10:00 on D-OUT.
func seedTwo(h *harness) (first, second string) {
	first = h.seedBooking(usecases.BookCommand{DoorCode: "D-IN", Direction: "INBOUND", Carrier: "ACME Freight", CarrierSCAC: "ACMF", WindowStart: at8, WindowEnd: at9, ASNNumbers: []string{"ASN-1"}})
	second = h.seedBooking(usecases.BookCommand{DoorCode: "D-OUT", Direction: "OUTBOUND", Carrier: "ACME Freight", WindowStart: at10, WindowEnd: at11, CPTID: "WH1-1500"})
	return first, second
}

func TestGetAndListAppointments(t *testing.T) {
	h := newHarness(t)
	first, second := seedTwo(h)

	var got apptOut
	h.ok("get_appointment", map[string]any{"appointmentId": first}, &got)
	if got.AppointmentID != first || got.DoorCode != "D-IN" || got.State != "BOOKED" || got.SiteID != "WH1" || got.Version != 1 {
		t.Fatalf("get_appointment = %+v", got)
	}
	if len(got.ASNNumbers) != 1 || got.ASNNumbers[0] != "ASN-1" || got.CPTID != nil {
		t.Fatalf("references = %+v", got)
	}

	var page pageOut
	h.ok("list_appointments", map[string]any{}, &page)
	if len(page.Items) != 2 || page.Items[0].AppointmentID != first || page.Items[1].AppointmentID != second || page.NextCursor != nil {
		t.Fatalf("list = %+v", page)
	}
}

func TestListAppointmentsFilters(t *testing.T) {
	h := newHarness(t)
	first, second := seedTwo(h)
	for name, tc := range map[string]struct {
		args map[string]any
		want []string
	}{
		"by door":      {map[string]any{"doorCode": "D-OUT"}, []string{second}},
		"by direction": {map[string]any{"direction": "INBOUND"}, []string{first}},
		"by site":      {map[string]any{"siteId": "WH1"}, []string{first, second}},
		"other site":   {map[string]any{"siteId": "WH9"}, nil},
		"by scac":      {map[string]any{"carrierScac": "ACMF"}, []string{first}},
		"by state":     {map[string]any{"state": "CANCELLED"}, nil},
		"by range":     {map[string]any{"from": at10.Format(time.RFC3339), "to": at12.Format(time.RFC3339)}, []string{second}},
		"at risk":      {map[string]any{"atRisk": true}, nil},
	} {
		t.Run(name, func(t *testing.T) {
			var p pageOut
			h.ok("list_appointments", tc.args, &p)
			var ids []string
			for _, it := range p.Items {
				ids = append(ids, it.AppointmentID)
			}
			if strings.Join(ids, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("ids = %v, want %v", ids, tc.want)
			}
		})
	}
}

func TestListAppointmentsPagesByCursor(t *testing.T) {
	h := newHarness(t)
	for i := 0; i < 3; i++ {
		start := at8.Add(time.Duration(i) * time.Hour)
		h.seedBooking(usecases.BookCommand{DoorCode: "D-BOTH", Direction: "OUTBOUND", Carrier: "ACME Freight", WindowStart: start, WindowEnd: start.Add(time.Hour), CPTID: "WH1-1500"})
	}
	var p1, p2 pageOut
	h.ok("list_appointments", map[string]any{"limit": 2}, &p1)
	if len(p1.Items) != 2 || p1.NextCursor == nil {
		t.Fatalf("page 1 = %+v", p1)
	}
	h.ok("list_appointments", map[string]any{"limit": 2, "cursor": *p1.NextCursor}, &p2)
	if len(p2.Items) != 1 || p2.NextCursor != nil || p2.Items[0].AppointmentID != apptN(3) {
		t.Fatalf("page 2 = %+v", p2)
	}
}

func TestReadToolsRefuseBadArguments(t *testing.T) {
	h := newHarness(t)
	for name, tc := range map[string]struct {
		tool string
		args map[string]any
		want string
	}{
		"unknown state":        {"list_appointments", map[string]any{"state": "DONE"}, "invalid-argument"},
		"unknown direction":    {"list_appointments", map[string]any{"direction": "SIDEWAYS"}, "invalid-argument"},
		"malformed from":       {"list_appointments", map[string]any{"from": "yesterday"}, "invalid-argument"},
		"limit too large":      {"list_appointments", map[string]any{"limit": 501}, "invalid-argument"},
		"negative limit":       {"list_at_risk_appointments", map[string]any{"limit": -1}, "invalid-argument"},
		"bad cursor":           {"list_appointments", map[string]any{"cursor": "###"}, "invalid-cursor"},
		"malformed id":         {"get_appointment", map[string]any{"appointmentId": "nope"}, "invalid-appointment-id"},
		"unknown id":           {"get_appointment", map[string]any{"appointmentId": apptN(99)}, "appointment-not-found"},
		"availability no from": {"get_door_availability", map[string]any{"doorCode": "D-IN", "to": at9.Format(time.RFC3339)}, `["from"]`},
		"availability reversed": {"get_door_availability", map[string]any{"doorCode": "D-IN", "from": at9.Format(time.RFC3339), "to": at8.Format(time.RFC3339)},
			"invalid-availability-query"},
		"availability unknown door": {"get_door_availability", map[string]any{"doorCode": "NOPE", "from": at8.Format(time.RFC3339), "to": at9.Format(time.RFC3339)}, "door-not-found"},
		"availability bad door":     {"get_door_availability", map[string]any{"doorCode": "a/b", "from": at8.Format(time.RFC3339), "to": at9.Format(time.RFC3339)}, "invalid-door-code"},
		"calendar bad site":         {"get_dock_calendar", map[string]any{"siteId": "a b"}, "invalid-site-id"},
	} {
		t.Run(name, func(t *testing.T) {
			if msg := h.fail(tc.tool, tc.args); !strings.HasPrefix(msg, tc.want+": ") && !strings.Contains(msg, "missing properties: "+tc.want) {
				t.Fatalf("message = %q, want it to start with %q", msg, tc.want+": ")
			}
		})
	}
}

func TestDoorAvailabilityAndCalendar(t *testing.T) {
	h := newHarness(t)
	h.seedBooking(usecases.BookCommand{DoorCode: "D-IN", Direction: "INBOUND", Carrier: "ACME Freight", WindowStart: at9, WindowEnd: at10, ASNNumbers: []string{"ASN-1"}})

	var av struct {
		DoorCode           string `json:"doorCode"`
		SiteID             string `json:"siteId"`
		GranularityMinutes int    `json:"granularityMinutes"`
		FreeWindows        []struct {
			Start time.Time `json:"start"`
			End   time.Time `json:"end"`
		} `json:"freeWindows"`
		Busy []struct {
			Start time.Time `json:"start"`
			Ref   *string   `json:"ref"`
		} `json:"busy"`
	}
	h.ok("get_door_availability", map[string]any{"doorCode": "D-IN", "from": at8.Format(time.RFC3339), "to": at11.Format(time.RFC3339), "minutes": 60}, &av)
	if av.DoorCode != "D-IN" || av.SiteID != "WH1" || av.GranularityMinutes != 15 || len(av.Busy) != 1 || av.Busy[0].Ref == nil || *av.Busy[0].Ref != apptN(1) {
		t.Fatalf("availability = %+v", av)
	}
	if len(av.FreeWindows) != 2 || !av.FreeWindows[0].Start.Equal(at8) || !av.FreeWindows[1].Start.Equal(at10) {
		t.Fatalf("free windows = %+v", av.FreeWindows)
	}

}

func TestDockCalendarCarriesTheDefaults(t *testing.T) {
	h := newHarness(t)
	var cal struct {
		SiteID   string `json:"siteId"`
		Settings struct {
			MaxWindowMinutes       int `json:"maxWindowMinutes"`
			SlotGranularityMinutes int `json:"slotGranularityMinutes"`
			NoShowGraceMinutes     int `json:"noShowGraceMinutes"`
		} `json:"settings"`
		OperatingHours []any `json:"operatingHours"`
		Closures       []any `json:"closures"`
	}
	h.ok("get_dock_calendar", map[string]any{"siteId": "WH1"}, &cal)
	if cal.SiteID != "WH1" || cal.Settings.MaxWindowMinutes != 240 || cal.Settings.SlotGranularityMinutes != 15 || cal.Settings.NoShowGraceMinutes != 60 {
		t.Fatalf("calendar defaults = %+v", cal)
	}
	if cal.OperatingHours == nil || cal.Closures == nil {
		t.Fatalf("empty lists must be [] not null: %+v", cal)
	}
}

func TestListAtRiskAfterALateEta(t *testing.T) {
	h := newHarness(t)
	id := h.seedBooking(usecases.BookCommand{DoorCode: "D-IN", Direction: "INBOUND", Carrier: "ACME Freight", WindowStart: at8, WindowEnd: at9, ASNNumbers: []string{"ASN-1"}})
	h.seedBooking(usecases.BookCommand{DoorCode: "D-BOTH", Direction: "OUTBOUND", Carrier: "ACME Freight", WindowStart: at8, WindowEnd: at9, CPTID: "WH1-1500"})

	var r writeResult
	h.ok("report_eta", map[string]any{"appointmentId": id, "etaAt": at10.Format(time.RFC3339), "idempotencyKey": "eta-1"}, &r)
	if !r.AtRisk || !r.Appointment.AtRisk {
		t.Fatalf("report_eta = %+v, want atRisk", r)
	}

	var page pageOut
	h.ok("list_at_risk_appointments", map[string]any{"siteId": "WH1"}, &page)
	if len(page.Items) != 1 || page.Items[0].AppointmentID != id || page.Items[0].AtRiskCause == nil || *page.Items[0].AtRiskCause != "LATE_ETA" {
		t.Fatalf("at risk = %+v", page)
	}
	h.ok("list_at_risk_appointments", map[string]any{"siteId": "WH2"}, &page)
	if len(page.Items) != 0 {
		t.Fatalf("other site at risk = %+v", page)
	}
}
