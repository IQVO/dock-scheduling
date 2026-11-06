package kafka

import (
	"context"
	"log/slog"
	"time"

	ce "github.com/cloudevents/sdk-go/v2/event"
)

// YardTopic is yard-management's integration topic.
const YardTopic = "warehouse.yard-management.events"

// The yard-management types the consumer acts on, byte-identical to its
// AsyncAPI (origin/feature/service:apis/asyncapi.yaml). Every other type on
// the topic is ignored.
const (
	TypeVisitCheckedIn  = "com.warehouse.wms.yard-management.yardvisit.VisitCheckedIn"
	TypeVisitRejected   = "com.warehouse.wms.yard-management.yardvisit.VisitRejected"
	TypeTrailerDocked   = "com.warehouse.wms.yard-management.trailer.TrailerDocked"
	TypeTrailerUndocked = "com.warehouse.wms.yard-management.trailer.TrailerUndocked"
)

// YardUseCases are the policies the yard consumer drives (ArrivalFromGate,
// CancelOnVisitRejected, ActualDoorWins, CompleteOnDoorRelease, all under
// OutOfOrderYardFacts). *usecases.Service implements it.
type YardUseCases interface {
	ConsumeVisitCheckedIn(ctx context.Context, eventID, visitID, siteID, appointmentID string, arrivedAt time.Time) error
	ConsumeVisitRejected(ctx context.Context, eventID, siteID, appointmentID string) error
	ConsumeTrailerDocked(ctx context.Context, eventID, siteID, appointmentID, doorCode string, dockedAt time.Time) error
	ConsumeTrailerUndocked(ctx context.Context, eventID, siteID, appointmentID string, undockedAt time.Time) error
}

// The yard payloads are snake_case. Only the fields the policies rely on are
// declared; unknown fields (trailers, asn_numbers, purpose, version, ...) are
// ignored. appointment_id is nullable: a walk-in or a drop carries none, and
// JSON null decodes to "".
type (
	visitCheckedInData struct {
		VisitID       string `json:"visit_id"`
		SiteID        string `json:"site_id"`
		AppointmentID string `json:"appointment_id"`
		ArrivedAt     string `json:"arrived_at"`
	}
	visitRejectedData struct {
		VisitID       string `json:"visit_id"`
		SiteID        string `json:"site_id"`
		AppointmentID string `json:"appointment_id"`
	}
	trailerDockedData struct {
		TrailerID     string `json:"trailer_id"`
		SiteID        string `json:"site_id"`
		DoorCode      string `json:"door_code"`
		AppointmentID string `json:"appointment_id"`
		DockedAt      string `json:"docked_at"`
	}
	trailerUndockedData struct {
		TrailerID     string `json:"trailer_id"`
		SiteID        string `json:"site_id"`
		DoorCode      string `json:"door_code"`
		AppointmentID string `json:"appointment_id"`
		UndockedAt    string `json:"undocked_at"`
	}
)

// YardHandlers maps yard-management's consumed types to the use cases.
//
// The yard events carry an aggregate `version`, but the guard against stale or
// reordered facts is the DockAppointment state machine itself (the events are
// keyed by visit or trailer, never by appointment, so no per-appointment
// order exists to guard): a fact that cannot apply in the appointment's state
// is ignored and counted (OutOfOrderYardFacts).
func YardHandlers(uc YardUseCases, logger *slog.Logger) map[string]HandlerFunc {
	return map[string]HandlerFunc{
		TypeVisitCheckedIn: typed(logger, func(ctx context.Context, e ce.Event, d visitCheckedInData) error {
			if err := require(e.Type(), map[string]string{"visit_id": d.VisitID, "site_id": d.SiteID}); err != nil {
				return err
			}
			at, err := when(e, "arrived_at", d.ArrivedAt)
			if err != nil {
				return err
			}
			return uc.ConsumeVisitCheckedIn(ctx, e.ID(), d.VisitID, d.SiteID, d.AppointmentID, at)
		}),
		TypeVisitRejected: typed(logger, func(ctx context.Context, e ce.Event, d visitRejectedData) error {
			if err := require(e.Type(), map[string]string{"visit_id": d.VisitID, "site_id": d.SiteID}); err != nil {
				return err
			}
			return uc.ConsumeVisitRejected(ctx, e.ID(), d.SiteID, d.AppointmentID)
		}),
		TypeTrailerDocked: typed(logger, func(ctx context.Context, e ce.Event, d trailerDockedData) error {
			if err := require(e.Type(), map[string]string{"trailer_id": d.TrailerID, "site_id": d.SiteID, "door_code": d.DoorCode}); err != nil {
				return err
			}
			at, err := when(e, "docked_at", d.DockedAt)
			if err != nil {
				return err
			}
			return uc.ConsumeTrailerDocked(ctx, e.ID(), d.SiteID, d.AppointmentID, d.DoorCode, at)
		}),
		TypeTrailerUndocked: typed(logger, func(ctx context.Context, e ce.Event, d trailerUndockedData) error {
			if err := require(e.Type(), map[string]string{"trailer_id": d.TrailerID, "site_id": d.SiteID}); err != nil {
				return err
			}
			at, err := when(e, "undocked_at", d.UndockedAt)
			if err != nil {
				return err
			}
			return uc.ConsumeTrailerUndocked(ctx, e.ID(), d.SiteID, d.AppointmentID, at)
		}),
	}
}

// NewYardConsumer reads YardTopic under groupID (YARD_CONSUMER_GROUP, never a
// literal). It does not dial.
func NewYardConsumer(tr Transport, groupID string, uc YardUseCases) (*Consumer, error) {
	return NewYardConsumerForTopic(tr, YardTopic, groupID, uc)
}

// NewYardConsumerForTopic is NewYardConsumer on an explicit topic (the
// integration tests' unique topics).
func NewYardConsumerForTopic(tr Transport, topic, groupID string, uc YardUseCases) (*Consumer, error) {
	return newConsumer("yard consumer", EnvYardGroup, tr, topic, groupID, YardHandlers(uc, tr.Logger))
}
