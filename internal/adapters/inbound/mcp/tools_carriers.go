package mcp

import (
	"context"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/claudioed/dock-scheduling/internal/application/usecases"
	"github.com/claudioed/dock-scheduling/internal/application/views"
	"github.com/claudioed/dock-scheduling/internal/domain/carrier"
)

// The carrier registry tools (ADR 0012): two reads (L1) and three writes (L3).
// A carrier is identified by its SCAC; the registry is opt-in (a booking that
// names an unregistered SCAC is accepted), a SUSPENDED carrier cannot book.

func (d Deps) registerCarrierTools(server *mcp.Server) {
	readOnly := true
	additive := false
	idempotent := true

	addTool(server, &mcp.Tool{
		Name: "list_carriers",
		Description: "Page through the carrier registry ordered by SCAC (at most 500 per page; follow nextCursor). Optionally only carriers of one status: ACTIVE or SUSPENDED. " +
			"A carrier holds its SCAC (Standard Carrier Alpha Code, 2..4 upper-case letters), name, optional USDOT number and MC or MX operating authority number, status and, when suspended, the reason.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: readOnly},
		Meta:        authorityMeta(authorityL1),
	}, d.listCarriers)

	addTool(server, &mcp.Tool{
		Name: "get_carrier",
		Description: "Read one registered carrier by SCAC: name, usdot, mc, status (ACTIVE or SUSPENDED), suspensionReason and suspendedAt when suspended, and the version to pass as expectedVersion on a change. " +
			"A SCAC that is not registered is carrier-not-found: that does NOT mean it cannot book, an unregistered SCAC is accepted as before.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: readOnly},
		Meta:        authorityMeta(authorityL1),
	}, d.getCarrier)

	addTool(server, &mcp.Tool{
		Name: "register_carrier",
		Description: "Put a carrier in the registry. Idempotent by SCAC: a new SCAC is created ACTIVE (created=true); a SCAC already registered with exactly this data answers it unchanged (created=false); " +
			"the same SCAC with other data is refused (carrier-already-registered), changing a registered carrier is not offered here. Refused with invalid-scac, invalid-carrier-name, invalid-usdot or invalid-mc. Needs an idempotencyKey.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &additive, IdempotentHint: idempotent},
		Meta:        authorityMeta(authorityL3),
	}, d.registerCarrier)

	addTool(server, &mcp.Tool{
		Name: "suspend_carrier",
		Description: "Suspend a registered carrier with a mandatory reason: from now on a booking or reschedule that names its SCAC is refused (carrier-suspended). Appointments the carrier already holds are NOT cancelled; " +
			"a person decides about them. Refused when the carrier is already suspended (carrier-already-suspended). Pass expectedVersion (from get_carrier) to guard against a concurrent change. Needs an idempotencyKey.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &additive, IdempotentHint: idempotent},
		Meta:        authorityMeta(authorityL3),
	}, d.suspendCarrier)

	addTool(server, &mcp.Tool{
		Name: "reinstate_carrier",
		Description: "Reinstate a suspended carrier so it can book again. Refused when the carrier is not suspended (carrier-not-suspended). " +
			"Pass expectedVersion (from get_carrier) to guard against a concurrent change. Needs an idempotencyKey.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &additive, IdempotentHint: idempotent},
		Meta:        authorityMeta(authorityL3),
	}, d.reinstateCarrier)
}

type carrierOut struct {
	SCAC             string     `json:"scac"`
	Name             string     `json:"name"`
	USDOT            *string    `json:"usdot"`
	MC               *string    `json:"mc"`
	Status           string     `json:"status"`
	SuspensionReason *string    `json:"suspensionReason"`
	SuspendedAt      *time.Time `json:"suspendedAt"`
	Version          int64      `json:"version"`
	CreatedAt        time.Time  `json:"createdAt"`
	UpdatedAt        time.Time  `json:"updatedAt"`
}

