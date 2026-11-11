package usecases_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/claudioed/dock-scheduling/internal/application/ports"
	"github.com/claudioed/dock-scheduling/internal/application/usecases"
	"github.com/claudioed/dock-scheduling/internal/domain/carrier"
	"github.com/claudioed/dock-scheduling/internal/domain/dockappointment"
)

var ctx = context.Background()

func sp(s string) *string { return &s }

func (e *env) suspend(scac string) {
	e.t.Helper()
	if _, err := e.svc.SuspendCarrier(ctx, usecases.SuspendCarrierCommand{SCAC: scac, Reason: "insurance lapsed"}); err != nil {
		e.t.Fatalf("SuspendCarrier(%s): %v", scac, err)
	}
}

func (e *env) registerCarrier(scac, name, usdot, mc string) {
	e.t.Helper()
	if _, err := e.svc.RegisterCarrier(ctx, usecases.RegisterCarrierCommand{SCAC: scac, Name: name, USDOT: usdot, MC: mc}); err != nil {
		e.t.Fatalf("RegisterCarrier(%s): %v", scac, err)
	}
}

func TestRegisterCarrierCreatesThenIsIdempotent(t *testing.T) {
	e := newEnv(t)
	cmd := usecases.RegisterCarrierCommand{SCAC: "ACMF", Name: "ACME Freight", USDOT: "1234567", MC: "654321"}
	res, err := e.svc.RegisterCarrier(ctx, cmd)
	if err != nil {
		t.Fatal(err)
	}
	v := res.Carrier
	if !res.Created || v.SCAC != "ACMF" || v.Name != "ACME Freight" || v.USDOT != "1234567" || v.MC != "MC654321" {
		t.Fatalf("created identity = %+v", res)
	}
	if v.Status != "ACTIVE" || v.Version != 1 || v.SuspendedAt != nil || v.SuspensionReason != "" || !v.CreatedAt.Equal(t0) || !v.UpdatedAt.Equal(t0) {
		t.Fatalf("created state = %+v", res)
	}
	if got := e.eventNames(); len(got) != 1 || got[0] != "CarrierRegistered" {
		t.Fatalf("events = %v", got)
	}
}

func TestRegisterCarrierOfTheSameDataIsANoOp(t *testing.T) {
	e := newEnv(t)
	cmd := usecases.RegisterCarrierCommand{SCAC: "ACMF", Name: "ACME Freight", USDOT: "1234567", MC: "654321"}
	e.registerCarrier("ACMF", "ACME Freight", "1234567", "MC-654321")
	e.clock.now = at9
	again, err := e.svc.RegisterCarrier(ctx, cmd)
	if err != nil || again.Created || again.Carrier.Version != 1 || !again.Carrier.UpdatedAt.Equal(t0) {
		t.Fatalf("the same data again = %+v, %v; want a no-op", again, err)
	}
	if got := e.eventNames(); len(got) != 1 {
		t.Fatalf("a no-op registration must not publish: %v", got)
	}
}

func TestRegisterCarrierOfAnotherDataIsRefused(t *testing.T) {
	e := newEnv(t)
	e.registerCarrier("ACMF", "ACME Freight", "1234567", "")
	_, err := e.svc.RegisterCarrier(ctx, usecases.RegisterCarrierCommand{SCAC: "ACMF", Name: "Another Name", USDOT: "1234567"})
	if !errors.Is(err, carrier.ErrCarrierAlreadyRegistered) {
		t.Fatalf("err = %v, want ErrAlreadyRegistered", err)
	}
	if v, _ := e.svc.GetCarrier(ctx, "ACMF"); v.Name != "ACME Freight" || v.Version != 1 || len(e.store.Published()) != 1 {
		t.Fatalf("a refused registration changes nothing: %+v", v)
	}
}

