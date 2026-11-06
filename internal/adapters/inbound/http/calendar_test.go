package http_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

const hoursBody = `{"operatingHours":[{"weekday":"MON","opensAt":"06:00","closesAt":"22:00","timezone":"UTC"},{"doorCode":"D-IN","weekday":"MON","opensAt":"08:00","closesAt":"16:00","timezone":"UTC"}]}`

func TestDockCalendarDefaultsAndSettings(t *testing.T) {
	e := newEnv(t)
	rec := e.get("/sites/WH1/dock-calendar")
	wantStatus(t, rec, http.StatusOK)
	b := decode(t, rec)
	s := b["settings"].(map[string]any)
	if b["version"] != float64(0) || s["maxWindowMinutes"] != float64(240) || s["slotGranularityMinutes"] != float64(15) ||
		s["arrivalToleranceMinutes"] != float64(15) || s["noShowGraceMinutes"] != float64(60) {
		t.Fatalf("a site that declared nothing answers the defaults at version 0: %s", rec.Body.String())
	}
	if len(b["operatingHours"].([]any)) != 0 || len(b["closures"].([]any)) != 0 {
		t.Fatalf("no hours and no closures by default: %s", rec.Body.String())
	}

	rec = e.put("/sites/WH1/dock-calendar/settings", `{"maxWindowMinutes":120,"slotGranularityMinutes":30,"arrivalToleranceMinutes":5,"noShowGraceMinutes":90}`)
	wantStatus(t, rec, http.StatusOK)
	if got := decode(t, rec)["settings"].(map[string]any); got["maxWindowMinutes"] != float64(120) || got["noShowGraceMinutes"] != float64(90) {
		t.Fatalf("settings not applied: %s", rec.Body.String())
	}
	wantProblem(t, e.put("/sites/WH1/dock-calendar/settings", `{"maxWindowMinutes":0,"slotGranularityMinutes":15,"arrivalToleranceMinutes":15,"noShowGraceMinutes":60}`), 422, "invalid-settings")
	wantProblem(t, e.put("/sites/WH1/dock-calendar/settings", `{"maxWindowMinutes":60}x`), 400, "malformed-request-body")
	wantProblem(t, e.get("/sites/"+strings.Repeat("x", 65)+"/dock-calendar"), 400, "invalid-path-parameter")
}

func TestOperatingHoursRoundTripAndGateBookings(t *testing.T) {
	e := newEnv(t)
	rec := e.put("/sites/WH1/dock-calendar/operating-hours", hoursBody)
	wantStatus(t, rec, http.StatusOK)
	rows := decode(t, rec)["operatingHours"].([]any)
	if len(rows) != 2 {
		t.Fatalf("both rows must round-trip: %s", rec.Body.String())
	}
	var siteDefault, ownRow int
	for _, r := range rows {
		if r.(map[string]any)["doorCode"] == nil {
			siteDefault++
		} else {
			ownRow++
		}
	}
	if siteDefault != 1 || ownRow != 1 {
		t.Fatalf("a site-default row has a null doorCode: %s", rec.Body.String())
	}
	// D-IN is open 08:00-16:00 on Monday: 07:00-09:00 does not fit.
	wantProblem(t, e.post("/appointments", inboundBody("D-IN", at8.Add(-time.Hour), at8.Add(time.Hour), "ASN-1")), 409, "door-unavailable")
	wantStatus(t, e.post("/appointments", inboundBody("D-IN", at8, at10, "ASN-1")), http.StatusCreated)

	// D-8: hours that strand the active appointment are refused.
	narrow := `{"operatingHours":[{"doorCode":"D-IN","weekday":"MON","opensAt":"12:00","closesAt":"14:00","timezone":"UTC"}]}`
	p := wantProblem(t, e.put("/sites/WH1/dock-calendar/operating-hours", narrow), 409, "hours-conflict-with-appointments")
	if !strings.Contains(p["detail"].(string), "appt-") {
		t.Fatalf("the refusal lists the appointment ids: %v", p["detail"])
	}
	wantProblem(t, e.put("/sites/WH1/dock-calendar/operating-hours", `{"operatingHours":[{"weekday":"MON","opensAt":"20:00","closesAt":"08:00","timezone":"UTC"}]}`), 422, "invalid-operating-hours")
	wantProblem(t, e.put("/sites/WH1/dock-calendar/operating-hours", `{"operatingHours":[{"weekday":"MON","opensAt":"06:00","closesAt":"22:00","timezone":"UTC","bogus":1}]}`), 400, "malformed-request-body")
}