type carrierPageOut struct {
	Items      []carrierOut `json:"items"`
	NextCursor *string      `json:"nextCursor"`
}

// carrierWriteOut is the answer of every carrier write tool.
type carrierWriteOut struct {
	Carrier carrierOut `json:"carrier"`
	// Created is register_carrier only: true when THIS call created the carrier.
	Created     bool           `json:"created"`
	InitiatedBy initiatedByOut `json:"initiatedBy"`
	// Replayed is true when the answer is the stored one of an earlier call
	// with the same idempotencyKey: nothing was written this time.
	Replayed bool `json:"replayed"`
}

func (o *carrierWriteOut) attribute(by initiatedByOut) { o.InitiatedBy = by }
func (o *carrierWriteOut) markReplayed(r bool)         { o.Replayed = r }
func (o *carrierWriteOut) logFields() []any {
	return []any{"scac", o.Carrier.SCAC, "status", o.Carrier.Status}
}

func toCarrier(v views.CarrierView) carrierOut {
	return carrierOut{
		SCAC: v.SCAC, Name: v.Name, USDOT: orNil(v.USDOT), MC: orNil(v.MC), Status: v.Status, SuspensionReason: orNil(v.SuspensionReason),
		SuspendedAt: utcPtr(v.SuspendedAt), Version: v.Version, CreatedAt: v.CreatedAt.UTC(), UpdatedAt: v.UpdatedAt.UTC(),
	}
}

// --- list_carriers --------------------------------------------------------------

type listCarriersInput struct {
	Status string `json:"status,omitempty" jsonschema:"optional status to narrow to: ACTIVE or SUSPENDED"`
	Limit  int    `json:"limit,omitempty" jsonschema:"optional page size 1..500 (default 100)"`
	Cursor string `json:"cursor,omitempty" jsonschema:"optional nextCursor of the previous page"`
}

func (d Deps) listCarriers(ctx context.Context, in listCarriersInput) (carrierPageOut, error) {
	var status carrier.Status
	if in.Status != "" {
		s, err := carrier.ParseStatus(in.Status)
		if err != nil {
			return carrierPageOut{}, argError("status must be ACTIVE or SUSPENDED, got %q", in.Status)
		}
		status = s
	}
	if err := checkLimit(in.Limit); err != nil {
		return carrierPageOut{}, err
	}
	page, err := d.Service.ListCarriers(ctx, status, in.Limit, in.Cursor)
	if err != nil {
		return carrierPageOut{}, err
	}
	out := carrierPageOut{Items: make([]carrierOut, 0, len(page.Items)), NextCursor: orNil(page.NextCursor)}
	for _, v := range page.Items {
		out.Items = append(out.Items, toCarrier(v))
	}
	return out, nil
}

// --- get_carrier ----------------------------------------------------------------

type getCarrierInput struct {
	SCAC string `json:"scac" jsonschema:"the carrier's SCAC, 2..4 upper-case letters"`
}

func (d Deps) getCarrier(ctx context.Context, in getCarrierInput) (carrierOut, error) {
	if err := requireText("scac", in.SCAC); err != nil {
		return carrierOut{}, err
	}
	v, err := d.Service.GetCarrier(ctx, in.SCAC)
	if err != nil {
		return carrierOut{}, err
	}
	return toCarrier(v), nil
}

// --- register_carrier -----------------------------------------------------------

type registerCarrierInput struct {
	SCAC  string `json:"scac" jsonschema:"the carrier's SCAC, 2..4 upper-case letters (assigned by NMFTA)"`
	Name  string `json:"name" jsonschema:"carrier name, 1..100 characters"`
	USDOT string `json:"usdot,omitempty" jsonschema:"optional USDOT number, 1..8 digits"`
	MC    string `json:"mc,omitempty" jsonschema:"optional operating authority number, 1..8 digits with an optional MC or MX prefix (no prefix means MC; stored as MC<digits> or MX<digits>)"`
	attribution
}

