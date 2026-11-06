package http_test

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestBookReturns201WithLocationETagAndNullables(t *testing.T) {
	e := newEnv(t)
	rec := e.post("/appointments", inboundBody("D-IN", at8, at10, "ASN-1"))
	wantStatus(t, rec, http.StatusCreated)
	b := decode(t, rec)
	id := b["appointmentId"].(string)
	if got := rec.Header().Get("Location"); got != "/appointments/"+id {
		t.Fatalf("Location = %q", got)
	}
	if got := rec.Header().Get("ETag"); got != `W/"1"` {
		t.Fatalf("ETag = %q", got)
	}
	if b["siteId"] != "WH1" || b["state"] != "BOOKED" || b["provenance"] != "native" || b["atRisk"] != false {
		t.Fatalf("unexpected appointment: %v", b)
	}
	wantNullFields(t, b, "atRiskCause", "cptId", "eta", "arrival", "completedAt", "cancellationReason")
	if refs, ok := b["shipmentRefs"].([]any); !ok || len(refs) != 0 {
		t.Fatalf("shipmentRefs must be an empty array: %v", b["shipmentRefs"])
	}
	if b["expectedSealNumber"] != "SEAL-1" || b["carrierScac"] != "ACMF" {
		t.Fatalf("seal and scac must round-trip: %v", b)
	}
}

// wantNullFields fails unless every named field of the JSON object is present and null.
func wantNullFields(t *testing.T, b map[string]any, names ...string) {
	t.Helper()
	for _, n := range names {
		if v, present := b[n]; !present || v != nil {
			t.Fatalf("%s must be present and null, got %v (present=%v)", n, v, present)
		}
	}
}

func TestBookOutboundOnABothDoorAndInboundOnTheSameDoorDoNotOverlapWhenAdjacent(t *testing.T) {
	e := newEnv(t)
	wantStatus(t, e.post("/appointments", outboundBody("D-BOTH", at8, at10)), http.StatusCreated)
	wantStatus(t, e.post("/appointments", inboundBody("D-BOTH", at10, at12, "ASN-1")), http.StatusCreated)
}

func TestBookRefusalsMapToProblemStatusAndSlug(t *testing.T) {
	e := newEnv(t)
	e.book("D-BOTH", at8, at10, "ASN-1")
	cases := []struct {
		name   string
		body   string
		status int
		slug   string
	}{
		{"unknown door", inboundBody("NOPE", at8, at10, "ASN-2"), 422, "unknown-door"},
		{"direction not allowed by the door", inboundBody("D-OUT", at8, at10, "ASN-2"), 422, "door-flow-mismatch"},
		{"unknown asn", inboundBody("D-IN", at8, at10, "ASN-404"), 422, "unknown-asn"},
		{"window in the past", inboundBody("D-IN", t0.Add(-2*time.Hour), t0.Add(-time.Hour), "ASN-2"), 422, "window-in-past"},
		{"window off the granularity grid", inboundBody("D-IN", at8.Add(7*time.Minute), at10, "ASN-2"), 422, "invalid-window"},
		{"window over four hours", inboundBody("D-IN", at8, at8.Add(5*time.Hour), "ASN-2"), 422, "invalid-window"},
		{"outbound without a reference", fmt.Sprintf(`{"direction":"OUTBOUND","doorCode":"D-OUT","carrier":"ACME","windowStart":%q,"windowEnd":%q}`, at8.Format(time.RFC3339), at10.Format(time.RFC3339)), 422, "missing-outbound-ref"},
		{"blank carrier", strings.Replace(inboundBody("D-IN", at8, at10, "ASN-2"), "ACME Freight", " ", 1), 422, "invalid-carrier"},
		{"bad direction", strings.Replace(inboundBody("D-IN", at8, at10, "ASN-2"), "INBOUND", "SIDEWAYS", 1), 422, "invalid-direction"},
		{"door code with whitespace", inboundBody("D IN", at8, at10, "ASN-2"), 422, "invalid-door-code"},
		{"door double booked", inboundBody("D-BOTH", at8.Add(time.Hour), at10.Add(time.Hour), "ASN-2"), 409, "door-double-booked"},
		{"asn already scheduled", inboundBody("D-IN", at8, at10, "ASN-1"), 409, "asn-already-scheduled"},
		{"unknown field", `{"direction":"INBOUND","bogus":1}`, 400, "malformed-request-body"},
		{"not json", `{"direction":`, 400, "malformed-request-body"},
		{"empty body", ``, 400, "malformed-request-body"},
		{"trailing data", inboundBody("D-IN", at8, at10, "ASN-2") + `{}`, 400, "malformed-request-body"},
		{"bad time", strings.Replace(inboundBody("D-IN", at8, at10, "ASN-2"), at8.Format(time.RFC3339), "tomorrow", 1), 400, "malformed-request-body"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wantProblem(t, e.post("/appointments", tc.body), tc.status, tc.slug)
		})
	}
}

