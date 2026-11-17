package http_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const acmfBody = `{"name":"ACME Freight","usdot":"1234567","mc":"MC-654321"}`

func (e *env) patch(path, body string, headers map[string]string) *httptest.ResponseRecorder {
	return e.do(req{method: http.MethodPatch, path: path, body: body, headers: headers})
}

func (e *env) register(scac, body string) {
	e.t.Helper()
	rec := e.put("/carriers/"+scac, body)
	if rec.Code != http.StatusCreated {
		e.t.Fatalf("register %s = %d: %s", scac, rec.Code, rec.Body.String())
	}
}

func TestPutCarrierRegisters(t *testing.T) {
	e := newEnv(t)
	rec := e.put("/carriers/ACMF", acmfBody)
	wantStatus(t, rec, http.StatusCreated)
	if rec.Header().Get("Location") != "/carriers/ACMF" || rec.Header().Get("ETag") != `W/"1"` {
		t.Fatalf("headers = %v", rec.Header())
	}
	b := decode(t, rec)
	if b["scac"] != "ACMF" || b["name"] != "ACME Freight" || b["usdot"] != "1234567" || b["mc"] != "MC654321" || b["status"] != "ACTIVE" || b["version"] != float64(1) {
		t.Fatalf("body = %s", rec.Body.String())
	}
	if b["suspensionReason"] != nil || b["suspendedAt"] != nil || b["createdAt"] != "2030-01-07T06:00:00Z" || b["updatedAt"] != "2030-01-07T06:00:00Z" {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestPutCarrierOfTheSameDataIsANoOpAndOfOtherDataIsRefused(t *testing.T) {
	e := newEnv(t)
	e.register("ACMF", acmfBody)
	again := e.put("/carriers/ACMF", `{"name":"ACME Freight","usdot":"1234567","mc":"654321"}`) // same data, the MC spelled otherwise
	wantStatus(t, again, http.StatusOK)
	if again.Header().Get("Location") != "" || again.Header().Get("ETag") != `W/"1"` || decode(t, again)["version"] != float64(1) {
		t.Fatalf("a registration of the same data is a 200 no-op: %v %s", again.Header(), again.Body.String())
	}
	if n := len(e.store.Published()); n != 1 {
		t.Fatalf("one CarrierRegistered, got %d events", n)
	}

	other := wantProblem(t, e.put("/carriers/ACMF", `{"name":"ACME Freight Lines","usdot":"1234567","mc":"MC654321"}`), 409, "carrier-already-registered")
	if !strings.Contains(other["detail"].(string), "ACMF") {
		t.Fatalf("detail = %v", other["detail"])
	}
}

func TestPutCarrierWithoutOptionalIdentifiers(t *testing.T) {
	e := newEnv(t)
	rec := e.put("/carriers/AB", `{"name":"Two Letters"}`)
	wantStatus(t, rec, http.StatusCreated)
	if b := decode(t, rec); b["usdot"] != nil || b["mc"] != nil {
		t.Fatalf("absent identifiers are null: %s", rec.Body.String())
	}
}

func TestPutCarrierRefusals(t *testing.T) {
	e := newEnv(t)
	for name, tc := range map[string]struct {
		path, body, slug string
		status           int
	}{
		"lower case scac":  {"/carriers/acmf", acmfBody, "invalid-scac", 422},
		"five letters":     {"/carriers/ACMFX", acmfBody, "invalid-scac", 422},
		"digits":           {"/carriers/AC1", acmfBody, "invalid-scac", 422},
		"blank name":       {"/carriers/ACMF", `{"name":" "}`, "invalid-carrier-name", 422},
		"missing name":     {"/carriers/ACMF", `{"usdot":"1"}`, "invalid-carrier-name", 422},
		"long usdot":       {"/carriers/ACMF", `{"name":"A","usdot":"123456789"}`, "invalid-usdot", 422},
		"alpha usdot":      {"/carriers/ACMF", `{"name":"A","usdot":"12a"}`, "invalid-usdot", 422},
		"bad mc":           {"/carriers/ACMF", `{"name":"A","mc":"FF123"}`, "invalid-mc", 422},
		"unknown field":    {"/carriers/ACMF", `{"name":"A","status":"SUSPENDED"}`, "malformed-request-body", 400},
		"not json":         {"/carriers/ACMF", `nope`, "malformed-request-body", 400},
		"usdot not a text": {"/carriers/ACMF", `{"name":"A","usdot":12}`, "malformed-request-body", 400},
		"empty body":       {"/carriers/ACMF", ``, "malformed-request-body", 400},
		"trailing garbage": {"/carriers/ACMF", `{"name":"A"}x`, "malformed-request-body", 400},
	} {
		rec := e.put(tc.path, tc.body)
		if rec.Code != tc.status {
			t.Errorf("%s: status %d, want %d: %s", name, rec.Code, tc.status, rec.Body.String())
			continue
		}
		wantProblem(t, rec, tc.status, tc.slug)
	}
	if len(e.store.Published()) != 0 {
		t.Fatal("a refused registration publishes nothing")
	}
}

func TestGetCarrier(t *testing.T) {
	e := newEnv(t)
	e.register("ACMF", acmfBody)
	rec := e.get("/carriers/ACMF")
	wantStatus(t, rec, http.StatusOK)
	if rec.Header().Get("ETag") != `W/"1"` || decode(t, rec)["scac"] != "ACMF" {
		t.Fatalf("GET = %v %s", rec.Header(), rec.Body.String())
	}
	wantProblem(t, e.get("/carriers/NOPE"), 404, "carrier-not-found")
	wantProblem(t, e.get("/carriers/nope"), 422, "invalid-scac")
}

func TestListCarriersPagesAndFilters(t *testing.T) {
	e := newEnv(t)
	for _, s := range []string{"CCCC", "AAAA", "BBBB"} {
		e.register(s, fmt.Sprintf(`{"name":"Carrier %s"}`, s))
	}
	wantStatus(t, e.post("/carriers/BBBB/suspensions", `{"reason":"insurance lapsed"}`), http.StatusOK)

	rec := e.get("/carriers?limit=2")
	wantStatus(t, rec, http.StatusOK)
	b := decode(t, rec)
	items := b["items"].([]any)
	if len(items) != 2 || items[0].(map[string]any)["scac"] != "AAAA" || items[1].(map[string]any)["scac"] != "BBBB" || b["nextCursor"] == nil {
		t.Fatalf("first page = %s", rec.Body.String())
	}
	rec = e.get("/carriers?limit=2&cursor=" + b["nextCursor"].(string))
	b = decode(t, rec)
	items = b["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["scac"] != "CCCC" {
		t.Fatalf("second page = %s", rec.Body.String())
	}
	if _, present := b["nextCursor"]; present {
		t.Fatalf("the last page has no nextCursor: %s", rec.Body.String())
	}

	rec = e.get("/carriers?status=SUSPENDED")
	items = decode(t, rec)["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["scac"] != "BBBB" || items[0].(map[string]any)["status"] != "SUSPENDED" {
		t.Fatalf("suspended = %s", rec.Body.String())
	}
	if rec := e.get("/carriers?status=ACTIVE"); len(decode(t, rec)["items"].([]any)) != 2 {
		t.Fatalf("active = %s", rec.Body.String())
	}
	empty := newEnv(t).get("/carriers")
	if body := strings.TrimSpace(empty.Body.String()); body != `{"items":[]}` {
		t.Fatalf("an empty registry is {\"items\":[]}: %s", body)
	}
}

func TestListCarriersRefusals(t *testing.T) {
	e := newEnv(t)
	wantProblem(t, e.get("/carriers?status=GONE"), 400, "invalid-query-parameter")
	wantProblem(t, e.get("/carriers?status=ACTIVE&status=SUSPENDED"), 400, "invalid-query-parameter")
	wantProblem(t, e.get("/carriers?scac=ACMF"), 400, "unknown-query-parameter")
	wantProblem(t, e.get("/carriers?limit=0"), 400, "invalid-limit")
	wantProblem(t, e.get("/carriers?cursor=!!"), 400, "invalid-cursor")
	// a well-formed page token whose id is not a SCAC (the appointment cursor shape)
	wantProblem(t, e.get("/carriers?cursor=eyJzIjoiMjAzMC0wMS0wN1QwODowMDowMFoiLCJpIjoiYXBwdC0xIn0"), 400, "invalid-cursor")
}

func TestPatchCarrierIsAMergePatch(t *testing.T) {
	e := newEnv(t)
	e.register("ACMF", acmfBody)
	e.clock.now = at8
	rec := e.patch("/carriers/ACMF", `{"name":"ACME Freight Lines","mc":"mx9"}`, map[string]string{"Idempotency-Key": "p-1", "If-Match": `W/"1"`})
	wantStatus(t, rec, http.StatusOK)
	b := decode(t, rec)
	if b["name"] != "ACME Freight Lines" || b["usdot"] != "1234567" || b["mc"] != "MX9" || b["version"] != float64(2) || rec.Header().Get("ETag") != `W/"2"` || b["updatedAt"] != "2030-01-07T08:00:00Z" {
		t.Fatalf("patched = %s", rec.Body.String())
	}
	// null clears an optional identifier; an absent one is kept.
	rec = e.patch("/carriers/ACMF", `{"usdot":null}`, map[string]string{"Idempotency-Key": "p-2"})
	if b := decode(t, rec); b["usdot"] != nil || b["mc"] != "MX9" || b["version"] != float64(3) {
		t.Fatalf("null clears: %s", rec.Body.String())
	}
	rec = e.patch("/carriers/ACMF", `{"mc":""}`, map[string]string{"Idempotency-Key": "p-3"})
	if b := decode(t, rec); b["mc"] != nil || b["version"] != float64(4) {
		t.Fatalf("an empty string clears too: %s", rec.Body.String())
	}
	// a patch that changes nothing is a 200 with the same version and no event
	before := len(e.store.Published())
	rec = e.patch("/carriers/ACMF", `{}`, map[string]string{"Idempotency-Key": "p-4"})
	wantStatus(t, rec, http.StatusOK)
	if decode(t, rec)["version"] != float64(4) || len(e.store.Published()) != before {
		t.Fatalf("a no-op patch: %s", rec.Body.String())
	}
	if got := e.store.Published(); len(got) != 4 {
		t.Fatalf("registration + 3 changes = 4 CarrierRegistered, got %d", len(got))
	}
}

func TestPatchCarrierRefusals(t *testing.T) {
	e := newEnv(t)
	e.register("ACMF", acmfBody)
	k := func(key string) map[string]string { return map[string]string{"Idempotency-Key": key} }
	wantProblem(t, e.patch("/carriers/ACMF", `{"name":"x"}`, nil), 400, "idempotency-key-required")
	wantProblem(t, e.patch("/carriers/NOPE", `{"name":"x"}`, k("a")), 404, "carrier-not-found")
	wantProblem(t, e.patch("/carriers/acmf", `{"name":"x"}`, k("b")), 422, "invalid-scac")
	wantProblem(t, e.patch("/carriers/ACMF", `{"name":null}`, k("c")), 422, "invalid-carrier-name")
	wantProblem(t, e.patch("/carriers/ACMF", `{"name":" "}`, k("d")), 422, "invalid-carrier-name")
	wantProblem(t, e.patch("/carriers/ACMF", `{"usdot":"abc"}`, k("e")), 422, "invalid-usdot")
	wantProblem(t, e.patch("/carriers/ACMF", `{"mc":"FF1"}`, k("f")), 422, "invalid-mc")
	wantProblem(t, e.patch("/carriers/ACMF", `{"usdot":5}`, k("g")), 400, "malformed-request-body")
	wantProblem(t, e.patch("/carriers/ACMF", `{"status":"SUSPENDED"}`, k("h")), 400, "malformed-request-body")
	wantProblem(t, e.patch("/carriers/ACMF", `{"name":"x"}`, map[string]string{"Idempotency-Key": "i", "If-Match": `W/"9"`}), 412, "precondition-failed")
	wantProblem(t, e.patch("/carriers/ACMF", `{"name":"x"}`, map[string]string{"Idempotency-Key": "j", "If-Match": "banana"}), 400, "invalid-if-match")
	if len(e.store.Published()) != 1 {
		t.Fatal("refused patches publish nothing")
	}
}

func TestSuspendAndReinstateOverRest(t *testing.T) {
	e := newEnv(t)
	e.register("ACMF", acmfBody)
	e.clock.now = at8
	rec := e.do(req{method: http.MethodPost, path: "/carriers/ACMF/suspensions", body: `{"reason":"insurance lapsed"}`, headers: map[string]string{"If-Match": `W/"1"`}})
	wantStatus(t, rec, http.StatusOK)
	b := decode(t, rec)
	if b["status"] != "SUSPENDED" || b["suspensionReason"] != "insurance lapsed" || b["suspendedAt"] != "2030-01-07T08:00:00Z" || b["version"] != float64(2) || rec.Header().Get("ETag") != `W/"2"` {
		t.Fatalf("suspended = %s", rec.Body.String())
	}
	wantProblem(t, e.post("/carriers/ACMF/suspensions", `{"reason":"again"}`), 409, "carrier-already-suspended")
	wantProblem(t, e.post("/carriers/ACMF/suspensions", `{"reason":" "}`), 422, "invalid-suspension-reason")
	wantProblem(t, e.post("/carriers/ACMF/suspensions", `{}`), 422, "invalid-suspension-reason")
	wantProblem(t, e.post("/carriers/ACMF/suspensions", `{"reason":"x","extra":1}`), 400, "malformed-request-body")
	wantProblem(t, e.post("/carriers/NOPE/suspensions", `{"reason":"x"}`), 404, "carrier-not-found")
	wantProblem(t, e.post("/carriers/nope/suspensions", `{"reason":"x"}`), 422, "invalid-scac")
	wantProblem(t, e.do(req{method: http.MethodPost, path: "/carriers/ACMF/reinstatements", headers: map[string]string{"If-Match": `W/"1"`}}), 412, "precondition-failed")

	rec = e.post("/carriers/ACMF/reinstatements", "")
	wantStatus(t, rec, http.StatusOK)
	if b := decode(t, rec); b["status"] != "ACTIVE" || b["suspensionReason"] != nil || b["suspendedAt"] != nil || b["version"] != float64(3) {
		t.Fatalf("reinstated = %s", rec.Body.String())
	}
	wantProblem(t, e.post("/carriers/ACMF/reinstatements", ""), 409, "carrier-not-suspended")
	wantProblem(t, e.post("/carriers/NOPE/reinstatements", ""), 404, "carrier-not-found")
	wantProblem(t, e.do(req{method: http.MethodPost, path: "/carriers/ACMF/suspensions", body: `{"reason":"x"}`, headers: map[string]string{"Idempotency-Key": ""}}), 400, "idempotency-key-required")
}

func TestSuspensionIsIdempotentByKey(t *testing.T) {
	e := newEnv(t)
	e.register("ACMF", acmfBody)
	send := func() *httptest.ResponseRecorder {
		return e.do(req{method: http.MethodPost, path: "/carriers/ACMF/suspensions", body: `{"reason":"insurance lapsed"}`, headers: map[string]string{"Idempotency-Key": "susp-1"}})
	}
	first, second := send(), send()
	wantStatus(t, first, http.StatusOK)
	if second.Header().Get("Idempotency-Replayed") != "true" || second.Body.String() != first.Body.String() {
		t.Fatalf("the retry replays the first answer: %q vs %q (%v)", second.Body.String(), first.Body.String(), second.Header())
	}
	if n := len(e.store.Published()); n != 2 {
		t.Fatalf("registered + suspended once, got %d events", n)
	}
}

func TestBookingIsRefusedForASuspendedCarrierOverRest(t *testing.T) {
	e := newEnv(t)
	// not in the registry: accepted as before
	wantStatus(t, e.post("/appointments", inboundBody("D-BOTH", at8, at10, "ASN-1")), http.StatusCreated)
	e.register("ACMF", acmfBody)
	wantStatus(t, e.post("/carriers/ACMF/suspensions", `{"reason":"insurance lapsed"}`), http.StatusOK)
	p := wantProblem(t, e.post("/appointments", inboundBody("D-BOTH", at10, at12, "ASN-2")), 409, "carrier-suspended")
	if d := p["detail"].(string); !strings.Contains(d, "ACMF") || !strings.Contains(d, "insurance lapsed") {
		t.Fatalf("the problem names the carrier and the reason: %s", d)
	}
	// an outbound booking that names no SCAC is untouched by the registry
	wantStatus(t, e.post("/appointments", outboundBody("D-BOTH", at12, at12.Add(2*time.Hour))), http.StatusCreated)
	wantStatus(t, e.post("/carriers/ACMF/reinstatements", ""), http.StatusOK)
	wantStatus(t, e.post("/appointments", inboundBody("D-BOTH", at10, at12, "ASN-2")), http.StatusCreated)
}

func TestCarrierRoutesRejectOtherMethods(t *testing.T) {
	e := newEnv(t)
	wantProblem(t, e.do(req{method: http.MethodDelete, path: "/carriers/ACMF"}), 405, "method-not-allowed")
	wantProblem(t, e.do(req{method: http.MethodPut, path: "/carriers"}), 405, "method-not-allowed")
	wantProblem(t, e.get("/carriers/ACMF/suspensions"), 405, "method-not-allowed")
}
