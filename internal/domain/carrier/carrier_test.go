package carrier_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/claudioed/dock-scheduling/internal/domain/carrier"
)

var (
	t0 = time.Date(2030, 1, 7, 8, 0, 0, 0, time.UTC)
	t1 = t0.Add(time.Hour)
	t2 = t0.Add(2 * time.Hour)
)

func ptr(s string) *string { return &s }

func register(t *testing.T) *carrier.Carrier {
	t.Helper()
	c, _, err := carrier.Register("ACMF", "ACME Freight", "1234567", "MC-654321", t0)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	return c
}

func TestRegisterCreatesAnActiveCarrierAtVersionOne(t *testing.T) {
	c, events, err := carrier.Register("ACMF", " ACME Freight ", "1234567", "654321", t0.In(time.FixedZone("x", 3600)))
	if err != nil {
		t.Fatal(err)
	}
	id := c.Identity()
	if c.SCAC() != "ACMF" || id.Name() != "ACME Freight" || id.USDOT() != "1234567" || id.MC() != "MC654321" {
		t.Fatalf("registered identity = %+v", c.Snapshot())
	}
	if c.Status() != carrier.Active || c.SuspensionReason() != "" || !c.SuspendedAt().IsZero() || c.Version() != 1 {
		t.Fatalf("registered state = %+v", c.Snapshot())
	}
	if !c.CreatedAt().Equal(t0) || !c.UpdatedAt().Equal(t0) || c.CreatedAt().Location() != time.UTC {
		t.Fatalf("registered times = %+v", c.Snapshot())
	}
	if len(events) != 1 {
		t.Fatalf("events = %v, want one CarrierRegistered", events)
	}
}

func TestRegisterRaisesCarrierRegisteredWithTheFullSnapshot(t *testing.T) {
	_, events, err := carrier.Register("ACMF", " ACME Freight ", "1234567", "654321", t0.In(time.FixedZone("x", 3600)))
	if err != nil || len(events) != 1 {
		t.Fatalf("Register = %v, %v", events, err)
	}
	e, ok := events[0].(carrier.RegisteredEvent)
	if !ok || e.EventName() != "CarrierRegistered" || e.SCAC() != "ACMF" || e.CarrierVersion() != 1 || !e.OccurredAt().Equal(t0) || e.OccurredAt().Location() != time.UTC {
		t.Fatalf("event = %#v", events[0])
	}
	if e.Name != "ACME Freight" || e.USDOT != "1234567" || e.MC != "MC654321" || e.Status != carrier.Active || e.Reason != "" || !e.SuspendedAt.IsZero() {
		t.Fatalf("snapshot of the event = %#v", e)
	}
}

func TestRegisterValidatesBeforeCreating(t *testing.T) {
	for name, tc := range map[string]struct {
		scac, name, usdot, mc string
		want                  error
	}{
		"scac":  {"acmf", "ACME", "", "", carrier.ErrInvalidScac},
		"name":  {"ACMF", " ", "", "", carrier.ErrInvalidCarrierName},
		"usdot": {"ACMF", "ACME", "x", "", carrier.ErrInvalidUSDOT},
		"mc":    {"ACMF", "ACME", "", "FF1", carrier.ErrInvalidMC},
	} {
		c, events, err := carrier.Register(tc.scac, tc.name, tc.usdot, tc.mc, t0)
		if !errors.Is(err, tc.want) || c != nil || events != nil {
			t.Errorf("%s: Register = %v, %v, %v; want %v and nothing", name, c, events, err, tc.want)
		}
	}
}

func TestEnsureRegisteredIsIdempotentForTheSameDataOnly(t *testing.T) {
	c := register(t)
	same, _ := carrier.NewIdentity("ACME Freight", "1234567", "MC654321")
	if err := c.EnsureRegistered(same); err != nil {
		t.Fatalf("same data must be accepted: %v", err)
	}
	for name, other := range map[string][3]string{
		"name": {"ACME Freight Co", "1234567", "MC654321"}, "usdot": {"ACME Freight", "7654321", "MC654321"},
		"mc": {"ACME Freight", "1234567", "MX654321"}, "no usdot": {"ACME Freight", "", "MC654321"}, "no mc": {"ACME Freight", "1234567", ""},
	} {
		id, err := carrier.NewIdentity(other[0], other[1], other[2])
		if err != nil {
			t.Fatal(err)
		}
		err = c.EnsureRegistered(id)
		if !errors.Is(err, carrier.ErrCarrierAlreadyRegistered) || !strings.Contains(err.Error(), "ACMF") {
			t.Errorf("%s: EnsureRegistered err = %v, want ErrCarrierAlreadyRegistered naming the SCAC", name, err)
		}
	}
	if c.Version() != 1 {
		t.Fatalf("EnsureRegistered must not change the carrier: version %d", c.Version())
	}
}

