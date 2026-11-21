package http

// The replay class of a problem type (ADR 0014): FINAL, OUTCOME or RETRYABLE,
// declared on its catalogue entry. These tests are the guard rails of that
// declaration: no entry without a class, every problem the adapter writes is
// classified or listed as outside the store, and the ADR table says what the
// code says.

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/claudioed/dock-scheduling/internal/domain/carrier"
	"github.com/claudioed/dock-scheduling/internal/domain/dockappointment"
)

const adrPath = "../../../../docs/adr/0014-idempotency-replay-classes.md"

// outsideTheStore are the problems the adapter writes that never reach the
// replay decision: the middleware writes them itself (not through the capture
// writer), the router writes them before any key is read, or they are a 5xx.
// They have no class; each one is listed in the ADR with the reason.
var outsideTheStore = map[string]string{
	"idempotency-key-required": "written by the middleware before a key exists",
	"idempotency-key-invalid":  "written by the middleware before a key is used",
	"idempotency-key-reused":   "written on a replay against a stored answer; never itself the stored answer",
	"not-found":                "written by the router and the NUL guard, before the keyed middleware",
	"method-not-allowed":       "written by the router, outside the keyed routes",
	"dependency-unavailable":   "the 503 of /readyz, not a keyed route (and a 5xx is never stored)",
	"internal-error":           "an unmapped error: a 5xx is never stored",
}

// mcpOnlyADRRows are the ADR rows that classify problems only the MCP adapter
// answers (a separate surface with its own catalogue, held to the same table
// by internal/adapters/inbound/mcp).
var mcpOnlyADRRows = map[string]bool{
	"invalid-argument": true, "invalid-initiator": true, "invalid-appointment-id": true,
}

func TestEveryCatalogueEntryDeclaresAReplayClass(t *testing.T) {
	for _, e := range problemCatalogue {
		switch e.replay {
		case replayFinal, replayOutcome, replayRetryable:
		default:
			t.Errorf("%s has no replay class: declare FINAL, OUTCOME or RETRYABLE on its catalogue entry (ADR 0014)", e.p.slug)
		}
	}
}

// Every problem the package can write is either a catalogue entry (which has
// a class, above) or one of outsideTheStore; the exemption list holds nothing
// that is also a catalogue entry or that the adapter no longer writes.
func TestEveryProblemTheAdapterWritesHasAClassOrIsOutsideTheStore(t *testing.T) {
	answerable := answerableProblems(t)
	inCatalogue := map[string]bool{}
	for _, e := range problemCatalogue {
		inCatalogue[e.p.slug] = true
	}
	for _, slug := range sortedSlugs(answerable) {
		if _, outside := outsideTheStore[slug]; !inCatalogue[slug] && !outside {
			t.Errorf("`%s` is written by the adapter and has no replay class: add it to problemCatalogue with a class, or to outsideTheStore with the reason", slug)
		}
	}
	for slug, why := range outsideTheStore {
		if _, written := answerable[slug]; !written {
			t.Errorf("`%s` is no longer written by the adapter: delete it from outsideTheStore (%s)", slug, why)
		}
		if inCatalogue[slug] {
			t.Errorf("`%s` is a catalogue entry and listed outsideTheStore: keep one of the two", slug)
		}
	}
}

// No catalogue entry is OUTCOME today: in the JOINED mode a 4xx always rolled
// back (nothing commits) and the DETACHED sweep's refusals never commit the
// failing appointment's own work. The first real OUTCOME needs a use case
// that provably commits before it refuses, and an update to this test.
func TestNoCatalogueEntryIsOutcomeYet(t *testing.T) {
	for _, e := range problemCatalogue {
		if e.replay == replayOutcome {
			t.Errorf("`%s` is OUTCOME: ADR 0014 says no entry qualifies today — prove the commit or reclassify", e.p.slug)
		}
	}
}

// replayOf reads the class of the first entry an error matches, also when the
// use case wrapped it, and says nothing for an error the catalogue does not know.
func TestReplayOfReadsTheClassOfTheMatchingEntry(t *testing.T) {
	for _, e := range problemCatalogue {
		wrapped := fmt.Errorf("while doing %d things: %w", 3, e.err)
		if got := replayOf(wrapped); got != e.replay {
			t.Errorf("%s wrapped: class %v, want %v", e.p.slug, got, e.replay)
		}
	}
	if got := replayOf(errors.New("boom")); got != replayUnset {
		t.Errorf("an unmapped error has class %v, want none", got)
	}
}

// writeError tells the capturing writer the class of what it is writing.
func TestWriteErrorMarksTheWriterWithTheClassOfTheProblem(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want replayClass
	}{
		{"a suspended carrier, wrapped as the use case returns it", fmt.Errorf("%w: SCAC ACMF is suspended", carrier.ErrCarrierSuspended), replayRetryable},
		{"a lost version race", dockappointment.ErrConcurrentUpdate, replayRetryable},
		{"an illegal transition", dockappointment.ErrIllegalTransition, replayFinal},
		{"a malformed body", errMalformedRequest, replayFinal},
		{"an unmapped error", errors.New("boom"), replayUnset},
	} {
		out := &capture{header: http.Header{}}
		writeError(out, httptest.NewRequest(http.MethodPost, "/appointments", nil), tc.err)
		if out.class != tc.want {
			t.Errorf("%s: class %v, want %v", tc.name, out.class, tc.want)
		}
	}
}

