package shared_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/claudioed/dock-scheduling/internal/domain/shared"
)

func TestNewDoorCode(t *testing.T) {
	long := strings.Repeat("d", shared.MaxDoorCodeLength)
	tests := []struct {
		name  string
		input string
		ok    bool
	}{
		{"plain", "WH1-DOCK-IN-01", true},
		{"max length", long, true},
		{"one char", "D", true},
		{"empty", "", false},
		{"too long", long + "d", false},
		{"space", "WH1 DOCK", false},
		{"tab", "WH1\tDOCK", false},
		{"slash", "WH1/DOCK", false},
		{"control", "WH1\x00DOCK", false},
		{"unicode space", "WH1\u00a0DOCK", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := shared.NewDoorCode(tc.input)
			if tc.ok {
				if err != nil || got.String() != tc.input {
					t.Fatalf("NewDoorCode(%q) = %q, %v; want ok", tc.input, got, err)
				}
				return
			}
			if !errors.Is(err, shared.ErrInvalidDoorCode) {
				t.Fatalf("NewDoorCode(%q) error = %v; want ErrInvalidDoorCode", tc.input, err)
			}
		})
	}
}

func TestNewSiteID(t *testing.T) {
	if _, err := shared.NewSiteID("WH1"); err != nil {
		t.Fatalf("valid site: %v", err)
	}
	for _, bad := range []string{"", "a b", "a/b", strings.Repeat("s", shared.MaxSiteIDLength+1)} {
		if _, err := shared.NewSiteID(bad); !errors.Is(err, shared.ErrInvalidSiteID) {
			t.Fatalf("NewSiteID(%q) error = %v; want ErrInvalidSiteID", bad, err)
		}
	}
	if _, err := shared.NewSiteID(strings.Repeat("s", shared.MaxSiteIDLength)); err != nil {
		t.Fatalf("max length site: %v", err)
	}
	if id, _ := shared.NewSiteID("WH1"); id.String() != "WH1" {
		t.Fatalf("String = %q", id.String())
	}
}

func TestNewCarrier(t *testing.T) {
	tests := []struct {
		name, carrier, scac string
		ok                  bool
	}{
		{"name only", "ACME Freight", "", true},
		{"with scac", "ACME Freight", "ACMF", true},
		{"two letter scac", "ACME", "AC", true},
		{"max name", strings.Repeat("c", shared.MaxCarrierLength), "", true},
		{"blank", "   ", "", false},
		{"empty", "", "", false},
		{"too long", strings.Repeat("c", shared.MaxCarrierLength+1), "", false},
		{"control", "ACME\x07", "", false},
		{"control first", "\x07ACME", "", false},
		{"scac lower", "ACME", "acmf", false},
		{"scac short", "ACME", "A", false},
		{"scac long", "ACME", "ACMFX", false},
		{"scac digit", "ACME", "AC1", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, err := shared.NewCarrier(tc.carrier, tc.scac)
			if tc.ok {
				if err != nil || c.Name() != tc.carrier || c.SCAC() != tc.scac {
					t.Fatalf("NewCarrier = %+v, %v; want ok", c, err)
				}
				return
			}
			if !errors.Is(err, shared.ErrInvalidCarrier) {
				t.Fatalf("error = %v; want ErrInvalidCarrier", err)
			}
		})
	}
}

func TestParseDirection(t *testing.T) {
	for _, v := range []shared.Direction{shared.Inbound, shared.Outbound} {
		got, err := shared.ParseDirection(string(v))
		if err != nil || got != v {
			t.Fatalf("ParseDirection(%q) = %q, %v", v, got, err)
		}
	}
	for _, bad := range []string{"", "inbound", "BOTH"} {
		if _, err := shared.ParseDirection(bad); !errors.Is(err, shared.ErrInvalidDirection) {
			t.Fatalf("ParseDirection(%q) error = %v", bad, err)
		}
	}
}

func TestDockFlowAllows(t *testing.T) {
	tests := []struct {
		flow shared.DockFlow
		dir  shared.Direction
		want bool
	}{
		{shared.FlowInbound, shared.Inbound, true},
		{shared.FlowInbound, shared.Outbound, false},
		{shared.FlowOutbound, shared.Outbound, true},
		{shared.FlowOutbound, shared.Inbound, false},
		{shared.FlowBoth, shared.Inbound, true},
		{shared.FlowBoth, shared.Outbound, true},
		{shared.DockFlow("Sideways"), shared.Inbound, false},
		{shared.FlowBoth, shared.Direction("UP"), false},
	}
	for _, tc := range tests {
		if got := tc.flow.Allows(tc.dir); got != tc.want {
			t.Errorf("%s.Allows(%s) = %v; want %v", tc.flow, tc.dir, got, tc.want)
		}
	}
}

func TestParseDockFlow(t *testing.T) {
	for _, v := range []string{"Inbound", "Outbound", "Both"} {
		got, err := shared.ParseDockFlow(v)
		if err != nil || string(got) != v {
			t.Fatalf("ParseDockFlow(%q) = %q, %v", v, got, err)
		}
	}
	for _, bad := range []string{"", "both", "Dock"} {
		if _, err := shared.ParseDockFlow(bad); !errors.Is(err, shared.ErrInvalidDockFlow) {
			t.Fatalf("ParseDockFlow(%q) error = %v", bad, err)
		}
	}
}
