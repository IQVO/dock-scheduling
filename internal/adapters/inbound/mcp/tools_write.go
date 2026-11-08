package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/claudioed/dock-scheduling/internal/application/usecases"
	"github.com/claudioed/dock-scheduling/internal/domain/dockappointment"
)

// attribution is what every write tool takes besides its business arguments:
// the idempotency key (REST's Idempotency-Key header) and the three optional
// attribution labels that become the event's `initiated_by`. The adapter, not
// the caller, stamps the surface: via is always mcp.
type attribution struct {
	IdempotencyKey string `json:"idempotencyKey" jsonschema:"a unique value per logical request, 1..128 printable ASCII characters; retry with the SAME key and arguments to get the stored answer instead of a second change, the same key with other arguments is refused (idempotency-key-reused)"`
	ProposalID     string `json:"proposalId,omitempty" jsonschema:"optional id of the proposal this change executes, recorded for attribution (max 128 characters)"`
	ApprovedBy     string `json:"approvedBy,omitempty" jsonschema:"optional id of the human who approved the proposal, recorded for attribution (max 128 characters)"`
	AgentID        string `json:"agentId,omitempty" jsonschema:"optional id of the calling agent, recorded for attribution (max 128 characters)"`
}

// write runs one state-changing tool with the cross-cutting rules of every
// agent write: key shape, initiator, REST-equivalent idempotency, audit log.
// args is what the fingerprint covers (the business arguments, not the key or
// the labels); run executes the use case with the initiator in its context.
func (d Deps) write(ctx context.Context, tool string, att attribution, args any, run func(ctx context.Context) (writeOut, error)) (writeOut, error) {
	if err := usecases.ValidateIdempotencyKey(att.IdempotencyKey); err != nil {
		return writeOut{}, err
	}
	initiator, err := usecases.NewInitiator(usecases.ViaMCP, att.ProposalID, att.ApprovedBy, att.AgentID)
	if err != nil {
		return writeOut{}, err
	}
	fp, err := usecases.Fingerprint(tool, args)
	if err != nil {
		return writeOut{}, err
	}
	trace.SpanFromContext(ctx).SetAttributes(
		attribute.String("initiated_by.via", string(initiator.Via())),
		attribute.String("initiated_by.agent_id", initiator.AgentID()),
		attribute.String("initiated_by.proposal_id", initiator.ProposalID()),
		attribute.String("initiated_by.approved_by", initiator.ApprovedBy()),
	)
	outcome, err := d.Idempotency.Do(ctx, att.IdempotencyKey, fp, func(ctx context.Context) ([]byte, error) {
		out, err := run(usecases.WithInitiator(ctx, initiator))
		if err != nil {
			return nil, err
		}
		out.InitiatedBy = toInitiatedBy(initiator)
		return json.Marshal(out)
	}, classify)
	if err != nil {
		return writeOut{}, err
	}
	var out writeOut
	if err := json.Unmarshal(outcome.Body, &out); err != nil {
		return writeOut{}, errors.New("the stored answer of the idempotency key is unreadable")
	}
	out.Replayed = outcome.Replayed
	trace.SpanFromContext(ctx).SetAttributes(attribute.Bool("idempotency.replayed", out.Replayed))
	slog.InfoContext(ctx, "mcp write", "tool", tool, "appointment_id", out.Appointment.AppointmentID, "state", out.Appointment.State,
		"via", string(initiator.Via()), "agent_id", initiator.AgentID(), "proposal_id", initiator.ProposalID(),
		"approved_by", initiator.ApprovedBy(), "replayed", out.Replayed)
	return out, nil
}

// --- book_appointment -----------------------------------------------------------

