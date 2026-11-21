package mcp

// replayClass is what the idempotency guard of the write tools does with the
// refusal of a problem type (docs/adr/0014): the classes of the REST
// adapter, with the same class for the same slug (errors_test.go pins the
// vocabulary and replay_class_internal_test.go holds this catalogue to the
// ADR table). The OUTCOME class is not here: it is for a command that
// commits state and then refuses, and no write tool can answer one — the
// tool wrapper runs inside ONE joined unit of work and a client failure
// always rolls it back, so nothing commits (usecases.Idempotency.Do). The
// day a tool use case commits before it refuses, the class is added here and
// the wrapper keeps the work for it.
type replayClass uint8

const (
	// replayUnset is the zero value: no class. No catalogue entry may have it.
	replayUnset replayClass = iota
	// replayFinal: the answer cannot change for this call; the refusal is
	// stored under the key and replayed, the tool's writes roll back.
	replayFinal
	// replayRetryable: nothing was committed and the answer depends on state
	// that can change. Nothing is stored and the key is free again: the same
	// call under the same key runs again.
	replayRetryable
)

func (c replayClass) String() string {
	switch c {
	case replayFinal:
		return "FINAL"
	case replayRetryable:
		return "RETRYABLE"
	}
	return "UNSET"
}