func (in registerCarrierInput) command() (usecases.RegisterCarrierCommand, error) {
	if err := requireText("scac", in.SCAC); err != nil {
		return usecases.RegisterCarrierCommand{}, err
	}
	return usecases.RegisterCarrierCommand{SCAC: in.SCAC, Name: in.Name, USDOT: in.USDOT, MC: in.MC}, nil
}

func (d Deps) registerCarrier(ctx context.Context, in registerCarrierInput) (carrierWriteOut, error) {
	cmd, err := in.command()
	if err != nil {
		return carrierWriteOut{}, err
	}
	return writeTool[carrierWriteOut](ctx, d, "register_carrier", in.attribution, cmd, func(ctx context.Context) (carrierWriteOut, error) {
		res, err := d.Service.RegisterCarrier(ctx, cmd)
		return carrierWriteOut{Carrier: toCarrier(res.Carrier), Created: res.Created}, err
	})
}

// --- suspend_carrier ------------------------------------------------------------

type suspendCarrierInput struct {
	SCAC            string `json:"scac" jsonschema:"the SCAC of the registered carrier to suspend"`
	Reason          string `json:"reason" jsonschema:"why the carrier is suspended, 1..200 characters; shown to whoever is refused"`
	ExpectedVersion int64  `json:"expectedVersion,omitempty" jsonschema:"optional version from get_carrier; the change is refused (precondition-failed) if the carrier is at another version now"`
	attribution
}

func (in suspendCarrierInput) command() (usecases.SuspendCarrierCommand, error) {
	if err := requireText("scac", in.SCAC); err != nil {
		return usecases.SuspendCarrierCommand{}, err
	}
	if in.ExpectedVersion < 0 {
		return usecases.SuspendCarrierCommand{}, argError("expectedVersion must not be negative")
	}
	return usecases.SuspendCarrierCommand{SCAC: in.SCAC, Reason: in.Reason, ExpectedVersion: in.ExpectedVersion}, nil
}

func (d Deps) suspendCarrier(ctx context.Context, in suspendCarrierInput) (carrierWriteOut, error) {
	cmd, err := in.command()
	if err != nil {
		return carrierWriteOut{}, err
	}
	return writeTool[carrierWriteOut](ctx, d, "suspend_carrier", in.attribution, cmd, func(ctx context.Context) (carrierWriteOut, error) {
		v, err := d.Service.SuspendCarrier(ctx, cmd)
		return carrierWriteOut{Carrier: toCarrier(v)}, err
	})
}

// --- reinstate_carrier ----------------------------------------------------------

type reinstateCarrierInput struct {
	SCAC            string `json:"scac" jsonschema:"the SCAC of the suspended carrier to reinstate"`
	ExpectedVersion int64  `json:"expectedVersion,omitempty" jsonschema:"optional version from get_carrier; the change is refused (precondition-failed) if the carrier is at another version now"`
	attribution
}

func (in reinstateCarrierInput) command() (usecases.ReinstateCarrierCommand, error) {
	if err := requireText("scac", in.SCAC); err != nil {
		return usecases.ReinstateCarrierCommand{}, err
	}
	if in.ExpectedVersion < 0 {
		return usecases.ReinstateCarrierCommand{}, argError("expectedVersion must not be negative")
	}
	return usecases.ReinstateCarrierCommand{SCAC: in.SCAC, ExpectedVersion: in.ExpectedVersion}, nil
}

func (d Deps) reinstateCarrier(ctx context.Context, in reinstateCarrierInput) (carrierWriteOut, error) {
	cmd, err := in.command()
	if err != nil {
		return carrierWriteOut{}, err
	}
	return writeTool[carrierWriteOut](ctx, d, "reinstate_carrier", in.attribution, cmd, func(ctx context.Context) (carrierWriteOut, error) {
		v, err := d.Service.ReinstateCarrier(ctx, cmd)
		return carrierWriteOut{Carrier: toCarrier(v)}, err
	})
}
