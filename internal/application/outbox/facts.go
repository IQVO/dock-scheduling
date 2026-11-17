package outbox

import (
	"fmt"
	"time"

	"github.com/claudioed/dock-scheduling/internal/domain/dockappointment"
	"github.com/claudioed/dock-scheduling/internal/domain/dockcalendar"
	"github.com/claudioed/dock-scheduling/internal/domain/shared"
	"github.com/claudioed/dock-scheduling/internal/domain/standards"
)

// Nullable returns a *string of s, or nil when s is empty: the wire fields
// the AsyncAPI schemas mark nullable (carrier_scac, cpt_id,
// expected_seal_number, visit_id, door_code) are PRESENT and null when unset,
// never omitted and never "".
func Nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// NullableTime is Nullable for an instant: the zero time is null.
func NullableTime(t time.Time) *time.Time {
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

// GLNLookup resolves the GS1 GLN of a door code from the local copy of
// facility-layout's doors (ADR 0003, ADR 0013): "" when the door is unknown or
// has no GLN. It runs where the outbox row is built, in the same transaction,
// and never calls another context.
type GLNLookup func(doorCode string) (string, error)

// standardFields are the optional ADR 0013 fields every door-bound fact can
// carry. A field with no honest value is omitted (not null), so a payload that
// carries none is byte-identical to one built before the fields existed.
type standardFields struct {
	X12Status    string `json:"x12_status,omitempty"`
	CBVBizStep   string `json:"cbv_biz_step,omitempty"`
	ReadPointGLN string `json:"read_point_gln,omitempty"`
}

// standardOf is the ONE place the mapping table and the door's GLN meet a fact.
func standardOf(event string, flow shared.Direction, door string, gln GLNLookup) (standardFields, error) {
	s, _ := standards.Of(event, flow)
	out := standardFields{X12Status: s.X12Status, CBVBizStep: s.CBVBizStep}
	if gln == nil || door == "" {
		return out, nil
	}
	g, err := gln(door)
	if err != nil {
		return standardFields{}, fmt.Errorf("resolve the GLN of door %s: %w", door, err)
	}
	out.ReadPointGLN = g
	return out, nil
}

// AppointmentFactData renders one appointment fact's data in the pinned wire
// shape of ADR 0004 (snake_case keys, RFC 3339 UTC times) plus the optional
// standard fields of ADR 0013. The read point is the door where the fact
// happened (the new door for a reschedule or a reassignment). The domain raises
// exactly the eight facts below; anything else is a programming error, not a
// runtime condition. gln may be nil (no GLN is ever published then).
func AppointmentFactData(e dockappointment.Event, gln GLNLookup) (any, error) {
	switch f := e.(type) {
	case dockappointment.BookedEvent:
		std, err := standardOf(f.EventName(), f.Flow, f.DoorCode, gln)
		return bookedData{
			AppointmentID: f.AppointmentID(), SiteID: f.SiteID(), Direction: string(f.Direction), DoorCode: f.DoorCode, Carrier: f.Carrier,
			CarrierSCAC: Nullable(f.CarrierSCAC), WindowStart: f.WindowStart.UTC(), WindowEnd: f.WindowEnd.UTC(),
			ASNNumbers: list(f.ASNNumbers), CPTID: Nullable(f.CPTID), ShipmentRefs: list(f.ShipmentRefs),
			ExpectedSealNumber: Nullable(f.ExpectedSeal), ArrivalToleranceMinutes: f.ToleranceMinutes, Provenance: string(f.Provenance),
			Version: f.AppointmentVersion(), standardFields: std,
		}, err
	case dockappointment.RescheduledEvent:
		std, err := standardOf(f.EventName(), f.Flow, f.DoorCode, gln)
		return rescheduledData{
			AppointmentID: f.AppointmentID(), SiteID: f.SiteID(), DoorCode: f.DoorCode, PreviousDoorCode: f.PreviousDoorCode,
			WindowStart: f.WindowStart.UTC(), WindowEnd: f.WindowEnd.UTC(),
			PreviousWindowStart: f.PreviousWindowStart.UTC(), PreviousWindowEnd: f.PreviousWindowEnd.UTC(),
			ArrivalToleranceMinutes: f.ToleranceMinutes, Version: f.AppointmentVersion(), standardFields: std,
		}, err
	case dockappointment.CancelledEvent:
		std, err := standardOf(f.EventName(), f.Flow, f.DoorCode, gln)
		return cancelledData{AppointmentID: f.AppointmentID(), SiteID: f.SiteID(), DoorCode: f.DoorCode, Reason: string(f.Reason), Version: f.AppointmentVersion(), standardFields: std}, err
	case dockappointment.NoShowEvent:
		std, err := standardOf(f.EventName(), f.Flow, f.DoorCode, gln)
		return noShowData{
			AppointmentID: f.AppointmentID(), SiteID: f.SiteID(), DoorCode: f.DoorCode, Direction: string(f.Direction),
			CarrierSCAC: Nullable(f.CarrierSCAC), WindowEnd: f.WindowEnd.UTC(), Version: f.AppointmentVersion(), standardFields: std,
		}, err
	case dockappointment.CheckedInEvent:
		std, err := standardOf(f.EventName(), f.Flow, f.DoorCode, gln)
		return checkedInData{
			AppointmentID: f.AppointmentID(), SiteID: f.SiteID(), DoorCode: f.DoorCode, CheckedInAt: f.CheckedInAt.UTC(),
			ArrivalStatus: string(f.ArrivalStatus), VisitID: Nullable(f.VisitID), Version: f.AppointmentVersion(), standardFields: std,
		}, err
	case dockappointment.DoorReassigned:
		std, err := standardOf(f.EventName(), f.Flow, f.ToDoorCode, gln)
		return doorReassignedData{
			AppointmentID: f.AppointmentID(), SiteID: f.SiteID(), FromDoorCode: f.FromDoorCode, ToDoorCode: f.ToDoorCode, Version: f.AppointmentVersion(), standardFields: std,
		}, err
	case dockappointment.CompletedEvent:
		std, err := standardOf(f.EventName(), f.Flow, f.DoorCode, gln)
		return completedData{AppointmentID: f.AppointmentID(), SiteID: f.SiteID(), DoorCode: f.DoorCode, CompletedAt: f.CompletedAt.UTC(), Version: f.AppointmentVersion(), standardFields: std}, err
	case dockappointment.AtRisk:
		std, err := standardOf(f.EventName(), f.Flow, f.DoorCode, gln)
		return atRiskData{
			AppointmentID: f.AppointmentID(), SiteID: f.SiteID(), DoorCode: f.DoorCode, EtaAt: NullableTime(f.EtaAt),
			WindowStart: f.WindowStart.UTC(), WindowEnd: f.WindowEnd.UTC(), Cause: string(f.Cause), standardFields: std,
		}, err
	default:
		return nil, fmt.Errorf("unknown dock appointment fact %T: no wire shape is declared for it", e)
	}
}

// CalendarFactData renders one calendar fact's data (ADR 0004, ADR 0013). A
// door closure has that door as its read point; the operating-hours declaration
// (rows of several doors) has none.
func CalendarFactData(e dockcalendar.Event, gln GLNLookup) (any, error) {
	switch f := e.(type) {
	case dockcalendar.ClosureScheduled:
		std, err := standardOf(f.EventName(), "", string(f.Closure.Door), gln)
		return closureScheduledData{
			SiteID: f.SiteID(), ClosureID: f.Closure.ID, DoorCode: string(f.Closure.Door), From: f.Closure.From.UTC(), To: f.Closure.To.UTC(),
			Reason: f.Closure.Reason, Version: f.CalendarVersion(), standardFields: std,
		}, err
	case dockcalendar.ClosureLifted:
		std, err := standardOf(f.EventName(), "", string(f.Closure.Door), gln)
		return closureLiftedData{SiteID: f.SiteID(), ClosureID: f.Closure.ID, DoorCode: string(f.Closure.Door), Version: f.CalendarVersion(), standardFields: std}, err
	case dockcalendar.OperatingHoursDeclared:
		rows := make([]operatingHoursRow, len(f.Rows))
		for i, r := range f.Rows {
			rows[i] = operatingHoursRow{
				DoorCode: Nullable(string(r.DoorCode())), Weekday: string(r.Weekday()), OpensAt: r.OpensAt(), ClosesAt: r.ClosesAt(), Timezone: r.Timezone(),
			}
		}
		// The declaration replaces every row of the site, so the event-level
		// door_code is always null; each row names its own door (null = site default).
		return operatingHoursDeclaredData{SiteID: f.SiteID(), OperatingHours: rows, Version: f.CalendarVersion()}, nil
	default:
		return nil, fmt.Errorf("unknown dock calendar fact %T: no wire shape is declared for it", e)
	}
}
