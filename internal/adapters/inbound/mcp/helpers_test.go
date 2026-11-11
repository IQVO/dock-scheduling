package mcp_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	inboundmcp "github.com/claudioed/dock-scheduling/internal/adapters/inbound/mcp"
	"github.com/claudioed/dock-scheduling/internal/adapters/outbound/memory"
	"github.com/claudioed/dock-scheduling/internal/application/repository"
	"github.com/claudioed/dock-scheduling/internal/application/usecases"
	"github.com/claudioed/dock-scheduling/internal/application/views"
	"github.com/claudioed/dock-scheduling/internal/domain/shared"
)

// The fixture's fixed "now" (a Monday) and the half-hour marks used as windows.
var (
	t0   = time.Date(2030, 1, 7, 6, 0, 0, 0, time.UTC)
	at8  = time.Date(2030, 1, 7, 8, 0, 0, 0, time.UTC)
	at9  = at8.Add(time.Hour)
	at10 = at8.Add(2 * time.Hour)
	at11 = at8.Add(3 * time.Hour)
	at12 = at8.Add(4 * time.Hour)
)

type fixedClock struct{ now time.Time }

func (c *fixedClock) Now() time.Time { return c.now }

type seqIDs struct{ appt, closure int }

func (f *seqIDs) NewAppointmentID() string {
	f.appt++
	return fmt.Sprintf("appt-%08x-e89b-12d3-a456-426614174000", f.appt)
}

func (f *seqIDs) NewClosureID() string {
	f.closure++
	return fmt.Sprintf("clo-%08x-e89b-12d3-a456-426614174001", f.closure)
}

func apptN(n int) string { return fmt.Sprintf("appt-%08x-e89b-12d3-a456-426614174000", n) }

// recordingService wraps the real use cases and remembers the initiator each
// write ran under (what the outbox publisher reads back from the context).
type recordingService struct {
	*usecases.Service
	initiators []usecases.Initiator
}

func (r *recordingService) BookAppointment(ctx context.Context, cmd usecases.BookCommand) (views.AppointmentView, error) {
	r.initiators = append(r.initiators, usecases.InitiatorFrom(ctx))
	return r.Service.BookAppointment(ctx, cmd)
}

func (r *recordingService) RescheduleAppointment(ctx context.Context, cmd usecases.RescheduleCommand) (views.AppointmentView, error) {
	r.initiators = append(r.initiators, usecases.InitiatorFrom(ctx))
	return r.Service.RescheduleAppointment(ctx, cmd)
}

func (r *recordingService) CancelAppointment(ctx context.Context, cmd usecases.CancelCommand) (views.AppointmentView, error) {
	r.initiators = append(r.initiators, usecases.InitiatorFrom(ctx))
	return r.Service.CancelAppointment(ctx, cmd)
}

func (r *recordingService) ReportEta(ctx context.Context, cmd usecases.EtaCommand) (views.EtaResult, error) {
	r.initiators = append(r.initiators, usecases.InitiatorFrom(ctx))
	return r.Service.ReportEta(ctx, cmd)
}

func (r *recordingService) RegisterCarrier(ctx context.Context, cmd usecases.RegisterCarrierCommand) (views.CarrierResult, error) {
	r.initiators = append(r.initiators, usecases.InitiatorFrom(ctx))
	return r.Service.RegisterCarrier(ctx, cmd)
}

func (r *recordingService) SuspendCarrier(ctx context.Context, cmd usecases.SuspendCarrierCommand) (views.CarrierView, error) {
	r.initiators = append(r.initiators, usecases.InitiatorFrom(ctx))
	return r.Service.SuspendCarrier(ctx, cmd)
}

func (r *recordingService) ReinstateCarrier(ctx context.Context, cmd usecases.ReinstateCarrierCommand) (views.CarrierView, error) {
	r.initiators = append(r.initiators, usecases.InitiatorFrom(ctx))
	return r.Service.ReinstateCarrier(ctx, cmd)
}

// harness is a connected in-memory MCP client over the real server wired to the
// real use cases over the in-memory store, exactly as cmd/mcp wires them
// (minus Postgres; cmd/mcp's integration test runs the Postgres shape).
type harness struct {
	t       *testing.T
	session *sdk.ClientSession
	store   *memory.Store
	svc     *recordingService
	clock   *fixedClock
	deps    inboundmcp.Deps
}