func TestUpdateChangesDataAndRaisesCarrierRegistered(t *testing.T) {
	c := register(t)
	events, err := c.Update(carrier.Change{Name: ptr("ACME Freight Lines"), USDOT: ptr("7"), MC: ptr("mx-8")}, t1)
	if err != nil {
		t.Fatal(err)
	}
	id := c.Identity()
	if id.Name() != "ACME Freight Lines" || id.USDOT() != "7" || id.MC() != "MX8" || c.Version() != 2 || !c.UpdatedAt().Equal(t1) || !c.CreatedAt().Equal(t0) {
		t.Fatalf("updated carrier = %+v", c.Snapshot())
	}
	if len(events) != 1 {
		t.Fatalf("events = %v", events)
	}
	e := events[0].(carrier.RegisteredEvent)
	if e.CarrierVersion() != 2 || !e.OccurredAt().Equal(t1) || e.Name != "ACME Freight Lines" || e.USDOT != "7" || e.MC != "MX8" || e.Status != carrier.Active {
		t.Fatalf("event = %#v", e)
	}
}

func TestUpdateKeepsWhatIsNotNamedAndClearsWhatIsEmpty(t *testing.T) {
	c := register(t)
	if _, err := c.Update(carrier.Change{Name: ptr("New Name")}, t1); err != nil {
		t.Fatal(err)
	}
	if id := c.Identity(); id.Name() != "New Name" || id.USDOT() != "1234567" || id.MC() != "MC654321" {
		t.Fatalf("a partial update must keep the rest: %+v", id)
	}
	if _, err := c.Update(carrier.Change{USDOT: ptr("")}, t1); err != nil {
		t.Fatal(err)
	}
	if id := c.Identity(); id.USDOT() != "" || id.MC() != "MC654321" || c.Version() != 3 {
		t.Fatalf("clearing usdot: %+v version %d", id, c.Version())
	}
	if _, err := c.Update(carrier.Change{MC: ptr("")}, t1); err != nil {
		t.Fatal(err)
	}
	if id := c.Identity(); id.MC() != "" || c.Version() != 4 {
		t.Fatalf("clearing mc: %+v version %d", id, c.Version())
	}
}

func TestUpdateThatChangesNothingRaisesNothing(t *testing.T) {
	c := register(t)
	for name, ch := range map[string]carrier.Change{
		"empty": {}, "same name": {Name: ptr("ACME Freight")}, "same name padded": {Name: ptr("  ACME Freight ")},
		"same usdot": {USDOT: ptr("1234567")}, "same mc spelled otherwise": {MC: ptr("654321")}, "everything the same": {Name: ptr("ACME Freight"), USDOT: ptr("1234567"), MC: ptr("MC-654321")},
	} {
		events, err := c.Update(ch, t1)
		if err != nil || events != nil || c.Version() != 1 || !c.UpdatedAt().Equal(t0) {
			t.Errorf("%s: Update = %v, %v; version %d updated %v; want a no-op", name, events, err, c.Version(), c.UpdatedAt())
		}
	}
}

func TestUpdateRefusesInvalidDataAndChangesNothing(t *testing.T) {
	for name, tc := range map[string]struct {
		ch   carrier.Change
		want error
	}{
		"blank name": {carrier.Change{Name: ptr(" ")}, carrier.ErrInvalidCarrierName},
		"bad usdot":  {carrier.Change{USDOT: ptr("123456789")}, carrier.ErrInvalidUSDOT},
		"bad mc":     {carrier.Change{MC: ptr("XX1")}, carrier.ErrInvalidMC},
		"good + bad": {carrier.Change{Name: ptr("Fine"), MC: ptr("XX1")}, carrier.ErrInvalidMC},
	} {
		c := register(t)
		events, err := c.Update(tc.ch, t1)
		if !errors.Is(err, tc.want) || events != nil || c.Version() != 1 || c.Identity().Name() != "ACME Freight" {
			t.Errorf("%s: Update = %v, %v (version %d name %q), want %v and no change", name, events, err, c.Version(), c.Identity().Name(), tc.want)
		}
	}
}

func TestUpdateOfASuspendedCarrierKeepsItSuspended(t *testing.T) {
	c := register(t)
	if _, err := c.Suspend("insurance lapsed", t1); err != nil {
		t.Fatal(err)
	}
	events, err := c.Update(carrier.Change{Name: ptr("Renamed")}, t2)
	if err != nil {
		t.Fatal(err)
	}
	e := events[0].(carrier.RegisteredEvent)
	if c.Status() != carrier.Suspended || e.Status != carrier.Suspended || e.Reason != "insurance lapsed" || !e.SuspendedAt.Equal(t1) || c.Version() != 3 {
		t.Fatalf("carrier = %+v event = %#v", c.Snapshot(), e)
	}
}

