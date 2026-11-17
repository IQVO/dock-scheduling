package repocontract

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/claudioed/dock-scheduling/internal/application/ports"
	"github.com/claudioed/dock-scheduling/internal/application/repository"
	"github.com/claudioed/dock-scheduling/internal/domain/carrier"
	"github.com/claudioed/dock-scheduling/internal/domain/dockappointment"
)

func newCarrier(t *testing.T, scac, name, usdot, mc string) *carrier.Carrier {
	t.Helper()
	c, _, err := carrier.Register(scac, name, usdot, mc, base)
	if err != nil {
		t.Fatalf("Register(%s): %v", scac, err)
	}
	return c
}

func scacs(cs []*carrier.Carrier) []string {
	out := []string{}
	for _, c := range cs {
		out = append(out, c.SCAC().String())
	}
	return out
}

// runCarriers: save, read back and the version guard of the carrier registry.
func runCarriers(t *testing.T, f Factory) {
	ctx := context.Background()
	e := f(t)
	if _, err := e.Carriers.Get(ctx, "ACMF"); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("a SCAC that was never registered: %v", err)
	}
	if _, err := e.Carriers.GetForUpdate(ctx, "ACMF"); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("GetForUpdate of a SCAC that was never registered: %v", err)
	}
	c := newCarrier(t, "ACMF", "ACME Freight", "1234567", "654321")
	if err := e.Carriers.Save(ctx, c, 5); !errors.Is(err, dockappointment.ErrConcurrentUpdate) {
		t.Fatalf("insert with a non-zero expected version: %v", err)
	}
	if err := e.Carriers.Save(ctx, c, 0); err != nil {
		t.Fatal(err)
	}
	if err := e.Carriers.Save(ctx, newCarrier(t, "ACMF", "Other", "", ""), 0); !errors.Is(err, dockappointment.ErrConcurrentUpdate) {
		t.Fatalf("second insert of one SCAC: %v", err)
	}
	got, err := e.Carriers.GetForUpdate(ctx, "ACMF")
	if err != nil || !reflect.DeepEqual(got.Snapshot(), c.Snapshot()) {
		t.Fatalf("saved carrier = %+v, %v; want %+v", got, err, c.Snapshot())
	}
}

// suspendedCarrier saves ACMF and saves it again suspended, returning the
// environment, the suspended aggregate as saved and the instant of suspension.
func suspendedCarrier(t *testing.T, f Factory) (Env, *carrier.Carrier) {
	t.Helper()
	ctx := context.Background()
	e := f(t)
	if err := e.Carriers.Save(ctx, newCarrier(t, "ACMF", "ACME Freight", "1234567", "654321"), 0); err != nil {
		t.Fatal(err)
	}
	got, err := e.Carriers.GetForUpdate(ctx, "ACMF")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := got.Suspend("insurance lapsed", base.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := e.Carriers.Save(ctx, got, 7); !errors.Is(err, dockappointment.ErrConcurrentUpdate) {
		t.Fatalf("stale update: %v", err)
	}
	if err := e.Carriers.Save(ctx, got, 1); err != nil {
		t.Fatal(err)
	}
	return e, got
}

// runCarrierSuspension: a suspension is saved under the version guard.
func runCarrierSuspension(t *testing.T, f Factory) {
	ctx := context.Background()
	e, got := suspendedCarrier(t, f)
	back, _ := e.Carriers.Get(ctx, "ACMF")
	if back.Status() != carrier.Suspended || back.SuspensionReason() != "insurance lapsed" || !back.SuspendedAt().Equal(base.Add(time.Hour)) || back.Version() != 2 {
		t.Fatalf("suspended carrier = %+v", back.Snapshot())
	}
	if err := e.Carriers.Save(ctx, got, 1); !errors.Is(err, dockappointment.ErrConcurrentUpdate) {
		t.Fatalf("the same update twice: %v", err)
	}
}

// runCarrierReinstatement: a reinstatement clears the suspension and keeps the creation time.
func runCarrierReinstatement(t *testing.T, f Factory) {
	ctx := context.Background()
	e, _ := suspendedCarrier(t, f)
	back, _ := e.Carriers.Get(ctx, "ACMF")
	if _, err := back.Reinstate(base.Add(2 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := e.Carriers.Save(ctx, back, 2); err != nil {
		t.Fatal(err)
	}
	final, _ := e.Carriers.Get(ctx, "ACMF")
	if final.Status() != carrier.Active || final.SuspensionReason() != "" || !final.SuspendedAt().IsZero() || final.Version() != 3 || !final.CreatedAt().Equal(base) {
		t.Fatalf("reinstated carrier = %+v", final.Snapshot())
	}
}

// runCarrierPaging: ordered by SCAC, strictly after the cursor, narrowed by status.
func runCarrierPaging(t *testing.T, f Factory) {
	ctx := context.Background()
	e := f(t)
	for _, s := range []string{"DDDD", "AB", "CCC", "BBBB", "EEEE"} {
		c := newCarrier(t, s, "Carrier "+s, "", "")
		if s == "BBBB" || s == "DDDD" {
			if _, err := c.Suspend("r", base); err != nil {
				t.Fatal(err)
			}
		}
		if err := e.Carriers.Save(ctx, c, 0); err != nil {
			t.Fatal(err)
		}
	}
	list := func(status string, after string, limit int) []string {
		got, err := e.Carriers.ListPage(ctx, repository.CarrierFilter{Status: status}, after, limit)
		if err != nil {
			t.Fatal(err)
		}
		return scacs(got)
	}
	for name, tc := range map[string]struct {
		status, after string
		limit         int
		want          []string
	}{
		"all in SCAC order":     {"", "", 10, []string{"AB", "BBBB", "CCC", "DDDD", "EEEE"}},
		"a page":                {"", "", 2, []string{"AB", "BBBB"}},
		"the next page":         {"", "BBBB", 2, []string{"CCC", "DDDD"}},
		"the last page":         {"", "DDDD", 2, []string{"EEEE"}},
		"after the last":        {"", "EEEE", 2, []string{}},
		"after a SCAC not held": {"", "BC", 10, []string{"CCC", "DDDD", "EEEE"}},
		"only suspended":        {"SUSPENDED", "", 10, []string{"BBBB", "DDDD"}},
		"only active":           {"ACTIVE", "", 10, []string{"AB", "CCC", "EEEE"}},
		"active, after":         {"ACTIVE", "AB", 1, []string{"CCC"}},
	} {
		if got := list(tc.status, tc.after, tc.limit); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: got %v, want %v", name, got, tc.want)
		}
	}
}

// runCarrierRollback: a carrier saved in a unit of work that fails is not kept.
func runCarrierRollback(t *testing.T, f Factory) {
	ctx := context.Background()
	e := f(t)
	boom := errors.New("boom")
	err := e.UoW.Do(ctx, func(ctx context.Context) error {
		c := newCarrier(t, "ACMF", "ACME", "", "")
		if err := e.Carriers.Save(ctx, c, 0); err != nil {
			return err
		}
		if err := e.Events.PublishCarrierEvents(ctx, carrier.RegisteredEvent{Header: carrier.Header{Scac: "ACMF", Version: 1, At: base}, Name: "ACME", Status: carrier.Active}); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("Do must return fn's error unchanged: %v", err)
	}
	if _, err := e.Carriers.Get(ctx, "ACMF"); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("a rolled back carrier must not persist: %v", err)
	}
}