func TestGetAppointment(t *testing.T) {
	e := newEnv(t)
	id := e.book("D-IN", at8, at10, "ASN-1")
	rec := e.get("/appointments/" + id)
	wantStatus(t, rec, http.StatusOK)
	if rec.Header().Get("ETag") != `W/"1"` || decode(t, rec)["appointmentId"] != id {
		t.Fatalf("unexpected GET answer: %s etag=%s", rec.Body.String(), rec.Header().Get("ETag"))
	}
	wantProblem(t, e.get("/appointments/appt-00000000-0000-0000-0000-000000000000"), 404, "appointment-not-found")
	wantProblem(t, e.get("/appointments/not-an-id"), 400, "invalid-path-parameter")
}

func TestListPagesByKeysetAndFilters(t *testing.T) {
	e := newEnv(t)
	e.book("D-IN", at8, at10, "ASN-1")
	e.book("D-BOTH", at10, at12, "ASN-2")
	wantStatus(t, e.post("/appointments", outboundBody("D-OUT", at8, at10)), http.StatusCreated)

	rec := e.get("/appointments?limit=2")
	wantStatus(t, rec, http.StatusOK)
	page1 := decode(t, rec)
	if len(page1["items"].([]any)) != 2 || page1["nextCursor"] == nil {
		t.Fatalf("page one must hold two items and a cursor: %s", rec.Body.String())
	}
	rec = e.get("/appointments?limit=2&cursor=" + url.QueryEscape(page1["nextCursor"].(string)))
	wantStatus(t, rec, http.StatusOK)
	page2 := decode(t, rec)
	if len(page2["items"].([]any)) != 1 || page2["nextCursor"] != nil {
		t.Fatalf("page two must hold the last item and no cursor: %s", rec.Body.String())
	}

	for query, want := range map[string]int{
		"state=BOOKED": 3, "state=CANCELLED": 0, "direction=OUTBOUND": 1, "doorCode=D-IN": 1, "siteId=WH1": 3, "siteId=ELSEWHERE": 0,
		"carrierScac=ACMF": 2, "atRisk=true": 0, "from=" + at10.Format(time.RFC3339) + "&to=" + at12.Format(time.RFC3339): 1,
	} {
		rec := e.get("/appointments?" + query)
		wantStatus(t, rec, http.StatusOK)
		if got := len(decode(t, rec)["items"].([]any)); got != want {
			t.Errorf("%s: %d items, want %d", query, got, want)
		}
	}
}

func TestListRefusesBadQueries(t *testing.T) {
	e := newEnv(t)
	cases := map[string]string{
		"limit=0": "invalid-limit", "limit=501": "invalid-limit", "limit=abc": "invalid-limit", "limit=1&limit=2": "invalid-limit",
		"cursor=%%%": "invalid-query-parameter", "cursor=bm90LWpzb24": "invalid-cursor",
		"after=x": "unknown-query-parameter", "offset=3": "unknown-query-parameter",
		"state=LOST": "invalid-query-parameter", "direction=UP": "invalid-query-parameter", "atRisk=maybe": "invalid-query-parameter",
		"from=yesterday": "invalid-query-parameter", "siteId=a&siteId=b": "invalid-query-parameter",
	}
	for query, slug := range cases {
		t.Run(query, func(t *testing.T) { wantProblem(t, e.get("/appointments?"+query), 400, slug) })
	}
}

