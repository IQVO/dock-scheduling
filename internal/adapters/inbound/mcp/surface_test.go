// Agentic Standard v2 (AS-3): the MCP surface is complete: tools plus resources
// and prompts. These drive the server through the SDK's in-memory transport over
// the seeded in-memory store, the same harness the tool tests use.
package mcp_test

import (
	"encoding/json"
	"regexp"
	"sort"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/claudioed/dock-scheduling/internal/application/usecases"
)

func TestSurface_ResourceTemplatesAreScopedReadModels(t *testing.T) {
	h := newHarness(t)
	res, err := h.session.ListResourceTemplates(t.Context(), nil)
	if err != nil {
		t.Fatalf("list resource templates: %v", err)
	}
	var got []string
	for _, tmpl := range res.ResourceTemplates {
		got = append(got, tmpl.URITemplate)
		if tmpl.MIMEType != "application/json" || tmpl.Description == "" {
			t.Errorf("template %s: mime %q, description %q", tmpl.URITemplate, tmpl.MIMEType, tmpl.Description)
		}
	}
	sort.Strings(got)
	want := []string{"dock://appointments/{appointmentId}", "dock://sites/{siteId}/at-risk", "dock://sites/{siteId}/calendar"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("resource templates = %v, want %v", got, want)
	}
}

func readResource(t *testing.T, h *harness, uri string, out any) {
	t.Helper()
	res, err := h.session.ReadResource(t.Context(), &sdk.ReadResourceParams{URI: uri})
	if err != nil {
		t.Fatalf("read %s: %v", uri, err)
	}
	if len(res.Contents) != 1 || res.Contents[0].URI != uri || res.Contents[0].MIMEType != "application/json" {
		t.Fatalf("contents = %+v", res.Contents)
	}
	if err := json.Unmarshal([]byte(res.Contents[0].Text), out); err != nil {
		t.Fatalf("resource body is not JSON: %v", err)
	}
}

func TestSurface_ResourcesReturnWhatTheToolsReturn(t *testing.T) {
	h := newHarness(t)
	id := h.seedBooking(usecases.BookCommand{DoorCode: "D-IN", Direction: "INBOUND", Carrier: "ACME Freight", WindowStart: at8, WindowEnd: at9, ASNNumbers: []string{"ASN-1"}})

	var fromResource, fromTool apptOut
	readResource(t, h, "dock://appointments/"+id, &fromResource)
	h.ok("get_appointment", map[string]any{"appointmentId": id}, &fromTool)
	if fromResource.AppointmentID != id || fromResource.State != "BOOKED" || fromResource.Version != fromTool.Version || fromResource.DoorCode != fromTool.DoorCode {
		t.Fatalf("resource %+v vs tool %+v", fromResource, fromTool)
	}

	var cal struct {
		SiteID   string `json:"siteId"`
		Settings struct {
			MaxWindowMinutes int `json:"maxWindowMinutes"`
		} `json:"settings"`
	}
	readResource(t, h, "dock://sites/WH1/calendar", &cal)
	if cal.SiteID != "WH1" || cal.Settings.MaxWindowMinutes != 240 {
		t.Fatalf("calendar = %+v", cal)
	}

	var risk pageOut
	readResource(t, h, "dock://sites/WH1/at-risk", &risk)
	if risk.Items == nil || len(risk.Items) != 0 {
		t.Fatalf("at-risk = %+v, want an empty list", risk)
	}
}

func TestSurface_ResourcesRejectMalformedURIsAndUnknownIds(t *testing.T) {
	h := newHarness(t)
	for _, uri := range []string{
		"dock://appointments/", "dock://appointments/a/b", "dock://sites//calendar", "dock://sites/WH1", "dock://sites/WH1/calendar/extra",
		"dock://appointments/not-an-id", "dock://appointments/" + apptN(42), "dock://sites/a b/calendar",
	} {
		if _, err := h.session.ReadResource(t.Context(), &sdk.ReadResourceParams{URI: uri}); err == nil {
			t.Errorf("reading %q succeeded, want an error", uri)
		}
	}
}

func TestSurface_PromptsCarryTheProcedures(t *testing.T) {
	h := newHarness(t)
	list, err := h.session.ListPrompts(t.Context(), nil)
	if err != nil {
		t.Fatalf("list prompts: %v", err)
	}
	var names []string
	for _, p := range list.Prompts {
		names = append(names, p.Name)
	}
	sort.Strings(names)
	if strings.Join(names, ",") != "plan_dock_booking,triage_at_risk_appointments" {
		t.Fatalf("prompts = %v", names)
	}
	wantTools := map[string][]string{
		"plan_dock_booking":           {"get_dock_calendar", "get_door_availability", "book_appointment", "reschedule_appointment", "idempotencyKey", "agentId", "approvedBy", "door-double-booked"},
		"triage_at_risk_appointments": {"list_at_risk_appointments", "get_door_availability", "reschedule_appointment", "cancel_appointment", "idempotencyKey", "LATE_ETA", "DOOR_REASSIGNED_OVERLAP"},
	}
	for name, needles := range wantTools {
		res, err := h.session.GetPrompt(t.Context(), &sdk.GetPromptParams{Name: name})
		if err != nil || len(res.Messages) != 1 {
			t.Fatalf("get prompt %s: %v, %+v", name, err, res)
		}
		body := res.Messages[0].Content.(*sdk.TextContent).Text
		for _, needle := range needles {
			if !strings.Contains(body, needle) {
				t.Errorf("prompt %s does not mention %q", name, needle)
			}
		}
	}
}

// toolWordRE finds the snake_case words that look like a tool of this surface.
var toolWordRE = regexp.MustCompile(`\b(?:get|list|book|reschedule|cancel|report)_[a-z_]+\b`)

// The prompts only name tools that exist and never send the model to a tool
// the surface does not offer.
func TestSurface_PromptToolNamesExist(t *testing.T) {
	h := newHarness(t)
	known := map[string]bool{}
	for _, tool := range listTools(t, h) {
		known[tool.Name] = true
	}
	for _, name := range []string{"plan_dock_booking", "triage_at_risk_appointments"} {
		res, err := h.session.GetPrompt(t.Context(), &sdk.GetPromptParams{Name: name})
		if err != nil {
			t.Fatal(err)
		}
		words := toolWordRE.FindAllString(res.Messages[0].Content.(*sdk.TextContent).Text, -1)
		if len(words) == 0 {
			t.Fatalf("prompt %s names no tool: the check is vacuous", name)
		}
		for _, word := range words {
			if !known[word] {
				t.Errorf("prompt %s names %q, which is not a tool", name, word)
			}
		}
	}
}

func TestSurface_InstructionsNameTheTools(t *testing.T) {
	h := newHarness(t)
	init := h.session.InitializeResult()
	if init == nil || init.ServerInfo.Name != "dock-scheduling-mcp" {
		t.Fatalf("initialize result = %+v", init)
	}
	for tool := range wantLevels {
		if !strings.Contains(init.Instructions, tool) {
			t.Errorf("instructions do not mention %s", tool)
		}
	}
	if init.Capabilities.Tools == nil || init.Capabilities.Resources == nil || init.Capabilities.Prompts == nil {
		t.Errorf("capabilities = %+v, want tools, resources and prompts", init.Capabilities)
	}
}
