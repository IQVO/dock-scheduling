package standards_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/claudioed/dock-scheduling/internal/domain/shared"
	"github.com/claudioed/dock-scheduling/internal/domain/standards"
)

// domainEventNames reads every `EventName() string` method of the domain
// packages and returns the string each one returns: the published event types
// as the code declares them. A new event type added to the domain shows up here
// without anyone listing it.
func domainEventNames(t *testing.T) []string {
	t.Helper()
	var names []string
	err := filepath.WalkDir("..", func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		file, perr := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if perr != nil {
			return perr
		}
		for _, decl := range file.Decls {
			if name, ok := eventNameOf(decl); ok {
				names = append(names, name)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(names)
	return names
}

// eventNameOf returns the literal an `EventName() string { return "X" }` method returns.
func eventNameOf(decl ast.Decl) (string, bool) {
	fn, ok := decl.(*ast.FuncDecl)
	if !ok || fn.Recv == nil || fn.Name.Name != "EventName" || fn.Body == nil || len(fn.Body.List) != 1 {
		return "", false
	}
	ret, ok := fn.Body.List[0].(*ast.ReturnStmt)
	if !ok || len(ret.Results) != 1 {
		return "", false
	}
	lit, ok := ret.Results[0].(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	name, err := strconv.Unquote(lit.Value)
	return name, err == nil
}

// TestEveryPublishedEventHasADecision is the build-failing guard: a domain event
// type with no row in the mapping table (or a row for a type that no longer
// exists) fails here. Adding an event means deciding what it maps to, even if
// the decision is "nothing" with a reason.
func TestEveryPublishedEventHasADecision(t *testing.T) {
	declared := domainEventNames(t)
	if len(declared) != 14 {
		t.Fatalf("the domain declares %d event types %v; the detector or the catalogue changed, update this count deliberately", len(declared), declared)
	}
	decided := append([]string(nil), standards.Events()...)
	sort.Strings(decided)
	if strings.Join(declared, ",") != strings.Join(decided, ",") {
		t.Fatalf("the mapping table and the domain events disagree\n domain events: %v\n table events:  %v\nDecide every event type in internal/domain/standards (ADR 0013).", declared, decided)
	}
	for _, name := range declared {
		if _, known := standards.Of(name, ""); !known {
			t.Errorf("Of(%q) is not known", name)
		}
	}
}

func TestTheTableRowsAreWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for _, d := range standards.Decisions() {
		key := d.Event + "/" + string(d.Direction)
		if seen[key] {
			t.Errorf("duplicate decision %s", key)
		}
		seen[key] = true
		for _, problem := range rowProblems(d) {
			t.Errorf("%s: %s", key, problem)
		}
	}
}

// rowProblems lists what is wrong with one row of the table.
func rowProblems(d standards.Decision) []string {
	var out []string
	if d.Direction != "" && d.Direction != shared.Inbound && d.Direction != shared.Outbound {
		out = append(out, "direction "+string(d.Direction)+" is neither INBOUND, OUTBOUND nor empty")
	}
	mapped := d.X12Status != "" && d.CBVBizStep != ""
	if mapped && d.Reason != "" {
		out = append(out, "both fields are mapped, so there is no reason to give: "+d.Reason)
	}
	if !mapped && d.Reason == "" {
		out = append(out, "a field is not mapped and no reason says why")
	}
	if d.CBVBizStep != "" && !strings.HasPrefix(d.CBVBizStep, "urn:epcglobal:cbv:bizstep:") {
		out = append(out, d.CBVBizStep+" is not a CBV business step URN (one form only)")
	}
	if len(d.X12Status) != 0 && len(d.X12Status) != 2 {
		out = append(out, "X12 214 status codes are two characters, got "+d.X12Status)
	}
	return out
}

// A direction-dependent event decides BOTH directions, and an event is either
// direction-dependent or not, never both.
func TestEachEventDecidesBothDirectionsOrNeither(t *testing.T) {
	for _, name := range standards.Events() {
		in, out, free := 0, 0, 0
		for _, d := range standards.Decisions() {
			if d.Event != name {
				continue
			}
			switch d.Direction {
			case shared.Inbound:
				in++
			case shared.Outbound:
				out++
			default:
				free++
			}
		}
		if (free == 1 && in+out != 0) || (free == 0 && (in != 1 || out != 1)) || free > 1 {
			t.Errorf("%s: %d direction-free, %d inbound, %d outbound rows", name, free, in, out)
		}
	}
}

// TestTheMapping pins every decision: the code and, for a withheld mapping, the
// fact that nothing is published. This is the table of ADR 0013.
func TestTheMapping(t *testing.T) {
	const arriving = "urn:epcglobal:cbv:bizstep:arriving"
	type want struct{ x12, cbv string }
	cases := []struct {
		event string
		in    want
		out   want
	}{
		{"DockAppointmentBooked", want{"AB", ""}, want{"AA", ""}},
		{"DockAppointmentRescheduled", want{"AB", ""}, want{"AA", ""}},
		{"DockAppointmentCancelled", want{}, want{}},
		{"DockAppointmentNoShow", want{}, want{}},
		{"DockAppointmentCheckedIn", want{"X1", arriving}, want{"X3", arriving}},
		{"DockAppointmentDoorReassigned", want{}, want{}},
		{"DockAppointmentCompleted", want{}, want{}},
		{"DockAppointmentAtRisk", want{}, want{}},
		{"DoorClosureScheduled", want{}, want{}},
		{"DoorClosureLifted", want{}, want{}},
		{"DoorOperatingHoursDeclared", want{}, want{}},
		{"CarrierRegistered", want{}, want{}},
		{"CarrierSuspended", want{}, want{}},
		{"CarrierReinstated", want{}, want{}},
	}
	if len(cases) != len(standards.Events()) {
		t.Fatalf("%d cases for %d decided events", len(cases), len(standards.Events()))
	}
	for _, c := range cases {
		for dir, w := range map[shared.Direction]want{shared.Inbound: c.in, shared.Outbound: c.out} {
			got, known := standards.Of(c.event, dir)
			if !known || got.X12Status != w.x12 || got.CBVBizStep != w.cbv {
				t.Errorf("Of(%s, %s) = %+v known=%v, want x12 %q cbv %q", c.event, dir, got, known, w.x12, w.cbv)
			}
		}
	}
}

func TestOfAnUnknownEventOrDirection(t *testing.T) {
	if s, known := standards.Of("DockAppointmentTeleported", shared.Inbound); known || s != (standards.Semantics{}) {
		t.Errorf("an undecided event must be unknown and map to nothing, got %+v known=%v", s, known)
	}
	// A direction-dependent event asked with no (or a wrong) direction is known
	// but maps to nothing: the table never guesses a direction.
	for _, dir := range []shared.Direction{"", "SIDEWAYS"} {
		if s, known := standards.Of("DockAppointmentBooked", dir); !known || s != (standards.Semantics{}) {
			t.Errorf("Of(Booked, %q) = %+v known=%v, want known and empty", dir, s, known)
		}
	}
	// A direction-free event answers for any direction.
	if s, known := standards.Of("DockAppointmentCancelled", shared.Outbound); !known || s != (standards.Semantics{}) {
		t.Errorf("Of(Cancelled) = %+v known=%v", s, known)
	}
}

func TestDecisionsReturnsACopy(t *testing.T) {
	first := standards.Decisions()
	first[0].X12Status = "ZZ"
	if standards.Decisions()[0].X12Status == "ZZ" {
		t.Fatal("Decisions exposes the table itself: a caller could rewrite the mapping")
	}
	if got := standards.Events(); len(got) != 14 || got[0] != "DockAppointmentBooked" || got[len(got)-1] != "CarrierReinstated" {
		t.Fatalf("Events() = %v, want 14 names in table order", got)
	}
}

// The official names of the codes, as the code lists publish them, so a typo in
// a constant (or a silent swap of AA and AB) fails.
func TestTheCodesAreTheOnesTheListsDefine(t *testing.T) {
	for code, name := range map[string]string{
		standards.X12PickUpAppointment:   "AA Pick-up Appointment Date and/or Time",
		standards.X12DeliveryAppointment: "AB Delivery Appointment Date and/or Time",
		standards.X12ArrivedAtPickUp:     "X3 Arrived at Pick-up Location",
		standards.X12ArrivedAtDelivery:   "X1 Arrived at Delivery Location",
	} {
		if !strings.HasPrefix(name, code+" ") {
			t.Errorf("constant %q does not start the official name %q", code, name)
		}
	}
	if standards.CBVArriving != "urn:epcglobal:cbv:bizstep:arriving" {
		t.Errorf("CBVArriving = %q", standards.CBVArriving)
	}
}
