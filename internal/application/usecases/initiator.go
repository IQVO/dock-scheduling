package usecases

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// Via names the agent surface a state change arrived through (Agentic Standard
// v2, fleet rule agent-authority). REST writes carry no Via at all.
type Via string

// The agent surfaces that may initiate a change.
const (
	ViaMCP Via = "mcp"
	ViaA2A Via = "a2a"
)

// MaxInitiatorFieldLen is the longest accepted proposal id, approver or agent id.
const MaxInitiatorFieldLen = 128

// ErrInvalidInitiator is returned when an Initiator cannot be built: an
// unknown Via, or a caller-supplied label longer than MaxInitiatorFieldLen.
// The tool boundary maps it to the 422 slug `invalid-initiator`.
var ErrInvalidInitiator = errors.New("invalid initiator")

// Initiator records that a state change was initiated by an agent. The adapter
// sets Via itself (the caller cannot claim to be REST); proposal id, approver
// and agent id are free-form attribution labels supplied by the caller. They
// grant nothing and never change what a command accepts. The zero Initiator
// means "not agent-initiated" (REST, policies, sweeps).
type Initiator struct {
	via        Via
	proposalID string
	approvedBy string
	agentID    string
}

// NewInitiator builds the Initiator of a call that arrived through via. The
// three labels are trimmed; an empty label is omitted.
func NewInitiator(via Via, proposalID, approvedBy, agentID string) (Initiator, error) {
	if via != ViaMCP && via != ViaA2A {
		return Initiator{}, fmt.Errorf("%w: via %q is not mcp or a2a", ErrInvalidInitiator, via)
	}
	fields := []struct{ name, value string }{
		{"proposal id", strings.TrimSpace(proposalID)},
		{"approved by", strings.TrimSpace(approvedBy)},
		{"agent id", strings.TrimSpace(agentID)},
	}
	for _, f := range fields {
		if n := utf8.RuneCountInString(f.value); n > MaxInitiatorFieldLen {
			return Initiator{}, fmt.Errorf("%w: %s is %d characters, the maximum is %d", ErrInvalidInitiator, f.name, n, MaxInitiatorFieldLen)
		}
	}
	return Initiator{via: via, proposalID: fields[0].value, approvedBy: fields[1].value, agentID: fields[2].value}, nil
}

// IsZero reports whether the change was not agent-initiated.
func (i Initiator) IsZero() bool { return i == Initiator{} }

// Via is the surface the change arrived through.
func (i Initiator) Via() Via { return i.via }

// ProposalID is the optional id of the proposal this change executes.
func (i Initiator) ProposalID() string { return i.proposalID }

// ApprovedBy is the optional id of the human who approved it.
func (i Initiator) ApprovedBy() string { return i.approvedBy }

// AgentID is the optional id of the calling agent.
func (i Initiator) AgentID() string { return i.agentID }

type initiatorKey struct{}

// WithInitiator returns ctx carrying the initiator of the command run under
// it. An agent-facing adapter sets it before calling a use case; the outbox
// publisher reads it back with InitiatorFrom to stamp `initiated_by` on the
// events of that unit of work (see docs/adr/0007, "Follow-up").
func WithInitiator(ctx context.Context, i Initiator) context.Context {
	return context.WithValue(ctx, initiatorKey{}, i)
}

// InitiatorFrom returns the initiator carried by ctx, or the zero Initiator.
func InitiatorFrom(ctx context.Context) Initiator {
	i, _ := ctx.Value(initiatorKey{}).(Initiator)
	return i
}