func TestRegisterCarrierValidation(t *testing.T) {
	for name, tc := range map[string]struct {
		cmd  usecases.RegisterCarrierCommand
		want error
	}{
		"scac":  {usecases.RegisterCarrierCommand{SCAC: "acmf", Name: "ACME"}, carrier.ErrInvalidScac},
		"name":  {usecases.RegisterCarrierCommand{SCAC: "ACMF", Name: " "}, carrier.ErrInvalidCarrierName},
		"usdot": {usecases.RegisterCarrierCommand{SCAC: "ACMF", Name: "ACME", USDOT: "123456789"}, carrier.ErrInvalidUSDOT},
		"mc":    {usecases.RegisterCarrierCommand{SCAC: "ACMF", Name: "ACME", MC: "FF12"}, carrier.ErrInvalidMC},
	} {
		e := newEnv(t)
		if _, err := e.svc.RegisterCarrier(ctx, tc.cmd); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", name, err, tc.want)
		}
		if len(e.store.Published()) != 0 {
			t.Errorf("%s: a refused registration publishes nothing", name)
		}
	}
}

func TestUpdateCarrier(t *testing.T) {
	e := newEnv(t)
	e.registerCarrier("ACMF", "ACME Freight", "", "")
	e.clock.now = at9
	v, err := e.svc.UpdateCarrier(ctx, usecases.UpdateCarrierCommand{SCAC: "ACMF", Name: sp("ACME Freight Lines"), USDOT: sp("7654321"), MC: sp("mx-12")})
	if err != nil {
		t.Fatal(err)
	}
	if v.Name != "ACME Freight Lines" || v.USDOT != "7654321" || v.MC != "MX12" || v.Version != 2 || !v.UpdatedAt.Equal(at9) || !v.CreatedAt.Equal(t0) {
		t.Fatalf("updated = %+v", v)
	}
	if got := e.eventNames(); len(got) != 2 || got[1] != "CarrierRegistered" {
		t.Fatalf("events = %v, want the update to publish CarrierRegistered again", got)
	}
	if last := e.store.Published()[1].(carrier.RegisteredEvent); last.CarrierVersion() != 2 || last.Name != "ACME Freight Lines" {
		t.Fatalf("event = %+v", last)
	}

	same, err := e.svc.UpdateCarrier(ctx, usecases.UpdateCarrierCommand{SCAC: "ACMF", Name: sp("ACME Freight Lines")})
	if err != nil || same.Version != 2 || len(e.store.Published()) != 2 {
		t.Fatalf("an update that changes nothing = %+v, %v; published %d", same, err, len(e.store.Published()))
	}
}

func TestUpdateCarrierRefusals(t *testing.T) {
	e := newEnv(t)
	e.registerCarrier("ACMF", "ACME Freight", "", "")
	if _, err := e.svc.UpdateCarrier(ctx, usecases.UpdateCarrierCommand{SCAC: "ZZZZ", Name: sp("x")}); !errors.Is(err, carrier.ErrCarrierNotFound) {
		t.Errorf("an unknown carrier: %v", err)
	}
	if _, err := e.svc.UpdateCarrier(ctx, usecases.UpdateCarrierCommand{SCAC: "acmf", Name: sp("x")}); !errors.Is(err, carrier.ErrInvalidScac) {
		t.Errorf("an invalid scac: %v", err)
	}
	if _, err := e.svc.UpdateCarrier(ctx, usecases.UpdateCarrierCommand{SCAC: "ACMF", USDOT: sp("abc")}); !errors.Is(err, carrier.ErrInvalidUSDOT) {
		t.Errorf("an invalid usdot: %v", err)
	}
	if _, err := e.svc.UpdateCarrier(ctx, usecases.UpdateCarrierCommand{SCAC: "ACMF", Name: sp("x"), ExpectedVersion: 5}); !errors.Is(err, usecases.ErrPreconditionFailed) {
		t.Errorf("a stale If-Match: %v", err)
	}
	if v, _ := e.svc.GetCarrier(ctx, "ACMF"); v.Version != 1 || len(e.store.Published()) != 1 {
		t.Errorf("refusals change nothing: %+v", v)
	}
	if v, err := e.svc.UpdateCarrier(ctx, usecases.UpdateCarrierCommand{SCAC: "ACMF", Name: sp("Right Version"), ExpectedVersion: 1}); err != nil || v.Version != 2 {
		t.Errorf("a matching If-Match is accepted: %+v %v", v, err)
	}
}

