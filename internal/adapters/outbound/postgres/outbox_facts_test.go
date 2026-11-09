package postgres

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/claudioed/dock-scheduling/internal/domain/dockappointment"
	"github.com/claudioed/dock-scheduling/internal/domain/dockcalendar"
	"github.com/claudioed/dock-scheduling/internal/domain/shared"
)

// The wire data the outbox publisher writes for each domain fact must be the
// `data` object of the golden CloudEvent the Kafka encoder is pinned to
// (internal/adapters/outbound/kafka/testdata): the two halves of the chain
// domain event -> outbox row -> CloudEvent meet on the same files.

const (
	apptID = "appt-123e4567-e89b-12d3-a456-426614174000"
	door1  = "WH1-DOCK-IN-01"
	door2  = "WH1-DOCK-IN-02"
)

func ts(day, h, m, s int) time.Time { return time.Date(2026, 11, day, h, m, s, 0, time.UTC) }

func goldenData(t *testing.T, file string) any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "kafka", "testdata", file))
	if err != nil {
		t.Fatal(err)
	}
	var env struct {
		Data any `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	return env.Data
}

func asJSONValue(t *testing.T, v any) any {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func apptHeader(version int64, at time.Time) dockappointment.Header {
	return dockappointment.Header{Appointment: apptID, Site: "WH1", Version: version, At: at}
}

func TestAppointmentFactsRenderTheGoldenWireData(t *testing.T) {
	hours := ts(6, 8, 0, 0)
	cases := []struct {
		golden string
		event  dockappointment.Event
	}{
		{"dock_appointment_booked.golden.json", dockappointment.BookedEvent{
			Header: apptHeader(1, ts(5, 12, 0, 0)), Direction: shared.Inbound, DoorCode: door1, Carrier: "ACME Freight", CarrierSCAC: "ACMF",
			WindowStart: hours, WindowEnd: ts(6, 10, 0, 0), ASNNumbers: []string{"ASN-1001"}, ExpectedSeal: "SEAL-77120", ToleranceMinutes: 15, Provenance: dockappointment.Native,
		}},
		{"dock_appointment_rescheduled.golden.json", dockappointment.RescheduledEvent{
			Header: apptHeader(2, ts(5, 13, 10, 0)), DoorCode: door2, PreviousDoorCode: door1, WindowStart: ts(6, 9, 0, 0), WindowEnd: ts(6, 11, 0, 0),
			PreviousWindowStart: hours, PreviousWindowEnd: ts(6, 10, 0, 0), ToleranceMinutes: 15,
		}},
		{"dock_appointment_cancelled.golden.json", dockappointment.CancelledEvent{Header: apptHeader(2, ts(5, 14, 20, 0)), DoorCode: door1, Reason: dockappointment.CarrierRequest}},
		{"dock_appointment_no_show.golden.json", dockappointment.NoShowEvent{
			Header: apptHeader(2, ts(6, 11, 5, 0)), DoorCode: door1, Direction: shared.Inbound, CarrierSCAC: "ACMF", WindowEnd: ts(6, 10, 0, 0),
		}},
		{"dock_appointment_checked_in_analytics.golden.json", dockappointment.CheckedInEvent{
			Header: apptHeader(2, ts(6, 7, 55, 30)), DoorCode: door1, CheckedInAt: ts(6, 7, 55, 0), ArrivalStatus: dockappointment.OnTime, VisitID: "visit-0b5c1f2e-7a9d-4c31-8e64-5d2f9a7b3c10",
		}},
		{"dock_appointment_door_reassigned_analytics.golden.json", dockappointment.DoorReassigned{Header: apptHeader(3, ts(6, 8, 20, 0)), FromDoorCode: door1, ToDoorCode: door2}},
		// ADR 0010: the integration golden carries the very same data.
		{"dock_appointment_door_reassigned.golden.json", dockappointment.DoorReassigned{Header: apptHeader(3, ts(6, 8, 20, 0)), FromDoorCode: door1, ToDoorCode: door2}},
		{"dock_appointment_completed_analytics.golden.json", dockappointment.CompletedEvent{Header: apptHeader(3, ts(6, 9, 40, 10)), DoorCode: door1, CompletedAt: ts(6, 9, 40, 0)}},
		{"dock_appointment_at_risk_analytics.golden.json", dockappointment.AtRisk{
			Header: apptHeader(2, ts(6, 7, 30, 0)), DoorCode: door1, EtaAt: ts(6, 9, 10, 0), WindowStart: hours, WindowEnd: ts(6, 10, 0, 0), Cause: dockappointment.LateEta,
		}},
	}
	for _, c := range cases {
		t.Run(c.golden, func(t *testing.T) {
			got, err := appointmentFactData(c.event)
			if err != nil {
				t.Fatal(err)
			}
			if g, w := asJSONValue(t, got), goldenData(t, c.golden); !reflect.DeepEqual(g, w) {
				t.Fatalf("wire data differs from the golden\n got: %v\nwant: %v", g, w)
			}
		})
	}
}

func TestCalendarFactsRenderTheGoldenWireData(t *testing.T) {
	site := dockcalendar.Header{Site: "WH1", Version: 3, At: ts(5, 15, 0, 0)}
	closure := dockcalendar.Closure{ID: "clo-123e4567-e89b-12d3-a456-426614174001", Door: door1, From: ts(10, 0, 0, 0), To: ts(11, 0, 0, 0), Reason: "Leveller maintenance"}
	monday, err := dockcalendar.NewOperatingHours("", dockcalendar.Mon, "06:00", "22:00", "America/Sao_Paulo")
	if err != nil {
		t.Fatal(err)
	}
	tuesday, err := dockcalendar.NewOperatingHours(door1, dockcalendar.Tue, "08:00", "24:00", "America/Sao_Paulo")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		golden string
		event  dockcalendar.Event
	}{
		{"door_closure_scheduled.golden.json", dockcalendar.ClosureScheduled{Header: site, Closure: closure}},
		{"door_closure_lifted.golden.json", dockcalendar.ClosureLifted{Header: dockcalendar.Header{Site: "WH1", Version: 4, At: ts(7, 9, 0, 0)}, Closure: closure}},
		{"door_operating_hours_declared_analytics.golden.json", dockcalendar.OperatingHoursDeclared{
			Header: dockcalendar.Header{Site: "WH1", Version: 2, At: ts(5, 11, 0, 0)}, Rows: []dockcalendar.OperatingHours{monday, tuesday},
		}},
	}
	for _, c := range cases {
		t.Run(c.golden, func(t *testing.T) {
			got, err := calendarFactData(c.event)
			if err != nil {
				t.Fatal(err)
			}
			if g, w := asJSONValue(t, got), goldenData(t, c.golden); !reflect.DeepEqual(g, w) {
				t.Fatalf("wire data differs from the golden\n got: %v\nwant: %v", g, w)
			}
		})
	}
}

// Optional fields are present-and-null, lists are present-and-empty, never omitted.
func TestUnsetOptionalFieldsAreNullAndEmptyListsAreArrays(t *testing.T) {
	got, err := appointmentFactData(dockappointment.BookedEvent{
		Header: apptHeader(1, ts(5, 12, 0, 0)), Direction: shared.Outbound, DoorCode: door1, Carrier: "ACME", WindowStart: ts(6, 8, 0, 0), WindowEnd: ts(6, 9, 0, 0),
		ShipmentRefs: []string{"SHP-1"}, Provenance: dockappointment.Native,
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(got)
	for _, want := range []string{`"carrier_scac":null`, `"cpt_id":null`, `"expected_seal_number":null`, `"asn_numbers":[]`, `"shipment_refs":["SHP-1"]`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("%s misses %s", raw, want)
		}
	}
	risk, err := appointmentFactData(dockappointment.AtRisk{Header: apptHeader(2, ts(6, 7, 30, 0)), DoorCode: door1, Cause: dockappointment.DoorReassignedOverlap})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(risk)
	if !strings.Contains(string(raw), `"eta_at":null`) || strings.Contains(string(raw), `"version"`) {
		t.Errorf("a door-reassignment advisory has a null eta_at and no version: %s", raw)
	}
}

type unknownAppointmentEvent struct{ dockappointment.Header }

func (unknownAppointmentEvent) EventName() string { return "DockAppointmentTeleported" }

type unknownCalendarEvent struct{ dockcalendar.Header }

func (unknownCalendarEvent) EventName() string { return "DoorTeleported" }

func TestAnUnknownFactHasNoWireShape(t *testing.T) {
	if _, err := appointmentFactData(unknownAppointmentEvent{}); err == nil {
		t.Error("an unknown appointment fact must be refused")
	}
	if _, err := calendarFactData(unknownCalendarEvent{}); err == nil {
		t.Error("an unknown calendar fact must be refused")
	}
}
