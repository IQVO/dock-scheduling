package repocontract

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/claudioed/dock-scheduling/internal/application/repository"
	"github.com/claudioed/dock-scheduling/internal/domain/dockappointment"
	"github.com/claudioed/dock-scheduling/internal/domain/dockcalendar"
	"github.com/claudioed/dock-scheduling/internal/domain/shared"
)

func runCalendars(t *testing.T, f Factory) {
	ctx := context.Background()
	e := f(t)
	def, err := e.Calendars.Get(ctx, "WH1")
	if err != nil || def.Version() != 0 || def.Settings() != dockcalendar.DefaultSettings() {
		t.Fatalf("default calendar = %+v, %v", def, err)
	}
	if _, err := e.Calendars.GetForUpdate(ctx, "WH1"); err != nil {
		t.Fatal(err)
	}
	row, _ := dockcalendar.NewOperatingHours("D1", dockcalendar.Mon, "06:00", "22:00", "America/Sao_Paulo")
	if _, err := def.DeclareOperatingHours([]dockcalendar.OperatingHours{row}, nil, base); err != nil {
		t.Fatal(err)
	}
	if err := e.Calendars.Save(ctx, def, 5); !errors.Is(err, dockappointment.ErrConcurrentUpdate) {
		t.Fatalf("insert with a non-zero expected version: %v", err)
	}
	if err := e.Calendars.Save(ctx, def, 0); err != nil {
		t.Fatal(err)
	}
	if err := e.Calendars.Save(ctx, def, 0); !errors.Is(err, dockappointment.ErrConcurrentUpdate) {
		t.Fatalf("second insert of one site: %v", err)
	}
	got, _ := e.Calendars.Get(ctx, "WH1")
	if got.Version() != 1 || len(got.OperatingHours()) != 1 || got.OperatingHours()[0].Timezone() != "America/Sao_Paulo" {
		t.Fatalf("saved calendar = %+v", got.Snapshot())
	}
	if err := e.Calendars.Save(ctx, got, 7); !errors.Is(err, dockappointment.ErrConcurrentUpdate) {
		t.Fatalf("stale update: %v", err)
	}
}

func runCalendarUpdates(t *testing.T, f Factory) {
	ctx := context.Background()
	e := f(t)
	cal, _ := e.Calendars.Get(ctx, "WH1")
	if err := cal.DeclareSettings(dockcalendar.Settings{MaxWindowMinutes: 120, SlotGranularityMinutes: 30, ArrivalToleranceMinutes: 5, NoShowGraceMinutes: 90}); err != nil {
		t.Fatal(err)
	}
	if err := e.Calendars.Save(ctx, cal, 0); err != nil {
		t.Fatal(err)
	}
	got, _ := e.Calendars.Get(ctx, "WH1")
	if _, _, err := got.ScheduleClosure("clo-123e4567-e89b-12d3-a456-426614174001", "D1", base, base.Add(time.Hour), "x", nil, base); err != nil {
		t.Fatal(err)
	}
	if err := e.Calendars.Save(ctx, got, 0); !errors.Is(err, dockappointment.ErrConcurrentUpdate) {
		t.Fatalf("stale update: %v", err)
	}
	if err := e.Calendars.Save(ctx, got, 1); err != nil {
		t.Fatal(err)
	}
	final, _ := e.Calendars.Get(ctx, "WH1")
	if final.Version() != 2 || len(final.Closures()) != 1 || final.Closures()[0].Door != "D1" || final.Settings().NoShowGraceMinutes != 90 {
		t.Fatalf("final calendar = %+v", final.Snapshot())
	}
}

func runDoors(t *testing.T, f Factory) {
	ctx := context.Background()
	e := f(t)
	if d, err := e.Doors.Door(ctx, "D1"); d != nil || err != nil {
		t.Fatalf("unknown door = %v, %v", d, err)
	}
	if err := e.Doors.UpsertDoor(ctx, "D1", "zone-1", shared.FlowBoth); err != nil {
		t.Fatal(err)
	}
	if d, _ := e.Doors.Door(ctx, "D1"); d != nil {
		t.Fatalf("a door whose zone has not arrived is held pending: %+v", d)
	}
	if err := e.Doors.UpsertZone(ctx, "zone-1", "WH1"); err != nil {
		t.Fatal(err)
	}
	d, err := e.Doors.Door(ctx, "D1")
	if err != nil || d == nil || d.Site != "WH1" || d.Flow != shared.FlowBoth || d.Code != "D1" {
		t.Fatalf("door = %+v, %v", d, err)
	}
}

