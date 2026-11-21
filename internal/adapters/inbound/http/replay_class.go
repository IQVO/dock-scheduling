package http

// replayClass is what the Idempotency-Key middleware does with the answer of
// a problem type (docs/adr/0014). It is declared on the entry of the problem
// catalogue (errors.go); the middleware reads it and holds no list of slugs
// and no status range beyond 2xx and 5xx. A catalogue entry without a class
// fails TestEveryCatalogueEntryDeclaresAReplayClass.
type replayClass uint8

const (
	// replayUnset is the zero value: no class. No catalogue entry may have
	// it; an answer the catalogue does not know (an unmapped 5xx, a response
	// a handler wrote without the shared error writer) has it and is
	// released like RETRYABLE, the safe fallback.
	replayUnset replayClass = iota
	// replayFinal: the answer cannot change for this request. The refusal is
	// stored under the key and replayed; the handler's writes roll back.
	replayFinal
	// replayOutcome: the command committed state and then refused, and the
	// refusal must be remembered. The writes commit together with the stored
	// answer, in one transaction (JOINED) or stay committed with the answer
	// stored after them (DETACHED). No catalogue entry uses it today; the
	// class and the mechanism are here so the first real OUTCOME is a
	// catalogue change, not a middleware change (ADR 0014).
	replayOutcome
	// replayRetryable: nothing was committed and the answer depends on state
	// that can change. Nothing is stored and the key is free again: the same
	// request under the same key runs again.
	replayRetryable
)

func (c replayClass) String() string {
	switch c {
	case replayFinal:
		return "FINAL"
	case replayOutcome:
		return "OUTCOME"
	case replayRetryable:
		return "RETRYABLE"
	}
	return "UNSET"
}

// replayMarker is implemented by the middleware's capturing writer:
// writeError tells it the class of the problem it is about to write.
type replayMarker interface{ MarkReplay(replayClass) }