func TestClosureLifecycle(t *testing.T) {
	e := newEnv(t)
	body := fmt.Sprintf(`{"doorCode":"D-IN","from":%q,"to":%q,"reason":"Leveller maintenance"}`, at8.Format(time.RFC3339), at10.Format(time.RFC3339))
	rec := e.post("/sites/WH1/dock-calendar/closures", body)
	wantStatus(t, rec, http.StatusCreated)
	c := decode(t, rec)
	id := c["closureId"].(string)
	if c["siteId"] != "WH1" || c["lifted"] != false || c["reason"] != "Leveller maintenance" {
		t.Fatalf("unexpected closure: %s", rec.Body.String())
	}
	if got := decode(t, e.get("/sites/WH1/dock-calendar"))["closures"].([]any); len(got) != 1 {
		t.Fatalf("the calendar lists the scheduled closure, got %d", len(got))
	}

	wantProblem(t, e.post("/sites/WH1/dock-calendar/closures", body), 409, "overlapping-closure")
	wantProblem(t, e.post("/appointments", inboundBody("D-IN", at8, at10, "ASN-1")), 409, "door-unavailable")

	lift := "/sites/WH1/dock-calendar/closures/" + id + "/lifts"
	rec = e.post(lift, "")
	wantStatus(t, rec, http.StatusOK)
	if decode(t, rec)["lifted"] != true {
		t.Fatalf("closure must be lifted: %s", rec.Body.String())
	}
	wantProblem(t, e.post(lift, ""), 409, "closure-already-lifted")
	wantProblem(t, e.post("/sites/WH1/dock-calendar/closures/clo-00000000-0000-0000-0000-000000000000/lifts", ""), 404, "unknown-closure")
	wantStatus(t, e.post("/appointments", inboundBody("D-IN", at8, at10, "ASN-1")), http.StatusCreated)
}

func TestClosureRefusals(t *testing.T) {
	e := newEnv(t)
	e.book("D-IN", at8, at10, "ASN-1")
	closure := func(door string, from, to time.Time, reason string) string {
		return fmt.Sprintf(`{"doorCode":%q,"from":%q,"to":%q,"reason":%q}`, door, from.Format(time.RFC3339), to.Format(time.RFC3339), reason)
	}
	p := wantProblem(t, e.post("/sites/WH1/dock-calendar/closures", closure("D-IN", at8, at10, "x")), 409, "closure-conflicts-with-appointments")
	if !strings.Contains(p["detail"].(string), "appt-") {
		t.Fatalf("the refusal lists the appointment ids: %v", p["detail"])
	}
	wantProblem(t, e.post("/sites/WH1/dock-calendar/closures", closure("NOPE", at8, at10, "x")), 422, "unknown-door")
	wantProblem(t, e.post("/sites/OTHER/dock-calendar/closures", closure("D-IN", at12, at12.Add(time.Hour), "x")), 422, "unknown-door")
	wantProblem(t, e.post("/sites/WH1/dock-calendar/closures", closure("D-IN", at10, at8, "x")), 422, "invalid-closure")
	wantProblem(t, e.post("/sites/WH1/dock-calendar/closures", closure("D-IN", at12, at12.Add(time.Hour), "")), 422, "invalid-closure")
}

func TestNoShowSweepMarksOnlyAppointmentsPastTheGrace(t *testing.T) {
	e := newEnv(t)
	late := e.book("D-IN", at8, at10, "ASN-1")
	soon := e.book("D-BOTH", at10, at12, "ASN-2")

	// 10:30: the first window ended at 10:00, grace 60 min -> not yet.
	e.clock.now = at10.Add(30 * time.Minute)
	rec := e.post("/no-show-sweeps", "")
	wantStatus(t, rec, http.StatusOK)
	if b := decode(t, rec); b["count"] != float64(0) || len(b["appointmentIds"].([]any)) != 0 {
		t.Fatalf("inside the grace nothing is marked: %s", rec.Body.String())
	}

	e.clock.now = at10.Add(61 * time.Minute)
	rec = e.post("/no-show-sweeps", "")
	wantStatus(t, rec, http.StatusOK)
	b := decode(t, rec)
	if ids := b["appointmentIds"].([]any); b["count"] != float64(1) || len(ids) != 1 || ids[0] != late {
		t.Fatalf("only the first appointment is past window end + grace: %s", rec.Body.String())
	}
	if decode(t, e.get("/appointments/"+late))["state"] != "NO_SHOW" || decode(t, e.get("/appointments/"+soon))["state"] != "BOOKED" {
		t.Fatalf("states after the sweep are wrong")
	}
	rec = e.post("/no-show-sweeps", "")
	if decode(t, rec)["count"] != float64(0) {
		t.Fatalf("a second sweep marks nothing new (naturally idempotent): %s", rec.Body.String())
	}
}