// The ADR table and the catalogue say the same: one row per catalogue slug
// with its class, a row for each problem outside the store, and nothing the
// adapter does not answer (the MCP-only rows are the other surface's).
func TestTheADRDeclaresTheClassOfEveryProblem(t *testing.T) {
	raw, err := os.ReadFile(adrPath)
	if err != nil {
		t.Fatalf("read the ADR: %v", err)
	}
	row := regexp.MustCompile("(?m)^\\| `([a-z0-9-]+)` \\| (FINAL|OUTCOME|RETRYABLE|OUTSIDE) \\|")
	rows := map[string]string{}
	for _, m := range row.FindAllStringSubmatch(string(raw), -1) {
		if prior, twice := rows[m[1]]; twice {
			t.Errorf("the ADR lists `%s` twice (%s, %s)", m[1], prior, m[2])
		}
		rows[m[1]] = m[2]
	}
	// Two entries may share a slug (invalid-cursor has two typed errors): the
	// ADR holds one row for it and both entries must share its class.
	bySlug := map[string]replayClass{}
	for _, e := range problemCatalogue {
		if prior, seen := bySlug[e.p.slug]; seen {
			if prior != e.replay {
				t.Errorf("`%s` is declared %s and %s: one slug, one class", e.p.slug, prior, e.replay)
			}
			continue
		}
		bySlug[e.p.slug] = e.replay
		if got := rows[e.p.slug]; got != e.replay.String() {
			t.Errorf("`%s`: the ADR says %q, the catalogue %s", e.p.slug, got, e.replay)
		}
		delete(rows, e.p.slug)
	}
	for slug := range outsideTheStore {
		if rows[slug] != "OUTSIDE" {
			t.Errorf("`%s`: the ADR says %q, want OUTSIDE (a problem outside the store)", slug, rows[slug])
		}
		delete(rows, slug)
	}
	for slug := range mcpOnlyADRRows {
		if rows[slug] == "" {
			t.Errorf("`%s` is an MCP-only problem the ADR no longer classifies", slug)
		}
		delete(rows, slug)
	}
	left := make([]string, 0, len(rows))
	for slug := range rows {
		left = append(left, slug)
	}
	sort.Strings(left)
	if len(left) > 0 {
		t.Errorf("the ADR classifies problems the adapter does not write: %s", strings.Join(left, ", "))
	}
}

// problemConstructors are the helpers of errors.go that build a problem of a
// fixed status from a slug and a title.
var problemConstructors = map[string]int{
	"badRequest": http.StatusBadRequest, "notFound": http.StatusNotFound,
	"conflict": http.StatusConflict, "unprocessable": http.StatusUnprocessableEntity,
}

// answerableProblems reads every problem this package can write from its own
// sources: each `problem{...}` literal and each call of a problemConstructor.
func answerableProblems(t *testing.T) map[string]bool {
	t.Helper()
	files, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	scan := problemScan{t: t, fset: token.NewFileSet(), found: map[string]bool{}}
	for _, f := range files {
		name := f.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(scan.fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, decl := range file.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && problemConstructors[fn.Name.Name] != 0 {
				continue // the constructor itself builds a problem from its arguments
			}
			ast.Inspect(decl, scan.visit)
		}
	}
	return scan.found
}

// problemScan collects the problems of the sources by slug.
type problemScan struct {
	t     *testing.T
	fset  *token.FileSet
	found map[string]bool
}

func (s *problemScan) visit(n ast.Node) bool {
	switch node := n.(type) {
	case *ast.CallExpr:
		s.visitCall(node)
	case *ast.CompositeLit:
		s.visitLiteral(node)
	}
	return true
}

// visitCall records the slug of a problemConstructor call.
func (s *problemScan) visitCall(node *ast.CallExpr) {
	id, ok := node.Fun.(*ast.Ident)
	if !ok || problemConstructors[id.Name] == 0 || len(node.Args) == 0 {
		return
	}
	if slug, ok := stringLit(node.Args[0]); ok {
		s.found[slug] = true
	}
}

// visitLiteral records the slug of a `problem{...}` literal (positional
// status, slug, title — or keyed fields).
func (s *problemScan) visitLiteral(node *ast.CompositeLit) {
	if id, ok := node.Type.(*ast.Ident); !ok || id.Name != "problem" {
		return
	}
	slug := ""
	if len(node.Elts) >= 2 {
		slug, _ = stringLit(node.Elts[1])
	}
	for _, elt := range node.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		if key, _ := kv.Key.(*ast.Ident); key != nil && key.Name == "slug" {
			slug, _ = stringLit(kv.Value)
		}
	}
	if slug != "" {
		s.found[slug] = true
	}
}

func stringLit(e ast.Expr) (string, bool) {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", false
	}
	return s, true
}

func sortedSlugs(problems map[string]bool) []string {
	out := make([]string, 0, len(problems))
	for slug := range problems {
		out = append(out, slug)
	}
	sort.Strings(out)
	return out
}