type bookInput struct {
	Direction          string   `json:"direction" jsonschema:"INBOUND or OUTBOUND"`
	DoorCode           string   `json:"doorCode" jsonschema:"the dock door to book; its flow must allow the direction"`
	Carrier            string   `json:"carrier" jsonschema:"carrier name, 1..100 characters"`
	CarrierSCAC        string   `json:"carrierScac,omitempty" jsonschema:"optional carrier SCAC, 2..4 upper-case letters"`
	WindowStart        string   `json:"windowStart" jsonschema:"RFC 3339 start of the window; on the site's slot granularity and not in the past"`
	WindowEnd          string   `json:"windowEnd" jsonschema:"RFC 3339 end of the window (exclusive), after the start and at most the site's max window"`
	ASNNumbers         []string `json:"asnNumbers,omitempty" jsonschema:"INBOUND: the ASN numbers the visit delivers (at least one, each known and not cancelled, each on no other active appointment); optional for OUTBOUND"`
	CPTID              string   `json:"cptId,omitempty" jsonschema:"optional for INBOUND; OUTBOUND: a cutoff id known for the site (alternative to shipmentRefs)"`
	ShipmentRefs       []string `json:"shipmentRefs,omitempty" jsonschema:"OUTBOUND: the shipments the visit picks up (alternative to cptId); optional for INBOUND"`
	ExpectedSealNumber string   `json:"expectedSealNumber,omitempty" jsonschema:"optional seal number the carrier declares; the yard compares it at the gate"`
	attribution
}

func (in bookInput) command() (usecases.BookCommand, error) {
	start, err := requireTime("windowStart", in.WindowStart)
	if err != nil {
		return usecases.BookCommand{}, err
	}
	end, err := requireTime("windowEnd", in.WindowEnd)
	if err != nil {
		return usecases.BookCommand{}, err
	}
	return usecases.BookCommand{
		DoorCode: in.DoorCode, Direction: in.Direction, Carrier: in.Carrier, CarrierSCAC: in.CarrierSCAC,
		WindowStart: start, WindowEnd: end, ASNNumbers: in.ASNNumbers, CPTID: in.CPTID, ShipmentRefs: in.ShipmentRefs,
		ExpectedSealNumber: in.ExpectedSealNumber,
	}, nil
}

func (d Deps) bookAppointment(ctx context.Context, in bookInput) (writeOut, error) {
	cmd, err := in.command()
	if err != nil {
		return writeOut{}, err
	}
	return d.write(ctx, "book_appointment", in.attribution, cmd, func(ctx context.Context) (writeOut, error) {
		v, err := d.Service.BookAppointment(ctx, cmd)
		return writeOut{Appointment: toAppointment(v)}, err
	})
}

// --- reschedule_appointment -----------------------------------------------------

type rescheduleInput struct {
	AppointmentID   string `json:"appointmentId" jsonschema:"the BOOKED appointment to move, appt-<uuid>"`
	DoorCode        string `json:"doorCode,omitempty" jsonschema:"optional new door; omit to keep the current door"`
	WindowStart     string `json:"windowStart" jsonschema:"RFC 3339 start of the new window"`
	WindowEnd       string `json:"windowEnd" jsonschema:"RFC 3339 end of the new window (exclusive)"`
	ExpectedVersion int64  `json:"expectedVersion,omitempty" jsonschema:"optional version from get_appointment; the change is refused (precondition-failed) if the appointment is at another version now"`
	attribution
}

func (in rescheduleInput) command() (usecases.RescheduleCommand, error) {
	if err := requireText("appointmentId", in.AppointmentID); err != nil {
		return usecases.RescheduleCommand{}, err
	}
	if in.ExpectedVersion < 0 {
		return usecases.RescheduleCommand{}, argError("expectedVersion must not be negative")
	}
	start, err := requireTime("windowStart", in.WindowStart)
	if err != nil {
		return usecases.RescheduleCommand{}, err
	}
	end, err := requireTime("windowEnd", in.WindowEnd)
	if err != nil {
		return usecases.RescheduleCommand{}, err
	}
	return usecases.RescheduleCommand{AppointmentID: in.AppointmentID, DoorCode: in.DoorCode, WindowStart: start, WindowEnd: end, ExpectedVersion: in.ExpectedVersion}, nil
}