func newDeps(store *memory.Store, clock *fixedClock) (*recordingService, inboundmcp.Deps) {
	svc := &recordingService{Service: usecases.New(usecases.Deps{
		Appointments: store.Appointments(), Calendars: store.Calendars(), Carriers: store.Carriers(), Doors: store.Doors(), Asns: store.Asns(), Cutoffs: store.Cutoffs(),
		Processed: store, Events: store, Metrics: &memory.Metrics{}, IDs: &seqIDs{}, Clock: clock, UoW: store,
	})}
	return svc, inboundmcp.Deps{
		Service:     svc,
		Idempotency: usecases.Idempotency{Store: store.Idempotency(), UoW: store, Now: clock.Now},
	}
}

func seed(t *testing.T, store *memory.Store) {
	t.Helper()
	ctx := context.Background()
	for _, err := range []error{
		store.Doors().UpsertZone(ctx, "zone-dock", "WH1"),
		store.Doors().UpsertDoor(ctx, "D-IN", "zone-dock", shared.FlowInbound),
		store.Doors().UpsertDoor(ctx, "D-OUT", "zone-dock", shared.FlowOutbound),
		store.Doors().UpsertDoor(ctx, "D-BOTH", "zone-dock", shared.FlowBoth),
		store.Asns().Register(ctx, "ASN-1", at8),
		store.Asns().Register(ctx, "ASN-2", at8),
		store.Asns().Register(ctx, "ASN-3", at8),
		store.Cutoffs().Replace(ctx, "WH1", "UTC", []repository.Cutoff{{ID: "WH1-1500"}}),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func connect(t *testing.T, deps inboundmcp.Deps) *sdk.ClientSession {
	t.Helper()
	ctx := context.Background()
	clientTransport, serverTransport := sdk.NewInMemoryTransports()
	serverSession, err := inboundmcp.NewServer(deps).Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })
	client := sdk.NewClient(&sdk.Implementation{Name: "test-client", Version: "0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	store := memory.NewStore()
	seed(t, store)
	clock := &fixedClock{now: t0}
	svc, deps := newDeps(store, clock)
	return &harness{t: t, session: connect(t, deps), store: store, svc: svc, clock: clock, deps: deps}
}

// call invokes a tool and returns the raw result.
func (h *harness) call(name string, args map[string]any) *sdk.CallToolResult {
	h.t.Helper()
	res, err := h.session.CallTool(context.Background(), &sdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		h.t.Fatalf("call %s: transport/protocol error: %v", name, err)
	}
	return res
}

// ok calls a tool, requires success and decodes the structured content into out.
func (h *harness) ok(name string, args map[string]any, out any) {
	h.t.Helper()
	res := h.call(name, args)
	if res.IsError {
		h.t.Fatalf("%s failed: %s", name, text(res))
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		h.t.Fatalf("%s: marshal structured content: %v", name, err)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		h.t.Fatalf("%s: decode structured content %s: %v", name, raw, err)
	}
}

// fail calls a tool, requires an isError result and returns its message.
func (h *harness) fail(name string, args map[string]any) string {
	h.t.Helper()
	res := h.call(name, args)
	if !res.IsError {
		h.t.Fatalf("%s succeeded, want an isError result: %v", name, res.StructuredContent)
	}
	return text(res)
}

func text(res *sdk.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*sdk.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

// merge returns base overlaid with extra.
func merge(base, extra map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

// bookArgs is a valid inbound booking of ASN-1 on D-IN, 08:00-09:00.
func bookArgs(key string, extra map[string]any) map[string]any {
	return merge(map[string]any{
		"direction": "INBOUND", "doorCode": "D-IN", "carrier": "ACME Freight", "carrierScac": "ACMF",
		"windowStart": at8.Format(time.RFC3339), "windowEnd": at9.Format(time.RFC3339), "asnNumbers": []string{"ASN-1"},
		"idempotencyKey": key,
	}, extra)
}

// seedBooking books through the use case (as REST would) and returns the id.
func (h *harness) seedBooking(cmd usecases.BookCommand) string {
	h.t.Helper()
	v, err := h.svc.Service.BookAppointment(context.Background(), cmd)
	if err != nil {
		h.t.Fatalf("seed booking: %v", err)
	}
	return v.AppointmentID
}

func (h *harness) eventNames() []string {
	var out []string
	for _, ev := range h.store.Published() {
		if n, ok := ev.(interface{ EventName() string }); ok {
			out = append(out, n.EventName())
		}
	}
	return out
}
