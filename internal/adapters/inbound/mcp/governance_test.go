package mcp_test

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// update regenerates testdata/tool_registry.golden and .golden.json:
//
//	go test ./internal/adapters/inbound/mcp -run TestToolRegistryGolden -update
var update = flag.Bool("update", false, "rewrite the MCP tool registry golden files")

const authorityKey = "com.warehouse/authority"

// wantLevels is the curated tool set of decision D-6 with its authority level.
// Reads are L1. The writes are L3: the fleet rule (and the template's
// TestMCPWriteToolsAreAtLeastL3) reserves L1/L2 for read tools, so the D-6
// wording "L2" for the writes cannot hold (docs/adr/0007).
var wantLevels = map[string]string{
	"list_appointments": "L1", "get_appointment": "L1", "get_door_availability": "L1", "get_dock_calendar": "L1", "list_at_risk_appointments": "L1",
	"book_appointment": "L3", "reschedule_appointment": "L3", "cancel_appointment": "L3", "report_eta": "L3",
}

// writeTools are the only tools allowed to change state; each is a verb of the
// REST contract that carrier schedulers and dock planners already use.
var writeTools = []string{"book_appointment", "cancel_appointment", "report_eta", "reschedule_appointment"}

// maxTools is the tool budget (the charter's eight plus the ninth D-6 names).
// Raising it is a deliberate, reviewed act: an ADR 0007 amendment.
const maxTools = 9

// outOfScopeWords must never appear in a tool name: arrival, check-in and door
// release are yard facts applied from Kafka (decision: "physical facts owned by
// yard-management"), and hours, settings, closures and the no-show sweep are
// planner/cron operations that stay on REST.
var outOfScopeWords = []string{"arrival", "arrive", "checkin", "release", "complete", "noshow", "sweep", "closure", "hours", "settings", "reassign"}

var (
	toolNaming = regexp.MustCompile(`^[a-z]+(_[a-z]+)+$`)
	argNaming  = regexp.MustCompile(`^[a-z][a-zA-Z0-9]*$`)
)

func listTools(t *testing.T, h *harness) []*sdk.Tool {
	t.Helper()
	res, err := h.session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	sort.Slice(res.Tools, func(i, j int) bool { return res.Tools[i].Name < res.Tools[j].Name })
	return res.Tools
}

func TestToolSurface(t *testing.T) {
	tools := listTools(t, newHarness(t))
	if len(tools) > maxTools {
		t.Fatalf("advertised %d tools, over the budget of %d", len(tools), maxTools)
	}
	got := map[string]string{}
	for _, tool := range tools {
		checkToolMetadata(t, tool)
		checkToolArguments(t, tool)
		level, _ := tool.Meta[authorityKey].(string)
		got[tool.Name] = level
		for _, word := range outOfScopeWords {
			if strings.Contains(strings.ReplaceAll(tool.Name, "_", ""), word) {
				t.Errorf("tool %q names %q, which belongs to the yard facts or to REST", tool.Name, word)
			}
		}
	}
	if fmt.Sprint(got) != fmt.Sprint(wantLevels) {
		t.Fatalf("advertised tools and levels %v, want exactly %v", got, wantLevels)
	}
}

func checkToolMetadata(t *testing.T, tool *sdk.Tool) {
	t.Helper()
	if !toolNaming.MatchString(tool.Name) {
		t.Errorf("tool %q is not snake_case", tool.Name)
	}
	if tool.Description == "" {
		t.Errorf("tool %q has no description", tool.Name)
	}
	if tool.Annotations == nil {
		t.Errorf("tool %q has no annotations", tool.Name)
		return
	}
	level, _ := tool.Meta[authorityKey].(string)
	if slices.Contains(writeTools, tool.Name) {
		checkWriteTool(t, tool, level)
	} else {
		checkReadTool(t, tool, level)
	}
}

func checkWriteTool(t *testing.T, tool *sdk.Tool, level string) {
	t.Helper()
	if tool.Annotations.ReadOnlyHint || level != "L3" {
		t.Errorf("write tool %q must be annotated not read-only and declare L3: %+v %q", tool.Name, tool.Annotations, level)
	}
	if !tool.Annotations.IdempotentHint {
		t.Errorf("write tool %q takes an idempotency key, so it is idempotent", tool.Name)
	}
}