func TestSuspendMakesTheCarrierSuspendedAndRaisesCarrierSuspended(t *testing.T) {
	c := register(t)
	events, err := c.Suspend("  insurance lapsed  ", t1)
	if err != nil {
		t.Fatal(err)
	}
	if c.Status() != carrier.Suspended || c.SuspensionReason() != "insurance lapsed" || !c.SuspendedAt().Equal(t1) || c.Version() != 2 || !c.UpdatedAt().Equal(t1) {
		t.Fatalf("suspended carrier = %+v", c.Snapshot())
	}
	s, ok := events[0].(carrier.SuspendedEvent)
	if len(events) != 1 || !ok || s.EventName() != "CarrierSuspended" || s.SCAC() != "ACMF" || s.CarrierVersion() != 2 || s.Reason != "insurance lapsed" || !s.SuspendedAt.Equal(t1) || !s.OccurredAt().Equal(t1) {
		t.Fatalf("event = %#v", events)
	}
}

func TestReinstateMakesTheCarrierActiveAndRaisesCarrierReinstated(t *testing.T) {
	c := register(t)
	if _, err := c.Suspend("insurance lapsed", t1); err != nil {
		t.Fatal(err)
	}
	events, err := c.Reinstate(t2)
	if err != nil {
		t.Fatal(err)
	}
	if c.Status() != carrier.Active || c.SuspensionReason() != "" || !c.SuspendedAt().IsZero() || c.Version() != 3 || !c.UpdatedAt().Equal(t2) {
		t.Fatalf("reinstated carrier = %+v", c.Snapshot())
	}
	r, ok := events[0].(carrier.ReinstatedEvent)
	if len(events) != 1 || !ok || r.EventName() != "CarrierReinstated" || r.SCAC() != "ACMF" || r.CarrierVersion() != 3 || !r.ReinstatedAt.Equal(t2) || !r.OccurredAt().Equal(t2) {
		t.Fatalf("event = %#v", events)
	}
}

func TestSuspendRules(t *testing.T) {
	c := register(t)
	for _, reason := range []string{"", "   ", strings.Repeat("r", 201), "bad\x00reason", "\x00leading", "two\nlines"} {
		if events, err := c.Suspend(reason, t1); !errors.Is(err, carrier.ErrInvalidSuspensionReason) || events != nil || c.Status() != carrier.Active || c.Version() != 1 {
			t.Errorf("Suspend(%q) = %v, %v; want ErrInvalidSuspensionReason and no change", reason, events, err)
		}
	}
	if _, err := c.Suspend(strings.Repeat("r", 200), t1); err != nil {
		t.Fatalf("a 200 character reason is the longest accepted: %v", err)
	}
	events, err := c.Suspend("again", t2)
	if !errors.Is(err, carrier.ErrCarrierAlreadySuspended) || events != nil || c.Version() != 2 || c.SuspensionReason() != strings.Repeat("r", 200) {
		t.Fatalf("a second suspension = %v, %v; version %d; want ErrCarrierAlreadySuspended and no change", events, err, c.Version())
	}
	if _, err := c.Suspend("", t2); !errors.Is(err, carrier.ErrInvalidSuspensionReason) {
		t.Fatalf("the reason is checked before the state: %v", err)
	}
}

func TestReinstateRules(t *testing.T) {
	c := register(t)
	events, err := c.Reinstate(t1)
	if !errors.Is(err, carrier.ErrCarrierNotSuspended) || events != nil || c.Version() != 1 || !strings.Contains(err.Error(), "ACMF") {
		t.Fatalf("reinstating an active carrier = %v, %v; want ErrCarrierNotSuspended", events, err)
	}
}

func TestCheckCanBook(t *testing.T) {
	c := register(t)
	if err := c.CheckCanBook(); err != nil {
		t.Fatalf("an ACTIVE carrier may book: %v", err)
	}
	if _, err := c.Suspend("insurance lapsed", t1); err != nil {
		t.Fatal(err)
	}
	err := c.CheckCanBook()
	if !errors.Is(err, carrier.ErrCarrierSuspended) || !strings.Contains(err.Error(), "ACMF") || !strings.Contains(err.Error(), "insurance lapsed") {
		t.Fatalf("a SUSPENDED carrier is refused naming the SCAC and the reason: %v", err)
	}
	if _, err := c.Reinstate(t2); err != nil {
		t.Fatal(err)
	}
	if err := c.CheckCanBook(); err != nil {
		t.Fatalf("a reinstated carrier may book: %v", err)
	}
}
