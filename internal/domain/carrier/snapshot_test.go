package carrier_test

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/claudioed/dock-scheduling/internal/domain/carrier"
)

func TestSnapshotRoundTrips(t *testing.T) {
	c := register(t)
	if _, err := c.Suspend("insurance lapsed", t1); err != nil {
		t.Fatal(err)
	}
	for _, src := range []*carrier.Carrier{register(t), c} {
		snap := src.Snapshot()
		back, err := carrier.Restore(snap)
		if err != nil {
			t.Fatalf("Restore(%+v): %v", snap, err)
		}
		if !reflect.DeepEqual(back.Snapshot(), snap) {
			t.Fatalf("round trip = %+v, want %+v", back.Snapshot(), snap)
		}
	}
}

func TestRestoreNormalisesTimesToUTC(t *testing.T) {
	snap := register(t).Snapshot()
	loc := time.FixedZone("x", -3*3600)
	snap.CreatedAt, snap.UpdatedAt = snap.CreatedAt.In(loc), snap.UpdatedAt.In(loc)
	c, err := carrier.Restore(snap)
	if err != nil {
		t.Fatal(err)
	}
	if c.CreatedAt().Location() != time.UTC || c.UpdatedAt().Location() != time.UTC {
		t.Fatalf("times = %v %v, want UTC", c.CreatedAt().Location(), c.UpdatedAt().Location())
	}
	susp := register(t)
	_, _ = susp.Suspend("r", t1)
	s := susp.Snapshot()
	s.SuspendedAt = s.SuspendedAt.In(loc)
	r, err := carrier.Restore(s)
	if err != nil || r.SuspendedAt().Location() != time.UTC {
		t.Fatalf("suspended at = %v, %v", r.SuspendedAt().Location(), err)
	}
}

func TestRestoreRefusesAnySnapshotThatBreaksAnInvariant(t *testing.T) {
	active := register(t).Snapshot()
	susp := func() carrier.Snapshot {
		c := register(t)
		_, _ = c.Suspend("insurance lapsed", t1)
		return c.Snapshot()
	}
	tests := map[string]struct {
		snap carrier.Snapshot
		edit func(*carrier.Snapshot)
		want error
	}{
		"scac":                          {active, func(s *carrier.Snapshot) { s.SCAC = "acmf" }, carrier.ErrInvalidScac},
		"blank name":                    {active, func(s *carrier.Snapshot) { s.Name = "" }, carrier.ErrInvalidCarrierName},
		"name not trimmed":              {active, func(s *carrier.Snapshot) { s.Name = " ACME" }, carrier.ErrInvalidSnapshot},
		"usdot":                         {active, func(s *carrier.Snapshot) { s.USDOT = "x" }, carrier.ErrInvalidUSDOT},
		"usdot not canonical":           {active, func(s *carrier.Snapshot) { s.USDOT = " 12" }, carrier.ErrInvalidSnapshot},
		"mc":                            {active, func(s *carrier.Snapshot) { s.MC = "FF1" }, carrier.ErrInvalidMC},
		"mc not canonical":              {active, func(s *carrier.Snapshot) { s.MC = "123" }, carrier.ErrInvalidSnapshot},
		"status":                        {active, func(s *carrier.Snapshot) { s.Status = "GONE" }, carrier.ErrInvalidStatus},
		"version zero":                  {active, func(s *carrier.Snapshot) { s.Version = 0 }, carrier.ErrInvalidSnapshot},
		"active with a reason":          {active, func(s *carrier.Snapshot) { s.Reason = "x" }, carrier.ErrInvalidSnapshot},
		"active with a suspension time": {active, func(s *carrier.Snapshot) { s.SuspendedAt = t1 }, carrier.ErrInvalidSnapshot},
		"suspended without a reason":    {susp(), func(s *carrier.Snapshot) { s.Reason = "" }, carrier.ErrInvalidSnapshot},
		"suspended reason not trimmed":  {susp(), func(s *carrier.Snapshot) { s.Reason = " x" }, carrier.ErrInvalidSnapshot},
		"suspended without a time":      {susp(), func(s *carrier.Snapshot) { s.SuspendedAt = time.Time{} }, carrier.ErrInvalidSnapshot},
	}
	for name, tc := range tests {
		snap := tc.snap
		tc.edit(&snap)
		if _, err := carrier.Restore(snap); !errors.Is(err, tc.want) {
			t.Errorf("%s: Restore err = %v, want %v", name, err, tc.want)
		}
	}
}