func TestRescheduleHonoursIfMatchAndRechecksTheWindow(t *testing.T) {
	e := newEnv(t)
	id := e.book("D-BOTH", at8, at10, "ASN-1")
	e.book("D-BOTH", at12, at12.Add(2*time.Hour), "ASN-2")
	body := fmt.Sprintf(`{"windowStart":%q,"windowEnd":%q}`, at10.Format(time.RFC3339), at12.Format(time.RFC3339))
	path := "/appointments/" + id + "/reschedules"

	p := wantProblem(t, e.do(req{method: http.MethodPost, path: path, body: body, headers: map[string]string{"If-Match": `W/"7"`}}), 412, "precondition-failed")
	if !strings.Contains(p["detail"].(string), "version 1") {
		t.Fatalf("412 detail must name the current version: %v", p["detail"])
	}
	wantProblem(t, e.do(req{method: http.MethodPost, path: path, body: body, headers: map[string]string{"If-Match": "banana"}}), 400, "invalid-if-match")
	wantProblem(t, e.post(path, fmt.Sprintf(`{"windowStart":%q,"windowEnd":%q}`, at12.Format(time.RFC3339), at12.Add(time.Hour).Format(time.RFC3339))), 409, "door-double-booked")

	rec := e.do(req{method: http.MethodPost, path: path, body: body, headers: map[string]string{"If-Match": `W/"1"`}})
	wantStatus(t, rec, http.StatusOK)
	if rec.Header().Get("ETag") != `W/"2"` || decode(t, rec)["windowStart"] != at10.Format(time.RFC3339) {
		t.Fatalf("reschedule must bump the version and move the window: %s", rec.Body.String())
	}

	// A strong tag and "*" are accepted too; the door may change (D-4).
	other := fmt.Sprintf(`{"doorCode":"D-IN","windowStart":%q,"windowEnd":%q}`, at10.Format(time.RFC3339), at12.Format(time.RFC3339))
	rec = e.do(req{method: http.MethodPost, path: path, body: other, headers: map[string]string{"If-Match": `"2"`}})
	wantStatus(t, rec, http.StatusOK)
	if decode(t, rec)["doorCode"] != "D-IN" {
		t.Fatalf("a reschedule may change the door: %s", rec.Body.String())
	}
	wantStatus(t, e.do(req{method: http.MethodPost, path: path, body: body, headers: map[string]string{"If-Match": "*"}}), http.StatusOK)
	wantProblem(t, e.post("/appointments/not-an-id/reschedules", body), 400, "invalid-path-parameter")
	wantProblem(t, e.post("/appointments/appt-00000000-0000-0000-0000-000000000000/reschedules", body), 404, "appointment-not-found")
}

func TestCancelNarrowsTheReasonsAndFreesTheDoor(t *testing.T) {
	e := newEnv(t)
	id := e.book("D-IN", at8, at10, "ASN-1")
	path := "/appointments/" + id + "/cancellations"

	for _, reason := range []string{"TURNED_AWAY", "ASN_CANCELLED", "BORED", ""} {
		wantProblem(t, e.post(path, fmt.Sprintf(`{"reason":%q}`, reason)), 422, "invalid-reason")
	}
	wantProblem(t, e.do(req{method: http.MethodPost, path: path, body: `{"reason":"SITE_REQUEST"}`, headers: map[string]string{"If-Match": `W/"9"`}}), 412, "precondition-failed")

	rec := e.post(path, `{"reason":"SITE_REQUEST"}`)
	wantStatus(t, rec, http.StatusOK)
	b := decode(t, rec)
	if b["state"] != "CANCELLED" || b["cancellationReason"] != "SITE_REQUEST" || rec.Header().Get("ETag") != `W/"2"` {
		t.Fatalf("unexpected cancelled appointment: %s", rec.Body.String())
	}
	wantProblem(t, e.post(path, `{"reason":"CARRIER_REQUEST"}`), 409, "illegal-transition")
	e.book("D-IN", at8, at10, "ASN-1") // the door window and the ASN are free again
}

