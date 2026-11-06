package kafka

import (
	"context"
	"log/slog"

	ce "github.com/cloudevents/sdk-go/v2/event"

	"github.com/claudioed/dock-scheduling/internal/application/repository"
)

// ProcessPathTopic is process-path-management's integration topic.
const ProcessPathTopic = "warehouse.process-path-management.events"

// TypeCPTScheduleChanged is the one process-path-management type the consumer
// acts on, byte-identical to its AsyncAPI (a `wes` subdomain event, unlike the
// `wms` rest of the fleet this service consumes).
const TypeCPTScheduleChanged = "com.warehouse.wes.process-path-management.cptschedule.CPTScheduleChanged"

// CutoffUseCases store the CutoffView. *usecases.Service implements it.
type CutoffUseCases interface {
	ConsumeCptSchedule(ctx context.Context, eventID, siteID, timezone string, cutoffs []repository.Cutoff) error
}

// cptScheduleData mirrors the part of CPTScheduleChanged this service stores:
// the event is the WHOLE schedule of the site. `eligible_path_ids` and any
// other field are ignored. PickupFromCutoffs is phase 2 (D-3): v1 stores the
// view and creates no appointment from it.
type cptScheduleData struct {
	SiteID   string `json:"site_id"`
	Timezone string `json:"timezone"`
	Cutoffs  []struct {
		CPTID      string   `json:"cpt_id"`
		LocalTime  string   `json:"local_time"`
		DaysOfWeek []string `json:"days_of_week"`
		ShipMethod string   `json:"ship_method"`
	} `json:"cutoffs"`
}

// CutoffHandlers maps CPTScheduleChanged to the use case. The event carries no
// version: it is the whole schedule, keyed by site on one partition, and the
// processed_events claim makes a replay a no-op.
func CutoffHandlers(uc CutoffUseCases, logger *slog.Logger) map[string]HandlerFunc {
	return map[string]HandlerFunc{
		TypeCPTScheduleChanged: typed(logger, func(ctx context.Context, e ce.Event, d cptScheduleData) error {
			if err := require(e.Type(), map[string]string{"site_id": d.SiteID}); err != nil {
				return err
			}
			cutoffs := make([]repository.Cutoff, 0, len(d.Cutoffs))
			for i, c := range d.Cutoffs {
				if err := require(e.Type(), map[string]string{"cutoffs[].cpt_id": c.CPTID}); err != nil {
					return invalidf("cutoff %d: %v", i, err)
				}
				cutoffs = append(cutoffs, repository.Cutoff{ID: c.CPTID, LocalTime: c.LocalTime, DaysOfWeek: c.DaysOfWeek, ShipMethod: c.ShipMethod})
			}
			return uc.ConsumeCptSchedule(ctx, e.ID(), d.SiteID, d.Timezone, cutoffs)
		}),
	}
}

// NewCutoffConsumer reads ProcessPathTopic under groupID (CPT_CONSUMER_GROUP,
// never a literal). It does not dial.
func NewCutoffConsumer(tr Transport, groupID string, uc CutoffUseCases) (*Consumer, error) {
	return NewCutoffConsumerForTopic(tr, ProcessPathTopic, groupID, uc)
}

// NewCutoffConsumerForTopic is NewCutoffConsumer on an explicit topic.
func NewCutoffConsumerForTopic(tr Transport, topic, groupID string, uc CutoffUseCases) (*Consumer, error) {
	return newConsumer("cutoff consumer", EnvCptGroup, tr, topic, groupID, CutoffHandlers(uc, tr.Logger))
}