func TestSuspendCarrierPublishesCarrierSuspended(t *testing.T) {
	e := newEnv(t)
	e.registerCarrier("ACMF", "ACME Freight", "", "")
	e.clock.now = at9
	v, err := e.svc.SuspendCarrier(ctx, usecases.SuspendCarrierCommand{SCAC: "ACMF", Reason: "insurance lapsed", ExpectedVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	if v.Status != "SUSPENDED" || v.SuspensionReason != "insurance lapsed" || v.SuspendedAt == nil || !v.SuspendedAt.Equal(at9) || v.Version != 2 {
		t.Fatalf("suspended = %+v", v)
	}
	if s := e.store.Published()[1].(carrier.SuspendedEvent); s.Reason != "insurance lapsed" || !s.SuspendedAt.Equal(at9) || s.CarrierVersion() != 2 || s.SCAC() != "ACMF" {
		t.Fatalf("suspended event = %+v", s)
	}
}

func TestSuspendCarrierRefusals(t *testing.T) {
	e := newEnv(t)
	e.registerCarrier("ACMF", "ACME Freight", "", "")
	if _, err := e.svc.SuspendCarrier(ctx, usecases.SuspendCarrierCommand{SCAC: "NOPE", Reason: "x"}); !errors.Is(err, carrier.ErrCarrierNotFound) {
		t.Errorf("an unknown carrier: %v", err)
	}
	if _, err := e.svc.SuspendCarrier(ctx, usecases.SuspendCarrierCommand{SCAC: "ACMF", Reason: " "}); !errors.Is(err, carrier.ErrInvalidSuspensionReason) {
		t.Errorf("a blank reason: %v", err)
	}
	if _, err := e.svc.SuspendCarrier(ctx, usecases.SuspendCarrierCommand{SCAC: "ACMF", Reason: "x", ExpectedVersion: 4}); !errors.Is(err, usecases.ErrPreconditionFailed) {
		t.Errorf("a stale If-Match: %v", err)
	}
	e.suspend("ACMF")
	if _, err := e.svc.SuspendCarrier(ctx, usecases.SuspendCarrierCommand{SCAC: "ACMF", Reason: "again"}); !errors.Is(err, carrier.ErrCarrierAlreadySuspended) {
		t.Errorf("a second suspension: %v", err)
	}
}

func TestReinstateCarrierPublishesCarrierReinstated(t *testing.T) {
	e := newEnv(t)
	e.registerCarrier("ACMF", "ACME Freight", "", "")
	e.suspend("ACMF")
	e.clock.now = at10
	v, err := e.svc.ReinstateCarrier(ctx, usecases.ReinstateCarrierCommand{SCAC: "ACMF"})
	if err != nil || v.Status != "ACTIVE" || v.SuspendedAt != nil || v.SuspensionReason != "" || v.Version != 3 || !v.UpdatedAt.Equal(at10) {
		t.Fatalf("reinstated = %+v, %v", v, err)
	}
	if got := fmt.Sprint(e.eventNames()); got != "[CarrierRegistered CarrierSuspended CarrierReinstated]" {
		t.Fatalf("events = %v", got)
	}
}

func TestReinstateCarrierRefusals(t *testing.T) {
	e := newEnv(t)
	e.registerCarrier("ACMF", "ACME Freight", "", "")
	if _, err := e.svc.ReinstateCarrier(ctx, usecases.ReinstateCarrierCommand{SCAC: "ACMF"}); !errors.Is(err, carrier.ErrCarrierNotSuspended) {
		t.Errorf("reinstating an active carrier: %v", err)
	}
	e.suspend("ACMF")
	if _, err := e.svc.ReinstateCarrier(ctx, usecases.ReinstateCarrierCommand{SCAC: "ACMF", ExpectedVersion: 1}); !errors.Is(err, usecases.ErrPreconditionFailed) {
		t.Errorf("a stale If-Match on the reinstatement: %v", err)
	}
	if _, err := e.svc.ReinstateCarrier(ctx, usecases.ReinstateCarrierCommand{SCAC: "NOPE"}); !errors.Is(err, carrier.ErrCarrierNotFound) {
		t.Errorf("an unknown carrier: %v", err)
	}
}

func TestGetCarrier(t *testing.T) {
	e := newEnv(t)
	e.registerCarrier("ACMF", "ACME Freight", "123", "")
	v, err := e.svc.GetCarrier(ctx, "ACMF")
	if err != nil || v.SCAC != "ACMF" || v.USDOT != "123" {
		t.Fatalf("GetCarrier = %+v, %v", v, err)
	}
	if _, err := e.svc.GetCarrier(ctx, "NOPE"); !errors.Is(err, carrier.ErrCarrierNotFound) || errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("an unknown carrier is carrier.ErrCarrierNotFound, not the appointment's miss: %v", err)
	}
	if _, err := e.svc.GetCarrier(ctx, "nope"); !errors.Is(err, carrier.ErrInvalidScac) {
		t.Fatalf("an invalid scac: %v", err)
	}
}

func TestListCarriersPagesByScacAndFiltersByStatus(t *testing.T) {
	e := newEnv(t)
	for _, s := range []string{"DDDD", "AAAA", "CCCC", "BBBB", "EEEE"} {
		e.registerCarrier(s, "Carrier "+s, "", "")
	}
	e.suspend("BBBB")
	e.suspend("DDDD")
	var seen []string
	cursor := ""
	for pages := 0; pages < 10; pages++ {
		p, err := e.svc.ListCarriers(ctx, "", 2, cursor)
		if err != nil {
			t.Fatal(err)
		}
		for _, it := range p.Items {
			seen = append(seen, it.SCAC)
		}
		if p.NextCursor == "" {
			break
		}
		cursor = p.NextCursor
	}
	if fmt.Sprint(seen) != "[AAAA BBBB CCCC DDDD EEEE]" {
		t.Fatalf("paged order = %v", seen)
	}
}

func TestListCarriersFiltersByStatus(t *testing.T) {
	e := newEnv(t)
	for _, s := range []string{"DDDD", "AAAA", "CCCC", "BBBB", "EEEE"} {
		e.registerCarrier(s, "Carrier "+s, "", "")
	}
	e.suspend("BBBB")
	e.suspend("DDDD")
	p, err := e.svc.ListCarriers(ctx, carrier.Suspended, 0, "")
	if err != nil || len(p.Items) != 2 || p.Items[0].SCAC != "BBBB" || p.Items[1].SCAC != "DDDD" || p.NextCursor != "" {
		t.Fatalf("suspended = %+v, %v", p, err)
	}
	p, err = e.svc.ListCarriers(ctx, carrier.Active, 0, "")
	if err != nil || len(p.Items) != 3 {
		t.Fatalf("active = %+v, %v", p, err)
	}
	empty := newEnv(t)
	if p, err := empty.svc.ListCarriers(ctx, "", 0, ""); err != nil || p.Items == nil || len(p.Items) != 0 {
		t.Fatalf("an empty registry is an empty, non-nil page: %+v %v", p, err)
	}
}

func TestListCarriersRejectsABadLimitOrCursor(t *testing.T) {
	e := newEnv(t)
	for _, limit := range []int{-1, 501} {
		if _, err := e.svc.ListCarriers(ctx, "", limit, ""); !errors.Is(err, usecases.ErrInvalidListQuery) {
			t.Errorf("limit %d: %v", limit, err)
		}
	}
	for _, cursor := range []string{"!!!", "bm90LWpzb24", "e30", "eyJpIjoiYWJjIn0"} { // not base64, not JSON, no id, id not a SCAC
		if _, err := e.svc.ListCarriers(ctx, "", 1, cursor); !errors.Is(err, usecases.ErrInvalidListQuery) {
			t.Errorf("cursor %q: %v", cursor, err)
		}
	}
}

func TestBookingWithASuspendedCarrierIsRefusedUntilReinstated(t *testing.T) {
	e := newEnv(t)
	e.registerCarrier("ACMF", "ACME Freight", "", "")
	if _, err := e.svc.SuspendCarrier(ctx, usecases.SuspendCarrierCommand{SCAC: "ACMF", Reason: "insurance lapsed"}); err != nil {
		t.Fatal(err)
	}
	before := len(e.store.Published())
	_, err := e.svc.BookAppointment(ctx, inboundCmd("D-BOTH", at8, at10))
	if !errors.Is(err, carrier.ErrCarrierSuspended) {
		t.Fatalf("err = %v, want ErrCarrierSuspended", err)
	}
	if len(e.store.Published()) != before {
		t.Fatal("a refused booking publishes nothing")
	}
	if _, err := e.svc.ReinstateCarrier(ctx, usecases.ReinstateCarrierCommand{SCAC: "ACMF"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.BookAppointment(ctx, inboundCmd("D-BOTH", at8, at10)); err != nil {
		t.Fatalf("a reinstated carrier books: %v", err)
	}
}

func TestBookingWithAnActiveCarrierAnUnknownScacOrNoScacIsAcceptedAsBefore(t *testing.T) {
	e := newEnv(t)
	e.registerCarrier("ACMF", "ACME Freight", "", "")
	e.registerCarrier("OTHR", "Other", "", "")
	if _, err := e.svc.SuspendCarrier(ctx, usecases.SuspendCarrierCommand{SCAC: "OTHR", Reason: "r"}); err != nil {
		t.Fatal(err)
	}
	active := inboundCmd("D-BOTH", at8, at9, "ASN-1") // ACMF is registered and ACTIVE
	unknown := inboundCmd("D-BOTH", at9, at10, "ASN-2")
	unknown.CarrierSCAC = "ZZZZ" // not in the registry
	none := inboundCmd("D-BOTH", at10, at11, "ASN-3")
	none.CarrierSCAC = "" // names no scac
	for name, cmd := range map[string]usecases.BookCommand{"active": active, "unknown": unknown, "none": none} {
		if _, err := e.svc.BookAppointment(ctx, cmd); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestBookingWithoutAScacNeverReadsTheRegistry(t *testing.T) {
	e := newEnv(t)
	boom := errors.New("registry is down")
	e.svc = usecases.New(usecases.Deps{
		Appointments: e.store.Appointments(), Calendars: e.store.Calendars(), Carriers: brokenCarriers{e.store.Carriers(), boom}, Doors: e.store.Doors(),
		Asns: e.store.Asns(), Cutoffs: e.store.Cutoffs(), Processed: e.store, Events: e.store, Metrics: e.metrics, IDs: e.ids, Clock: e.clock, UoW: e.store,
	})
	none := inboundCmd("D-BOTH", at8, at9)
	none.CarrierSCAC = ""
	if _, err := e.svc.BookAppointment(ctx, none); err != nil {
		t.Fatalf("no scac, no lookup: %v", err)
	}
	if _, err := e.svc.BookAppointment(ctx, inboundCmd("D-BOTH", at9, at10, "ASN-2")); !errors.Is(err, boom) {
		t.Fatalf("a lookup failure is not swallowed as 'not registered': %v", err)
	}
}

// brokenCarriers fails every read, so a test can tell whether the registry was consulted.
type brokenCarriers struct {
	ports.CarrierRepo
	err error
}

func (b brokenCarriers) Get(context.Context, carrier.Scac) (*carrier.Carrier, error) {
	return nil, b.err
}

func TestRescheduleOfASuspendedCarriersAppointmentIsRefused(t *testing.T) {
	e := newEnv(t)
	id := e.book(inboundCmd("D-BOTH", at8, at9)) // booked while the carrier was not in the registry
	e.registerCarrier("ACMF", "ACME Freight", "", "")
	if _, err := e.svc.SuspendCarrier(ctx, usecases.SuspendCarrierCommand{SCAC: "ACMF", Reason: "insurance lapsed"}); err != nil {
		t.Fatal(err)
	}
	if got := e.state(id); got != dockappointment.Booked {
		t.Fatalf("suspending a carrier cancels nothing: %s", got)
	}
	_, err := e.svc.RescheduleAppointment(ctx, usecases.RescheduleCommand{AppointmentID: id, WindowStart: at10, WindowEnd: at11})
	if !errors.Is(err, carrier.ErrCarrierSuspended) {
		t.Fatalf("err = %v, want ErrCarrierSuspended", err)
	}
	if _, err := e.svc.CancelAppointment(ctx, usecases.CancelCommand{AppointmentID: id, Reason: "SITE_REQUEST"}); err != nil {
		t.Fatalf("a human can still cancel it: %v", err)
	}
}

func TestRescheduleOfAnActiveCarriersAppointmentIsAccepted(t *testing.T) {
	e := newEnv(t)
	e.registerCarrier("ACMF", "ACME Freight", "", "")
	id := e.book(inboundCmd("D-BOTH", at8, at9))
	if _, err := e.svc.RescheduleAppointment(ctx, usecases.RescheduleCommand{AppointmentID: id, WindowStart: at10, WindowEnd: at11}); err != nil {
		t.Fatal(err)
	}
}
