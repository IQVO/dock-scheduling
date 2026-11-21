package mcp

// The replay class of a problem type (ADR 0014) on the agent write surface:
// the catalogue of errors.go declares one class per entry, the ADR table and
// the catalogue are held equal, and the idempotency guard stores a FINAL
// refusal and releases a RETRYABLE one.

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

const adrPath = "../../../../docs/adr/0014-idempotency-replay-classes.md"

func TestEveryCatalogueEntryDeclaresAReplayClass(t *testing.T) {
	for _, e := range errorCatalogue {
		switch e.replay {
		case replayFinal, replayRetryable:
		default:
			t.Errorf("%s has no replay class: declare FINAL or RETRYABLE on its catalogue entry (ADR 0014)", e.slug)
		}
	}
}

// The MCP catalogue and the ADR say the same for every slug it answers.
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
	for _, e := range errorCatalogue {
		// The three idempotency-key refusals are raised before the store is
		// ever read (ValidateIdempotencyKey, replay) or on a replay: they are
		// OUTSIDE the store on both surfaces (ADR 0014); their entry exists
		// so mapError names the slug, and its class is never consulted.
		if strings.HasPrefix(e.slug, "idempotency-key-") {
			continue
		}
		if got := rows[e.slug]; got != e.replay.String() {
			t.Errorf("`%s`: the ADR says %q, the catalogue %s", e.slug, got, e.replay)
		}
		delete(rows, e.slug)
	}
	// What is left classifies the REST surface (its catalogue and its
	// outside-the-store problems); completeness is the REST test's job.
}

// classify stores a FINAL refusal and releases a RETRYABLE one (ok=false),
// whatever wraps the error. replayUnset is the class of no entry and of an
// untyped error alike: neither is stored.
func TestClassifyStoresFinalAndReleasesRetryable(t *testing.T) {
	finalSlug, retrySlug := "", ""
	for _, e := range errorCatalogue {
		if finalSlug == "" && e.replay == replayFinal {
			finalSlug = e.slug
		}
		if retrySlug == "" && e.replay == replayRetryable {
			retrySlug = e.slug
		}
	}
	if finalSlug == "" || retrySlug == "" {
		t.Fatalf("the catalogue needs a FINAL and a RETRYABLE entry, got %q / %q", finalSlug, retrySlug)
	}
	errFinal := errorOf(finalSlug)
	if slug, _, ok := classify(errFinal); !ok || slug != finalSlug {
		t.Errorf("classify(%s) = %q,%v: a FINAL refusal is stored", finalSlug, slug, ok)
	}
	if errRetry := errorOf(retrySlug); errRetry != nil {
		if _, _, ok := classify(errRetry); ok {
			t.Errorf("classify(%s) stores the refusal: a RETRYABLE one is released", retrySlug)
		}
	}
	if _, _, ok := classify(errUnmapped); ok {
		t.Error("classify stores an untyped error")
	}
	if e := entryOf(retrySlug); e.replay == replayUnset {
		t.Errorf("`%s` has no class", retrySlug)
	}
}

// entryOf returns the first catalogue entry whose slug matches.
func entryOf(slug string) struct {
	target error
	slug   string
	replay replayClass
} {
	for _, e := range errorCatalogue {
		if e.slug == slug {
			return e
		}
	}
	return struct {
		target error
		slug   string
		replay replayClass
	}{}
}

var errUnmapped = &unmappedError{}

type unmappedError struct{}

func (*unmappedError) Error() string { return "not in any catalogue" }

// errorOf returns the typed error of the first catalogue entry whose slug
// matches, wrapped the way a use case returns it.
func errorOf(slug string) error {
	for _, e := range errorCatalogue {
		if e.slug == slug {
			return &wrappedError{target: e.target}
		}
	}
	return nil
}

type wrappedError struct{ target error }

func (w *wrappedError) Error() string { return "while booking: " + w.target.Error() }

func (w *wrappedError) Unwrap() error { return w.target }
