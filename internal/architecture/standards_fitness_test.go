package architecture

// Standard-vocabulary fitness tests (ADR 0013). The mapping table in
// internal/domain/standards is the ONE place that decides which X12 214 code and
// which CBV business step a published fact maps to. These tests tie it to the two
// documents partners read, so none of the three can drift:
//
//   - every event type apis/asyncapi.yaml publishes has a decision in the table
//     (and the table decides nothing the contract does not publish);
//   - the contract declares the three optional standard fields on exactly the data
//     schemas whose facts can carry them, never as required fields;
//   - ADR 0013 lists every event, every code and the CBV business step.
//
// The domain test (standards_test.go) ties the table to the Go event types.

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/claudioed/dock-scheduling/internal/domain/standards"
)

const ownTypePrefix = "com.warehouse.wms.dock-scheduling."

var standardFieldNames = []string{"x12_status", "cbv_biz_step", "read_point_gln"}

// asyncAPI is the part of apis/asyncapi.yaml these tests read.
type asyncAPI struct {
	Components struct {
		Messages map[string]struct {
			Name string `yaml:"name"`
		} `yaml:"messages"`
		Schemas map[string]struct {
			Required   []string       `yaml:"required"`
			Properties map[string]any `yaml:"properties"`
		} `yaml:"schemas"`
	} `yaml:"components"`
}

func loadAsyncAPI(t *testing.T) (asyncAPI, string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "apis", "asyncapi.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var doc asyncAPI
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("apis/asyncapi.yaml: %v", err)
	}
	return doc, string(raw)
}

// publishedEvents returns the event names of the types this service publishes,
// read from the full type strings the contract writes out.
func publishedEvents(contract string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range cloudEventTypeRx.FindAllString(contract, -1) {
		if !strings.HasPrefix(m, ownTypePrefix) {
			continue
		}
		name := m[strings.LastIndex(m, ".")+1:]
		if !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

func TestEveryPublishedTypeOfTheContractHasAStandardsDecision(t *testing.T) {
	_, contract := loadAsyncAPI(t)
	published := publishedEvents(contract)
	decided := append([]string(nil), standards.Events()...)
	sort.Strings(decided)
	if len(published) == 0 {
		t.Fatal("no published type found in apis/asyncapi.yaml: the detector is broken")
	}
	if strings.Join(published, ",") != strings.Join(decided, ",") {
		t.Fatalf("%s", archViolation("contents",
			"every event type apis/asyncapi.yaml publishes has a row in internal/domain/standards, and the table decides nothing else",
			"contract publishes "+strings.Join(published, ", ")+"; the table decides "+strings.Join(decided, ", ")+
				". FIX: decide the new type in internal/domain/standards (a code, or NOT mapped with the reason) and in ADR 0013"))
	}
}

// standardFieldHolders names which data schemas declare which standard fields: an
// appointment fact can carry all three, a door closure only its read point, and
// the operating-hours declaration and the carrier facts none.
func expectedStandardFields(schema string) []string {
	switch {
	case strings.HasPrefix(schema, "DockAppointment") && strings.HasSuffix(schema, "Data"):
		return standardFieldNames
	case strings.HasPrefix(schema, "DoorClosure") && strings.HasSuffix(schema, "Data"):
		return []string{"read_point_gln"}
	}
	return nil
}

func TestTheContractDeclaresTheStandardFieldsOnTheRightSchemasAsOptional(t *testing.T) {
	doc, _ := loadAsyncAPI(t)
	checked := 0
	for name, schema := range doc.Components.Schemas {
		if !strings.HasSuffix(name, "Data") || strings.HasPrefix(name, "Consumed") {
			continue
		}
		want := map[string]bool{}
		for _, f := range expectedStandardFields(name) {
			want[f] = true
		}
		for _, f := range standardFieldNames {
			_, declared := schema.Properties[f]
			if declared != want[f] {
				t.Errorf("%s: %s declared = %v, want %v (appointment facts carry all three, door closures only read_point_gln, nothing else any)", name, f, declared, want[f])
			}
			for _, r := range schema.Required {
				if r == f {
					t.Errorf("%s: %s must be optional (omitted when absent), not required", name, f)
				}
			}
		}
		checked++
	}
	if checked != 14 { // eight appointment, three calendar and three carrier data schemas
		t.Fatalf("%d data schemas were checked, want 14: the detector is broken", checked)
	}
}

func TestTheConsumedSlotSchemaDeclaresTheOptionalGLN(t *testing.T) {
	doc, _ := loadAsyncAPI(t)
	slot, ok := doc.Components.Schemas["ConsumedLocationSlotRegisteredData"]
	if !ok {
		t.Fatal("ConsumedLocationSlotRegisteredData is missing")
	}
	if _, ok := slot.Properties["gln"]; !ok {
		t.Error("the consumed LocationSlotRegistered schema must declare the optional gln (facility-layout ADR 0045)")
	}
	for _, r := range slot.Required {
		if r == "gln" {
			t.Error("gln is optional on the consumed LocationSlotRegistered")
		}
	}
}

func TestADR0013ListsTheMapping(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "adr", "0013-standard-vocabulary-on-published-facts.md"))
	if err != nil {
		t.Fatal(err)
	}
	adr := string(raw)
	for _, d := range standards.Decisions() {
		if !strings.Contains(adr, d.Event) {
			t.Errorf("ADR 0013 does not name the event %s", d.Event)
		}
		for _, code := range []string{d.X12Status, d.CBVBizStep} {
			if code != "" && !strings.Contains(adr, "`"+code+"`") {
				t.Errorf("ADR 0013 does not list the code `%s` of %s %s", code, d.Event, d.Direction)
			}
		}
	}
	for _, source := range []string{"stedi.com/edi/x12-004010/element/1652", "stedi.com/edi/x12-004010/element/1650", "ref.gs1.org/cbv"} {
		if !strings.Contains(adr, source) {
			t.Errorf("ADR 0013 does not cite its source %s", source)
		}
	}
}
