package http

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/claudioed/dock-scheduling/internal/application/views"
)

// The wire shapes of the carrier registry in apis/openapi.yaml (camelCase).

// carrierRequest is the body of PUT /carriers/{scac}: the whole identity the
// registry holds besides the SCAC.
type carrierRequest struct {
	Name  string  `json:"name"`
	USDOT *string `json:"usdot"`
	MC    *string `json:"mc"`
}

// optionalText is a field of a merge patch: absent (set false), null (set,
// nil) or a string. Absent keeps the value, null clears an optional one.
type optionalText struct {
	set   bool
	value *string
}

// UnmarshalJSON records that the field was present, and its value or null.
func (o *optionalText) UnmarshalJSON(raw []byte) error {
	o.set = true
	if string(raw) == "null" {
		o.value = nil
		return nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return fmt.Errorf("must be a string or null: %w", err)
	}
	o.value = &s
	return nil
}

// pointer maps a patch field to the use case's convention: nil keeps the
// value, a pointer to "" clears it (null and "" both clear).
func (o optionalText) pointer() *string {
	if !o.set {
		return nil
	}
	if o.value == nil {
		empty := ""
		return &empty
	}
	return o.value
}

// carrierPatch is the body of PATCH /carriers/{scac} (a JSON merge patch of the identity).
type carrierPatch struct {
	Name  optionalText `json:"name"`
	USDOT optionalText `json:"usdot"`
	MC    optionalText `json:"mc"`
}

type suspensionRequest struct {
	Reason string `json:"reason"`
}

type carrierBody struct {
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

type carrierPageBody struct {
	Items      []carrierBody `json:"items"`
	NextCursor *string       `json:"nextCursor,omitempty"`
}

func toCarrierBody(v views.CarrierView) carrierBody {
	return carrierBody{
		SCAC: v.SCAC, Name: v.Name, USDOT: orNil(v.USDOT), MC: orNil(v.MC), Status: v.Status,
		SuspensionReason: orNil(v.SuspensionReason), SuspendedAt: utcPtr(v.SuspendedAt), Version: v.Version,
		CreatedAt: v.CreatedAt.UTC(), UpdatedAt: v.UpdatedAt.UTC(),
	}
}
