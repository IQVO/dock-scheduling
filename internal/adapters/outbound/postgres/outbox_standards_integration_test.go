//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/claudioed/dock-scheduling/internal/adapters/outbound/postgres"
	"github.com/claudioed/dock-scheduling/internal/domain/dockappointment"
	"github.com/claudioed/dock-scheduling/internal/domain/shared"
	"github.com/claudioed/dock-scheduling/internal/testing/pgtest"
)

// ADR 0013 against a real Postgres: the outbox publisher resolves the GS1 GLN of
// the fact's door from the doors copy, in the transaction the row is written in,
// and the standard fields of the mapping table land in the persisted payload.

const (
	glnDoor   = "4006381333931"
	glnDoorB  = "4012345000009"
	dockWith  = "D-GLN"
	dockNone  = "D-NOGLN"
	dockMoved = "D-GLN-2"
)

func hours(n int) time.Duration { return time.Duration(n) * time.Hour }

var standardsRules = dockappointment.Rules{
	MaxWindow: dockappointment.HardMaxWindow, Granularity: 15 * time.Minute, ArrivalTolerance: 15 * time.Minute, NoShowGrace: time.Hour,
}

func seedDoors(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx, doors := context.Background(), postgres.NewDoorStore(pool)
	for _, err := range []error{
		doors.UpsertZone(ctx, "zone-1", "WH1"),
		doors.UpsertDoor(ctx, dockWith, "zone-1", shared.FlowBoth, glnDoor),
		doors.UpsertDoor(ctx, dockMoved, "zone-1", shared.FlowBoth, glnDoorB),
		doors.UpsertDoor(ctx, dockNone, "zone-1", shared.FlowBoth, ""),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
}

// bookedOutbound books an OUTBOUND appointment on door.
func bookedOutbound(t *testing.T, n int, door string, hoursFromBase int) (*dockappointment.DockAppointment, []dockappointment.Event) {
	t.Helper()
	start := base.Add(hours(hoursFromBase))
	a, events, err := dockappointment.Book(dockappointment.BookParams{
		ID: apptID(n), SiteID: "WH1", DoorCode: door, Direction: "OUTBOUND", Carrier: "ACME", CarrierSCAC: "ACMF",
		WindowStart: start, WindowEnd: start.Add(hours(1)), CPTID: "WH1-1500", Now: base.Add(-hours(24)),
	})
	if err != nil {
		t.Fatalf("Book outbound: %v", err)
	}
	return a, events
}

// outboxData returns the `data` of every outbox row, in order.
func outboxData(t *testing.T, pool *pgxpool.Pool) []map[string]any {
	t.Helper()
	rows, err := pool.Query(context.Background(), `SELECT payload FROM outbox ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			t.Fatal(err)
		}
		var env struct {
			Data map[string]any `json:"data"`
		}
		if err := json.Unmarshal(payload, &env); err != nil {
			t.Fatal(err)
		}
		out = append(out, env.Data)
	}
	return out
}

func TestThePublisherWritesTheStandardFieldsAndTheDoorGLN(t *testing.T) {
	pool := pgtest.NewPool(t)
	seedDoors(t, pool)
	pub, ctx := postgres.NewOutboxPublisher(pool), context.Background()

	_, inbound := booked(t, 1, dockWith, 0)          // INBOUND on a door with a GLN
	_, outbound := bookedOutbound(t, 2, dockNone, 2) // OUTBOUND on a door with none
	_, unknown := booked(t, 3, "D-UNKNOWN", 4)       // a door this copy has never seen
	for _, events := range [][]dockappointment.Event{inbound, outbound, unknown} {
		if err := pub.PublishAppointmentEvents(ctx, events...); err != nil {
			t.Fatal(err)
		}
	}
	data := outboxData(t, pool)
	if len(data) != 3 {
		t.Fatalf("%d outbox rows, want 3", len(data))
	}
	if data[0]["x12_status"] != "AB" || data[0]["read_point_gln"] != glnDoor {
		t.Errorf("inbound at a door with a GLN: %v", data[0])
	}
	if data[1]["x12_status"] != "AA" {
		t.Errorf("outbound: %v", data[1])
	}
	for i, d := range data[1:] {
		if _, present := d["read_point_gln"]; present {
			t.Errorf("row %d: a door without a GLN (or unknown) must omit read_point_gln, got %v", i+1, d["read_point_gln"])
		}
		if _, present := d["cbv_biz_step"]; present {
			t.Errorf("row %d: a booking has no CBV business step, got %v", i+1, d["cbv_biz_step"])
		}
	}
	if data[2]["x12_status"] != "AB" {
		t.Errorf("an unknown door still gets the status: %v", data[2])
	}
}

func TestAFactThatWasNotMappedCarriesOnlyTheReadPoint(t *testing.T) {
	pool := pgtest.NewPool(t)
	seedDoors(t, pool)
	a, events := booked(t, 1, dockWith, 0)
	cancel, err := a.Cancel(dockappointment.CarrierRequest, base.Add(-hours(1)))
	if err != nil {
		t.Fatal(err)
	}
	if err := postgres.NewOutboxPublisher(pool).PublishAppointmentEvents(context.Background(), append(events, cancel...)...); err != nil {
		t.Fatal(err)
	}
	cancelled := outboxData(t, pool)[1]
	if cancelled["read_point_gln"] != glnDoor {
		t.Errorf("the cancellation's read point is its door: %v", cancelled)
	}
	for _, f := range []string{"x12_status", "cbv_biz_step"} {
		if _, present := cancelled[f]; present {
			t.Errorf("a cancellation has no honest %s, got %v", f, cancelled[f])
		}
	}
}

func TestAReschedulesReadPointIsTheNewDoorAndACheckInIsAnArrival(t *testing.T) {
	pool := pgtest.NewPool(t)
	seedDoors(t, pool)
	a, events := bookedOutbound(t, 1, dockWith, 0)
	moved, err := a.Reschedule(dockMoved, base.Add(hours(1)), base.Add(hours(2)), standardsRules, base.Add(-hours(1)))
	if err != nil {
		t.Fatal(err)
	}
	arrived, err := a.RecordArrival(base.Add(hours(1)), "visit-1", standardsRules, base)
	if err != nil {
		t.Fatal(err)
	}
	all := append(append(events, moved...), arrived...)
	if err := postgres.NewOutboxPublisher(pool).PublishAppointmentEvents(context.Background(), all...); err != nil {
		t.Fatal(err)
	}
	data := outboxData(t, pool)
	if data[1]["x12_status"] != "AA" || data[1]["read_point_gln"] != glnDoorB {
		t.Errorf("reschedule to the second door: %v", data[1])
	}
	if data[2]["x12_status"] != "X3" || data[2]["cbv_biz_step"] != "urn:epcglobal:cbv:bizstep:arriving" || data[2]["read_point_gln"] != glnDoorB {
		t.Errorf("an outbound check-in at the second door: %v", data[2])
	}
}

// The GLN is read in the transaction that writes the row: a door registered in
// the same unit of work is seen, and a rollback leaves nothing behind.
func TestTheGLNIsResolvedInTheSameTransaction(t *testing.T) {
	pool := pgtest.NewPool(t)
	pub, uow, doors := postgres.NewOutboxPublisher(pool), postgres.NewUnitOfWork(pool), postgres.NewDoorStore(pool)
	_, events := booked(t, 1, "D-FRESH", 0)
	burst := errors.New("burst")
	err := uow.Do(context.Background(), func(ctx context.Context) error {
		if err := doors.UpsertZone(ctx, "zone-1", "WH1"); err != nil {
			return err
		}
		if err := doors.UpsertDoor(ctx, "D-FRESH", "zone-1", shared.FlowBoth, glnDoor); err != nil {
			return err
		}
		if err := pub.PublishAppointmentEvents(ctx, events...); err != nil {
			return err
		}
		return burst
	})
	if !errors.Is(err, burst) || pgtest.Count(t, pool, "outbox") != 0 || pgtest.Count(t, pool, "doors") != 0 {
		t.Fatalf("a rolled-back unit of work leaves no door and no row (err %v)", err)
	}
	err = uow.Do(context.Background(), func(ctx context.Context) error {
		if err := doors.UpsertZone(ctx, "zone-1", "WH1"); err != nil {
			return err
		}
		if err := doors.UpsertDoor(ctx, "D-FRESH", "zone-1", shared.FlowBoth, glnDoor); err != nil {
			return err
		}
		return pub.PublishAppointmentEvents(ctx, events...)
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := outboxData(t, pool)[0]["read_point_gln"]; got != glnDoor {
		t.Fatalf("read_point_gln = %v, want the GLN registered in the same transaction", got)
	}
}

// The doors table keeps only well-formed GLNs, and a door can be re-registered
// without one (the known one is kept) or with another (it replaces it).
func TestTheDoorStoreKeepsGLNsAsFacilityLayoutRegistersThem(t *testing.T) {
	pool := pgtest.NewPool(t)
	ctx, doors := context.Background(), postgres.NewDoorStore(pool)
	if err := doors.UpsertZone(ctx, "zone-1", "WH1"); err != nil {
		t.Fatal(err)
	}
	if err := doors.UpsertDoor(ctx, "D1", "zone-1", shared.FlowBoth, "not-a-gln"); err == nil {
		t.Fatal("the column check must refuse a GLN that is not 13 digits")
	}
	if _, err := pool.Exec(ctx, `INSERT INTO doors (door_code, zone_id, dock_flow) VALUES ('D2', 'zone-1', 'Both')`); err != nil {
		t.Fatalf("a door with no GLN (the shape before migration 0004): %v", err)
	}
	if err := doors.UpsertDoor(ctx, "D2", "zone-1", shared.FlowInbound, glnDoor); err != nil {
		t.Fatal(err)
	}
	if err := doors.UpsertDoor(ctx, "D2", "zone-1", shared.FlowOutbound, ""); err != nil {
		t.Fatal(err)
	}
	d, err := doors.Door(ctx, "D2")
	if err != nil || d == nil || d.GLN != glnDoor || d.Flow != shared.FlowOutbound {
		t.Fatalf("door = %+v, %v; want the known GLN kept and the new flow applied", d, err)
	}
}
