package carrier_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/claudioed/dock-scheduling/internal/domain/carrier"
)

func TestNewScac(t *testing.T) {
	for _, ok := range []string{"AB", "ACM", "ACMF", "ZZ"} {
		got, err := carrier.NewScac(ok)
		if err != nil || got.String() != ok {
			t.Errorf("NewScac(%q) = %q, %v; want it accepted", ok, got, err)
		}
	}
	for _, bad := range []string{"", "A", "ACMFX", "acmf", "Acm", "AC1", "AC-M", " ACM", "ACM ", "AC\nM", "ÅCM", "AC\n"} {
		if _, err := carrier.NewScac(bad); !errors.Is(err, carrier.ErrInvalidScac) {
			t.Errorf("NewScac(%q) err = %v, want ErrInvalidScac", bad, err)
		}
	}
}

func TestParseStatus(t *testing.T) {
	for _, ok := range []carrier.Status{carrier.Active, carrier.Suspended} {
		if got, err := carrier.ParseStatus(string(ok)); err != nil || got != ok {
			t.Errorf("ParseStatus(%q) = %q, %v", ok, got, err)
		}
	}
	for _, bad := range []string{"", "active", "DELETED"} {
		if _, err := carrier.ParseStatus(bad); !errors.Is(err, carrier.ErrInvalidStatus) {
			t.Errorf("ParseStatus(%q) err = %v, want ErrInvalidStatus", bad, err)
		}
	}
}

func TestNewIdentity(t *testing.T) {
	tests := []struct {
		name, usdot, mc string
		wantName        string
		wantUSDOT       string
		wantMC          string
		wantErr         error
	}{
		{name: "ACME Freight", wantName: "ACME Freight"},
		{name: "  ACME Freight  ", wantName: "ACME Freight"},
		{name: strings.Repeat("x", 100), wantName: strings.Repeat("x", 100)},
		{name: "ACME", usdot: "1", wantName: "ACME", wantUSDOT: "1"},
		{name: "ACME", usdot: "12345678", wantName: "ACME", wantUSDOT: "12345678"},
		{name: "ACME", usdot: " 123456 ", wantName: "ACME", wantUSDOT: "123456"},
		{name: "ACME", mc: "123456", wantName: "ACME", wantMC: "MC123456"},
		{name: "ACME", mc: "MC123456", wantName: "ACME", wantMC: "MC123456"},
		{name: "ACME", mc: "mc123456", wantName: "ACME", wantMC: "MC123456"},
		{name: "ACME", mc: "MC-123456", wantName: "ACME", wantMC: "MC123456"},
		{name: "ACME", mc: "MC 123456", wantName: "ACME", wantMC: "MC123456"},
		{name: "ACME", mc: "MX-9", wantName: "ACME", wantMC: "MX9"},
		{name: "ACME", mc: " mx00123 ", wantName: "ACME", wantMC: "MX00123"},
		{name: "ACME", mc: "MC12345678", wantName: "ACME", wantMC: "MC12345678"},
		{name: "ACME", usdot: "12", mc: "34", wantName: "ACME", wantUSDOT: "12", wantMC: "MC34"},

		{name: "", wantErr: carrier.ErrInvalidCarrierName},
		{name: "   ", wantErr: carrier.ErrInvalidCarrierName},
		{name: strings.Repeat("x", 101), wantErr: carrier.ErrInvalidCarrierName},
		{name: "AC\x00ME", wantErr: carrier.ErrInvalidCarrierName},
		{name: "\x00ACME", wantErr: carrier.ErrInvalidCarrierName},
		{name: "AC\nME", wantErr: carrier.ErrInvalidCarrierName},
		{name: "ACME", usdot: "123456789", wantErr: carrier.ErrInvalidUSDOT},
		{name: "ACME", usdot: "12A4", wantErr: carrier.ErrInvalidUSDOT},
		{name: "ACME", usdot: "-1", wantErr: carrier.ErrInvalidUSDOT},
		{name: "ACME", usdot: "12 34", wantErr: carrier.ErrInvalidUSDOT},
		{name: "ACME", usdot: "USDOT123", wantErr: carrier.ErrInvalidUSDOT},
		{name: "ACME", mc: "MC", wantErr: carrier.ErrInvalidMC},
		{name: "ACME", mc: "MC-", wantErr: carrier.ErrInvalidMC},
		{name: "ACME", mc: "FF123", wantErr: carrier.ErrInvalidMC},
		{name: "ACME", mc: "MC123456789", wantErr: carrier.ErrInvalidMC},
		{name: "ACME", mc: "MC12A", wantErr: carrier.ErrInvalidMC},
		{name: "ACME", mc: "MCMC123", wantErr: carrier.ErrInvalidMC},
		{name: "ACME", mc: "MC--123", wantErr: carrier.ErrInvalidMC},
		{name: "ACME", mc: "123-456", wantErr: carrier.ErrInvalidMC},
	}
	for _, tc := range tests {
		got, err := carrier.NewIdentity(tc.name, tc.usdot, tc.mc)
		if tc.wantErr != nil {
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("NewIdentity(%q,%q,%q) err = %v, want %v", tc.name, tc.usdot, tc.mc, err, tc.wantErr)
			}
			continue
		}
		if err != nil {
			t.Errorf("NewIdentity(%q,%q,%q): %v", tc.name, tc.usdot, tc.mc, err)
			continue
		}
		if got.Name() != tc.wantName || got.USDOT() != tc.wantUSDOT || got.MC() != tc.wantMC {
			t.Errorf("NewIdentity(%q,%q,%q) = %q/%q/%q, want %q/%q/%q", tc.name, tc.usdot, tc.mc, got.Name(), got.USDOT(), got.MC(), tc.wantName, tc.wantUSDOT, tc.wantMC)
		}
	}
}

func TestIdentityIsComparableByValue(t *testing.T) {
	a, _ := carrier.NewIdentity("ACME", "123", "MC9")
	b, _ := carrier.NewIdentity(" ACME ", "123", "9")
	c, _ := carrier.NewIdentity("ACME", "123", "MX9")
	if a != b || a == c {
		t.Fatalf("identity equality is by canonical value: a==b %v, a==c %v", a == b, a == c)
	}
}
