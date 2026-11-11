package postgres

import (
	"fmt"
	"time"

	"github.com/claudioed/dock-scheduling/internal/application/usecases"
	"github.com/claudioed/dock-scheduling/internal/domain/carrier"
)

// initiatedByData is the optional `initiated_by` object of a carrier fact
// (fleet agent-authority rule): which agent surface made the change and the
// attribution labels its caller supplied. It is omitted for REST writes.
type initiatedByData struct {
	Via        string `json:"via"`
	ProposalID string `json:"proposal_id,omitempty"`
	ApprovedBy string `json:"approved_by,omitempty"`
	AgentID    string `json:"agent_id,omitempty"`
}

// initiatedBy maps the initiator carried by the unit of work to its wire
// object, or nil when the change was not agent-initiated.
func initiatedBy(i usecases.Initiator) *initiatedByData {
	if i.IsZero() {
		return nil
	}
	return &initiatedByData{Via: string(i.Via()), ProposalID: i.ProposalID(), ApprovedBy: i.ApprovedBy(), AgentID: i.AgentID()}
}

// carrierFactData renders one carrier fact's data in the pinned wire shape of
// ADR 0012 (snake_case keys, RFC 3339 UTC times, nullable fields present and
// null). The domain raises exactly the three facts below.
func carrierFactData(e carrier.Event, by *initiatedByData) (any, error) {
	switch f := e.(type) {
	case carrier.RegisteredEvent:
		return carrierRegisteredData{
			SCAC: f.SCAC(), Name: f.Name, USDOT: nullable(f.USDOT), MC: nullable(f.MC), Status: string(f.Status),
			Reason: nullable(f.Reason), SuspendedAt: nullableTime(f.SuspendedAt), Version: f.CarrierVersion(), InitiatedBy: by,
		}, nil
	case carrier.SuspendedEvent:
		return carrierSuspendedData{SCAC: f.SCAC(), Reason: f.Reason, SuspendedAt: f.SuspendedAt.UTC(), Version: f.CarrierVersion(), InitiatedBy: by}, nil
	case carrier.ReinstatedEvent:
		return carrierReinstatedData{SCAC: f.SCAC(), ReinstatedAt: f.ReinstatedAt.UTC(), Version: f.CarrierVersion(), InitiatedBy: by}, nil
	default:
		return nil, fmt.Errorf("unknown carrier fact %T: no wire shape is declared for it", e)
	}
}

type carrierRegisteredData struct {
	SCAC        string           `json:"scac"`
	Name        string           `json:"name"`
	USDOT       *string          `json:"usdot"`
	MC          *string          `json:"mc"`
	Status      string           `json:"status"`
	Reason      *string          `json:"reason"`
	SuspendedAt *time.Time       `json:"suspended_at"`
	Version     int64            `json:"version"`
	InitiatedBy *initiatedByData `json:"initiated_by,omitempty"`
}

type carrierSuspendedData struct {
	SCAC        string           `json:"scac"`
	Reason      string           `json:"reason"`
	SuspendedAt time.Time        `json:"suspended_at"`
	Version     int64            `json:"version"`
	InitiatedBy *initiatedByData `json:"initiated_by,omitempty"`
}

type carrierReinstatedData struct {
	SCAC         string           `json:"scac"`
	ReinstatedAt time.Time        `json:"reinstated_at"`
	Version      int64            `json:"version"`
	InitiatedBy  *initiatedByData `json:"initiated_by,omitempty"`
}