func TestReportEtaFlagsALateAppointmentAtRisk(t *testing.T) {
	e := newEnv(t)
	id := e.book("D-IN", at8, at10, "ASN-1")
	path := "/appointments/" + id + "/eta-reports"

	rec := e.post(path, fmt.Sprintf(`{"etaAt":%q,"source":"carrier-portal"}`, at8.Add(30*time.Minute).Format(time.RFC3339)))
	wantStatus(t, rec, http.StatusCreated)
	res := decode(t, rec)
	appt := res["appointment"].(map[string]any)
	if res["atRisk"] != true || appt["atRisk"] != true || appt["atRiskCause"] != "LATE_ETA" || rec.Header().Get("ETag") != `W/"2"` {
		t.Fatalf("a late ETA must flag the appointment: %s", rec.Body.String())
	}
	if eta := appt["eta"].(map[string]any); eta["source"] != "carrier-portal" {
		t.Fatalf("eta source = %v", eta["source"])
	}
	listed := decode(t, e.get("/appointments?atRisk=true"))["items"].([]any)
	if len(listed) != 1 {
		t.Fatalf("atRisk=true must list the flagged appointment, got %d", len(listed))
	}

	rec = e.post(path, fmt.Sprintf(`{"etaAt":%q}`, at8.Format(time.RFC3339)))
	wantStatus(t, rec, http.StatusCreated)
	if decode(t, rec)["atRisk"] != false {
		t.Fatalf("an on-time ETA is not at risk: %s", rec.Body.String())
	}
	wantProblem(t, e.post(path, `{}`), 422, "invalid-eta")
	wantProblem(t, e.post("/appointments/appt-00000000-0000-0000-0000-000000000000/eta-reports", `{"etaAt":"2030-01-07T08:00:00Z"}`), 404, "appointment-not-found")
}

func TestDoorAvailabilityShowsBusyAndFreeSpans(t *testing.T) {
	e := newEnv(t)
	id := e.book("D-IN", at8, at10, "ASN-1")
	q := "?from=" + at8.Format(time.RFC3339) + "&to=" + at12.Format(time.RFC3339)
	rec := e.get("/doors/D-IN/availability" + q)
	wantStatus(t, rec, http.StatusOK)
	b := decode(t, rec)
	free, busy := b["freeWindows"].([]any), b["busy"].([]any)
	if len(free) != 1 || len(busy) != 1 || b["siteId"] != "WH1" || b["granularityMinutes"] != float64(15) {
		t.Fatalf("unexpected availability: %s", rec.Body.String())
	}
	if span := busy[0].(map[string]any); span["kind"] != "APPOINTMENT" || span["ref"] != id {
		t.Fatalf("busy span = %v", span)
	}
	wantStatus(t, e.get("/doors/D-IN/availability"+q+"&minutes=30"), http.StatusOK)

	wantProblem(t, e.get("/doors/NOPE/availability"+q), 404, "door-not-found")
	bad := []struct{ query, slug string }{
		{"", "invalid-availability-query"},
		{"?from=" + at8.Format(time.RFC3339), "invalid-availability-query"},
		{"?from=" + at12.Format(time.RFC3339) + "&to=" + at8.Format(time.RFC3339), "invalid-availability-query"},
		{q + "&minutes=0", "invalid-query-parameter"},
		{q + "&minutes=x", "invalid-query-parameter"},
		{q + "&limit=1", "unknown-query-parameter"},
	}
	for _, tc := range bad {
		wantProblem(t, e.get("/doors/D-IN/availability"+tc.query), 400, tc.slug)
	}
}