func (d Deps) rescheduleAppointment(ctx context.Context, in rescheduleInput) (writeOut, error) {
	cmd, err := in.command()
	if err != nil {
		return writeOut{}, err
	}
	return d.write(ctx, "reschedule_appointment", in.attribution, cmd, func(ctx context.Context) (writeOut, error) {
		v, err := d.Service.RescheduleAppointment(ctx, cmd)
		return writeOut{Appointment: toAppointment(v)}, err
	})
}

// --- cancel_appointment ---------------------------------------------------------

type cancelInput struct {
	AppointmentID   string `json:"appointmentId" jsonschema:"the BOOKED or CHECKED_IN appointment to cancel, appt-<uuid>"`
	Reason          string `json:"reason" jsonschema:"CARRIER_REQUEST or SITE_REQUEST"`
	ExpectedVersion int64  `json:"expectedVersion,omitempty" jsonschema:"optional version from get_appointment; the change is refused (precondition-failed) if the appointment is at another version now"`
	attribution
}

func (in cancelInput) command() (usecases.CancelCommand, error) {
	if err := requireText("appointmentId", in.AppointmentID); err != nil {
		return usecases.CancelCommand{}, err
	}
	if in.ExpectedVersion < 0 {
		return usecases.CancelCommand{}, argError("expectedVersion must not be negative")
	}
	// Over REST (and here) only the two caller-chosen reasons are accepted;
	// ASN_CANCELLED and TURNED_AWAY belong to the policies.
	if in.Reason != string(dockappointment.CarrierRequest) && in.Reason != string(dockappointment.SiteRequest) {
		return usecases.CancelCommand{}, dockappointment.ErrInvalidReason
	}
	return usecases.CancelCommand{AppointmentID: in.AppointmentID, Reason: in.Reason, ExpectedVersion: in.ExpectedVersion}, nil
}

func (d Deps) cancelAppointment(ctx context.Context, in cancelInput) (writeOut, error) {
	cmd, err := in.command()
	if err != nil {
		return writeOut{}, err
	}
	return d.write(ctx, "cancel_appointment", in.attribution, cmd, func(ctx context.Context) (writeOut, error) {
		v, err := d.Service.CancelAppointment(ctx, cmd)
		return writeOut{Appointment: toAppointment(v)}, err
	})
}

// --- report_eta -----------------------------------------------------------------

// etaSourceMCP is recorded as the ETA source when the caller names none.
const etaSourceMCP = "mcp"

type reportEtaInput struct {
	AppointmentID string `json:"appointmentId" jsonschema:"the BOOKED appointment the carrier is on the way to, appt-<uuid>"`
	EtaAt         string `json:"etaAt" jsonschema:"RFC 3339 estimated arrival"`
	Source        string `json:"source,omitempty" jsonschema:"optional label of where the ETA came from, at most 64 characters (default mcp)"`
	attribution
}

func (in reportEtaInput) command() (usecases.EtaCommand, error) {
	if err := requireText("appointmentId", in.AppointmentID); err != nil {
		return usecases.EtaCommand{}, err
	}
	eta, err := requireTime("etaAt", in.EtaAt)
	if err != nil {
		return usecases.EtaCommand{}, err
	}
	source := in.Source
	if source == "" {
		source = etaSourceMCP
	}
	return usecases.EtaCommand{AppointmentID: in.AppointmentID, EtaAt: eta, Source: source}, nil
}

func (d Deps) reportEta(ctx context.Context, in reportEtaInput) (writeOut, error) {
	cmd, err := in.command()
	if err != nil {
		return writeOut{}, err
	}
	return d.write(ctx, "report_eta", in.attribution, cmd, func(ctx context.Context) (writeOut, error) {
		res, err := d.Service.ReportEta(ctx, cmd)
		return writeOut{Appointment: toAppointment(res.Appointment), AtRisk: res.AtRisk}, err
	})
}