func checkReadTool(t *testing.T, tool *sdk.Tool, level string) {
	t.Helper()
	if !tool.Annotations.ReadOnlyHint || level != "L1" {
		t.Errorf("read tool %q must be annotated read-only and declare L1: %+v %q", tool.Name, tool.Annotations, level)
	}
	if tool.Annotations.DestructiveHint != nil && *tool.Annotations.DestructiveHint {
		t.Errorf("read tool %q is annotated destructive", tool.Name)
	}
}

func checkToolArguments(t *testing.T, tool *sdk.Tool) {
	t.Helper()
	schema, isMap := tool.InputSchema.(map[string]any)
	if !isMap {
		t.Errorf("tool %q input schema = %T", tool.Name, tool.InputSchema)
		return
	}
	props, _ := schema["properties"].(map[string]any)
	if len(props) == 0 {
		t.Errorf("tool %q has no arguments in its schema", tool.Name)
	}
	if schema["additionalProperties"] != false {
		t.Errorf("tool %q accepts unknown arguments (additionalProperties must be false)", tool.Name)
	}
	for arg, p := range props {
		if !argNaming.MatchString(arg) {
			t.Errorf("tool %q argument %q is not camelCase", tool.Name, arg)
		}
		if d, _ := p.(map[string]any)["description"].(string); d == "" {
			t.Errorf("tool %q argument %q has no description", tool.Name, arg)
		}
	}
}

// The five reads and four writes of decision D-6, none more: this is the test
// that fails when a tenth tool, or a state change on a read tool, appears.
func TestWriteToolsAreExactlyTheBookingVerbs(t *testing.T) {
	var writes []string
	for _, tool := range listTools(t, newHarness(t)) {
		if tool.Annotations != nil && !tool.Annotations.ReadOnlyHint {
			writes = append(writes, tool.Name)
		}
	}
	if strings.Join(writes, ",") != strings.Join(writeTools, ",") {
		t.Fatalf("write tools = %v, want exactly %v", writes, writeTools)
	}
}

// Every write needs the idempotency key and the attribution labels are optional
// (the template's optional-input rule, checked here on the wire schema too).
func TestWriteToolsRequireTheIdempotencyKeyAndOfferAttribution(t *testing.T) {
	for _, tool := range listTools(t, newHarness(t)) {
		schema := tool.InputSchema.(map[string]any)
		props := schema["properties"].(map[string]any)
		required := map[string]bool{}
		if r, ok := schema["required"].([]any); ok {
			for _, name := range r {
				required[name.(string)] = true
			}
		}
		isWrite := tool.Annotations != nil && !tool.Annotations.ReadOnlyHint
		for _, name := range []string{"idempotencyKey", "proposalId", "approvedBy", "agentId"} {
			_, has := props[name]
			if has != isWrite {
				t.Errorf("tool %s: has %s = %v, want %v", tool.Name, name, has, isWrite)
			}
		}
		if isWrite && !required["idempotencyKey"] {
			t.Errorf("tool %s: idempotencyKey must be required", tool.Name)
		}
		for _, name := range []string{"proposalId", "approvedBy", "agentId"} {
			if required[name] {
				t.Errorf("tool %s: %s must be optional", tool.Name, name)
			}
		}
	}
}

// TestToolRegistryGolden pins the whole advertised registry byte for byte (names,
// descriptions, annotations, authority, input AND output schemas), so any change
// to what an MCP client sees shows up as a reviewed golden diff. The TSV beside
// it (name, readOnly, destructive, authority) is what the fleet catalogue reads.
func TestToolRegistryGolden(t *testing.T) {
	tools := listTools(t, newHarness(t))
	full, err := json.MarshalIndent(tools, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	checkGolden(t, "tool_registry.golden.json", append(full, '\n'))

	var tsv strings.Builder
	for _, tool := range tools {
		destructive := !tool.Annotations.ReadOnlyHint && (tool.Annotations.DestructiveHint == nil || *tool.Annotations.DestructiveHint)
		fmt.Fprintf(&tsv, "%s\t%t\t%t\t%s\n", tool.Name, tool.Annotations.ReadOnlyHint, destructive, tool.Meta[authorityKey])
	}
	checkGolden(t, "tool_registry.golden", []byte(tsv.String()))
}

func checkGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden (run with -update to create it): %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("the MCP tool registry drifted from %s; review the change and re-run with -update.\ngot:\n%s", path, got)
	}
}
