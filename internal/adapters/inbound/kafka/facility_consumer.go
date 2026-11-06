package kafka

import (
	"context"
	"log/slog"
	"strings"

	ce "github.com/cloudevents/sdk-go/v2/event"
)

// FacilityTopic is facility-layout's integration topic.
const FacilityTopic = "warehouse.facility.events"

// The facility-layout types the consumer acts on, byte-identical to its
// AsyncAPI (origin/develop:apis/asyncapi.yaml).
const (
	TypeZoneRegistered             = "com.warehouse.wms.facility-layout.zone.ZoneRegistered"
	TypeLocationSlotRegistered     = "com.warehouse.wms.facility-layout.locationslot.LocationSlotRegistered"
	TypeLocationSlotDecommissioned = "com.warehouse.wms.facility-layout.locationslot.LocationSlotDecommissioned"
)

// roleDock is facility-layout's LocationRole of a dock door. An absent role is
// Storage, so only an explicit Dock makes a slot bookable.
const roleDock = "Dock"

// FacilityUseCases feed the DoorView and the zone -> site join. The X5 rule
// (a slot that arrives before its zone is held pending and cannot be booked)
// lives in the DoorView's join, so the consumer needs no ordering of its own.
// *usecases.Service implements it.
type FacilityUseCases interface {
	ConsumeZoneRegistered(ctx context.Context, eventID, zoneID, siteCode string) error
	ConsumeSlotRegistered(ctx context.Context, eventID, code, zoneID, role, dockFlow string) error
	ConsumeSlotDecommissioned(ctx context.Context, eventID, code string) error
}

// facility-layout payloads are camelCase and also carry eventName, eventType
// and occurredAt. Decoding accepts the snake_case spelling too (tolerant
// reader) and ignores every other field.
type (
	zoneData struct {
		ZoneID        string `json:"zoneId"`
		ZoneIDSnake   string `json:"zone_id"`
		SiteCode      string `json:"siteCode"`
		SiteCodeSnake string `json:"site_code"`
	}
	slotData struct {
		LocationCode      string `json:"locationCode"`
		LocationCodeSnake string `json:"location_code"`
		ZoneID            string `json:"zoneId"`
		ZoneIDSnake       string `json:"zone_id"`
		Role              string `json:"role"`
		DockFlow          string `json:"dockFlow"`
		DockFlowSnake     string `json:"dock_flow"`
	}
)

func firstNonBlank(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// FacilityHandlers maps facility-layout's consumed types to the use cases.
func FacilityHandlers(uc FacilityUseCases, logger *slog.Logger) map[string]HandlerFunc {
	return map[string]HandlerFunc{
		TypeZoneRegistered: typed(logger, func(ctx context.Context, e ce.Event, d zoneData) error {
			zone, site := firstNonBlank(d.ZoneID, d.ZoneIDSnake), firstNonBlank(d.SiteCode, d.SiteCodeSnake)
			if err := require(e.Type(), map[string]string{"zoneId": zone, "siteCode": site}); err != nil {
				return err
			}
			return uc.ConsumeZoneRegistered(ctx, e.ID(), zone, site)
		}),
		TypeLocationSlotRegistered: typed(logger, func(ctx context.Context, e ce.Event, d slotData) error {
			code, zone := firstNonBlank(d.LocationCode, d.LocationCodeSnake), firstNonBlank(d.ZoneID, d.ZoneIDSnake)
			role, flow := strings.TrimSpace(d.Role), firstNonBlank(d.DockFlow, d.DockFlowSnake)
			if strings.EqualFold(role, roleDock) {
				role = roleDock
				if err := require(e.Type(), map[string]string{"locationCode": code, "zoneId": zone}); err != nil {
					return err
				}
			} else if err := require(e.Type(), map[string]string{"locationCode": code}); err != nil {
				return err
			}
			return uc.ConsumeSlotRegistered(ctx, e.ID(), code, zone, role, flow)
		}),
		TypeLocationSlotDecommissioned: typed(logger, func(ctx context.Context, e ce.Event, d slotData) error {
			code := firstNonBlank(d.LocationCode, d.LocationCodeSnake)
			if err := require(e.Type(), map[string]string{"locationCode": code}); err != nil {
				return err
			}
			return uc.ConsumeSlotDecommissioned(ctx, e.ID(), code)
		}),
	}
}

// NewFacilityConsumer reads FacilityTopic under groupID
// (FACILITY_CONSUMER_GROUP, never a literal). It does not dial.
func NewFacilityConsumer(tr Transport, groupID string, uc FacilityUseCases) (*Consumer, error) {
	return NewFacilityConsumerForTopic(tr, FacilityTopic, groupID, uc)
}

// NewFacilityConsumerForTopic is NewFacilityConsumer on an explicit topic.
func NewFacilityConsumerForTopic(tr Transport, topic, groupID string, uc FacilityUseCases) (*Consumer, error) {
	return newConsumer("facility consumer", EnvFacilityGroup, tr, topic, groupID, FacilityHandlers(uc, tr.Logger))
}
