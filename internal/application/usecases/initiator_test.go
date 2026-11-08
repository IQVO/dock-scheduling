package usecases_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/claudioed/dock-scheduling/internal/application/usecases"
)

func TestNewInitiator(t *testing.T) {
	i, err := usecases.NewInitiator(usecases.ViaMCP, " prop-1 ", "", "agent-9")
	if err != nil {
		t.Fatal(err)
	}
	if i.IsZero() || i.Via() != usecases.ViaMCP || i.ProposalID() != "prop-1" || i.ApprovedBy() != "" || i.AgentID() != "agent-9" {
		t.Fatalf("initiator = %+v", i)
	}
	if !(usecases.Initiator{}).IsZero() {
		t.Error("the zero Initiator must be zero")
	}
	// The surface alone is an attribution (an unlabelled agent write).
	bare, err := usecases.NewInitiator(usecases.ViaA2A, "", "", "")
	if err != nil || bare.IsZero() || bare.Via() != usecases.ViaA2A {
		t.Fatalf("bare = %+v, %v", bare, err)
	}
}

func TestNewInitiatorRefusals(t *testing.T) {
	long := strings.Repeat("é", usecases.MaxInitiatorFieldLen+1) // counted in characters, not bytes
	for name, tc := range map[string]struct {
		via              usecases.Via
		prop, appr, agnt string
	}{
		"rest is not an agent surface": {"rest", "", "", ""},
		"empty via":                    {"", "", "", ""},
		"proposal too long":            {usecases.ViaMCP, long, "", ""},
		"approver too long":            {usecases.ViaMCP, "", long, ""},
		"agent too long":               {usecases.ViaMCP, "", "", long},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := usecases.NewInitiator(tc.via, tc.prop, tc.appr, tc.agnt); !errors.Is(err, usecases.ErrInvalidInitiator) {
				t.Fatalf("err = %v, want ErrInvalidInitiator", err)
			}
		})
	}
	edge := strings.Repeat("é", usecases.MaxInitiatorFieldLen)
	if _, err := usecases.NewInitiator(usecases.ViaMCP, edge, edge, edge); err != nil {
		t.Fatalf("a label of exactly %d characters is accepted: %v", usecases.MaxInitiatorFieldLen, err)
	}
}

func TestInitiatorTravelsInTheContext(t *testing.T) {
	if got := usecases.InitiatorFrom(context.Background()); !got.IsZero() {
		t.Fatalf("a bare context carries %+v, want the zero Initiator", got)
	}
	i, _ := usecases.NewInitiator(usecases.ViaMCP, "p", "a", "g")
	if got := usecases.InitiatorFrom(usecases.WithInitiator(context.Background(), i)); got != i {
		t.Fatalf("round trip = %+v, want %+v", got, i)
	}
}
