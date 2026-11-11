package postgres

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/claudioed/dock-scheduling/internal/application/usecases"
	"github.com/claudioed/dock-scheduling/internal/domain/carrier"
)

func TestCarrierFactsRenderTheGoldenWireData(t *testing.T) {
	cases := []struct {
		golden string
		event  carrier.Event
	}{
		{"carrier_registered.golden.json", carrier.RegisteredEvent{
			Header: carrier.Header{Scac: "ACMF", Version: 1, At: ts(10, 14, 0, 0)}, Name: "ACME Freight", USDOT: "1234567", MC: "MC654321", Status: carrier.Active,
		}},
		{"carrier_suspended.golden.json", carrier.SuspendedEvent{
			Header: carrier.Header{Scac: "ACMF", Version: 2, At: ts(10, 15, 30, 0)}, Reason: "insurance lapsed", SuspendedAt: ts(10, 15, 30, 0),
		}},
		{"carrier_reinstated.golden.json", carrier.ReinstatedEvent{
			Header: carrier.Header{Scac: "ACMF", Version: 3, At: ts(11, 9, 0, 0)}, ReinstatedAt: ts(11, 9, 0, 0),
		}},
	}
	for _, c := range cases {
		t.Run(c.golden, func(t *testing.T) {
			got, err := carrierFactData(c.event, nil)
			if err != nil {
				t.Fatal(err)
			}
			if g, w := asJSONValue(t, got), goldenData(t, c.golden); !reflect.DeepEqual(g, w) {
				t.Fatalf("wire data differs from the golden\n got: %v\nwant: %v", g, w)
			}
		})
	}
}

// A registration snapshot of a suspended carrier carries the reason and the time;
// the optional identifiers are present and null, never omitted.
func TestCarrierRegisteredSnapshotNullsAndSuspension(t *testing.T) {
	bare, err := carrierFactData(carrier.RegisteredEvent{Header: carrier.Header{Scac: "AB", Version: 1, At: ts(10, 14, 0, 0)}, Name: "Bare", Status: carrier.Active}, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(bare)
	for _, want := range []string{`"usdot":null`, `"mc":null`, `"reason":null`, `"suspended_at":null`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("%s misses %s", raw, want)
		}
	}
	suspended, err := carrierFactData(carrier.RegisteredEvent{
		Header: carrier.Header{Scac: "AB", Version: 3, At: ts(10, 16, 0, 0)}, Name: "Bare", Status: carrier.Suspended, Reason: "r", SuspendedAt: ts(10, 15, 0, 0),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(suspended)
	for _, want := range []string{`"status":"SUSPENDED"`, `"reason":"r"`, `"suspended_at":"2026-11-10T15:00:00Z"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("%s misses %s", raw, want)
		}
	}
}

func TestCarrierFactsCarryInitiatedByOnlyWhenAnAgentMadeTheChange(t *testing.T) {
	if got := initiatedBy(usecases.Initiator{}); got != nil {
		t.Fatalf("a REST write carries no initiated_by: %+v", got)
	}
	by, err := usecases.NewInitiator(usecases.ViaMCP, "prop-1", "ops-lead", "agent-7")
	if err != nil {
		t.Fatal(err)
	}
	ctxBy := initiatedBy(usecases.InitiatorFrom(usecases.WithInitiator(context.Background(), by)))
	for name, e := range map[string]carrier.Event{
		"registered": carrier.RegisteredEvent{Header: carrier.Header{Scac: "AB", Version: 1, At: ts(10, 14, 0, 0)}, Name: "n", Status: carrier.Active},
		"suspended":  carrier.SuspendedEvent{Header: carrier.Header{Scac: "AB", Version: 2, At: ts(10, 14, 0, 0)}, Reason: "r", SuspendedAt: ts(10, 14, 0, 0)},
		"reinstated": carrier.ReinstatedEvent{Header: carrier.Header{Scac: "AB", Version: 3, At: ts(10, 14, 0, 0)}, ReinstatedAt: ts(10, 14, 0, 0)},
	} {
		data, err := carrierFactData(e, ctxBy)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		raw, _ := json.Marshal(data)
		_ = json.Unmarshal(raw, &m)
		want := map[string]any{"via": "mcp", "proposal_id": "prop-1", "approved_by": "ops-lead", "agent_id": "agent-7"}
		if !reflect.DeepEqual(m["initiated_by"], want) {
			t.Errorf("%s: initiated_by = %v, want %v", name, m["initiated_by"], want)
		}
		plain, _ := carrierFactData(e, nil)
		raw, _ = json.Marshal(plain)
		if strings.Contains(string(raw), "initiated_by") {
			t.Errorf("%s: a change that no agent made has no initiated_by: %s", name, raw)
		}
	}
	partial, _ := usecases.NewInitiator(usecases.ViaA2A, "", "", "")
	raw, _ := json.Marshal(initiatedBy(partial))
	if string(raw) != `{"via":"a2a"}` {
		t.Errorf("empty labels are omitted: %s", raw)
	}
}

type unknownCarrierEvent struct{ carrier.Header }

func (unknownCarrierEvent) EventName() string { return "CarrierTeleported" }

func TestAnUnknownCarrierFactHasNoWireShape(t *testing.T) {
	if _, err := carrierFactData(unknownCarrierEvent{}, nil); err == nil {
		t.Error("an unknown carrier fact must be refused")
	}
}