func runDoorUpdates(t *testing.T, f Factory) {
	ctx := context.Background()
	e := f(t)
	_ = e.Doors.UpsertZone(ctx, "zone-1", "WH1")
	_ = e.Doors.UpsertDoor(ctx, "D1", "zone-1", shared.FlowBoth)
	_ = e.Doors.UpsertDoor(ctx, "D1", "zone-1", shared.FlowInbound)
	d, _ := e.Doors.Door(ctx, "D1")
	if d.Flow != shared.FlowInbound {
		t.Fatalf("upsert must replace the flow: %+v", d)
	}
	_ = e.Doors.UpsertZone(ctx, "zone-1", "WH2")
	if d, _ = e.Doors.Door(ctx, "D1"); d.Site != "WH2" {
		t.Fatalf("upsert must replace the site: %+v", d)
	}
	if err := e.Doors.RemoveDoor(ctx, "D1"); err != nil {
		t.Fatal(err)
	}
	if d, _ = e.Doors.Door(ctx, "D1"); d != nil {
		t.Fatalf("removed door still known: %+v", d)
	}
	if err := e.Doors.RemoveDoor(ctx, "D1"); err != nil {
		t.Fatalf("removing an unknown door is a no-op: %v", err)
	}
}

func runAsns(t *testing.T, f Factory) {
	ctx := context.Background()
	e := f(t)
	_ = e.Asns.Register(ctx, "ASN-1", base)
	_ = e.Asns.Register(ctx, "ASN-2", base)
	_ = e.Asns.Cancel(ctx, "ASN-2")
	_ = e.Asns.Cancel(ctx, "ASN-3") // cancel before register inserts CANCELLED
	_ = e.Asns.Register(ctx, "ASN-3", base)
	_ = e.Asns.Register(ctx, "ASN-1", base) // idempotent
	got, err := e.Asns.Statuses(ctx, []string{"ASN-1", "ASN-2", "ASN-3", "ASN-4"})
	if err != nil || got["ASN-1"] != dockappointment.AsnRegistered || got["ASN-2"] != dockappointment.AsnCancelledStatus ||
		got["ASN-3"] != dockappointment.AsnCancelledStatus || got["ASN-4"] != dockappointment.AsnUnknown {
		t.Fatalf("statuses = %v, %v", got, err)
	}
	if got, err = e.Asns.Statuses(ctx, nil); err != nil || len(got) != 0 {
		t.Fatalf("statuses of nothing = %v, %v", got, err)
	}
}

func runCutoffs(t *testing.T, f Factory) {
	ctx := context.Background()
	e := f(t)
	one := []repository.Cutoff{{ID: "WH1-1500", LocalTime: "15:00", DaysOfWeek: []string{"Mon", "Tue"}, ShipMethod: "ground"}, {ID: "WH1-1800", LocalTime: "18:00", ShipMethod: "same-day"}}
	if err := e.Cutoffs.Replace(ctx, "WH1", "America/Sao_Paulo", one); err != nil {
		t.Fatal(err)
	}
	if err := e.Cutoffs.Replace(ctx, "WH2", "UTC", []repository.Cutoff{{ID: "WH2-1"}}); err != nil {
		t.Fatal(err)
	}
	got, err := e.Cutoffs.Known(ctx, "WH1", []string{"WH1-1500", "WH1-1800", "WH2-1", "nope"})
	if err != nil || len(got) != 2 || !got["WH1-1500"] || !got["WH1-1800"] {
		t.Fatalf("known = %v, %v", got, err)
	}
	if err := e.Cutoffs.Replace(ctx, "WH1", "UTC", one[:1]); err != nil {
		t.Fatal(err)
	}
	if got, _ = e.Cutoffs.Known(ctx, "WH1", []string{"WH1-1500", "WH1-1800"}); len(got) != 1 || !got["WH1-1500"] {
		t.Fatalf("replace must drop the cutoffs of the old schedule: %v", got)
	}
	if err := e.Cutoffs.Replace(ctx, "WH1", "UTC", nil); err != nil {
		t.Fatal(err)
	}
	if got, _ = e.Cutoffs.Known(ctx, "WH1", []string{"WH1-1500"}); len(got) != 0 {
		t.Fatalf("an empty schedule empties the site: %v", got)
	}
	if got, err = e.Cutoffs.Known(ctx, "WH1", nil); err != nil || len(got) != 0 {
		t.Fatalf("known of nothing: %v %v", got, err)
	}
}
