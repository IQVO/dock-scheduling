package kafka

import (
	"context"
	"log/slog"
	"time"

	ce "github.com/cloudevents/sdk-go/v2/event"

	"github.com/claudioed/dock-scheduling/internal/application/usecases"
)

// The inbound-receiving appointment types the legacy importer acts on
// (strangler stage A), byte-identical to its AsyncAPI. DockAppointmentCheckedIn
// and ...Completed move an imported appointment; the NoShow/Rescheduled events
// do not exist on the legacy side.
const (
	TypeLegacyBooked    = "com.warehouse.wms.inbound-receiving.dockappointment.DockAppointmentBooked"
	TypeLegacyCheckedIn = "com.warehouse.wms.inbound-receiving.dockappointment.DockAppointmentCheckedIn"
	TypeLegacyCancelled = "com.warehouse.wms.inbound-receiving.dockappointment.DockAppointmentCancelled"
	TypeLegacyCompleted = "com.warehouse.wms.inbound-receiving.dockappointment.DockAppointmentCompleted"
)

// LegacyUseCases are the LegacyImport policy (D-2): each legacy appointment is
// imported once, its later events applied to the imported copy only.
// *usecases.Service implements it.
type LegacyUseCases interface {
	ConsumeLegacyBooked(ctx context.Context, eventID string, b usecases.LegacyBooking) error
	ConsumeLegacyCheckedIn(ctx context.Context, eventID, appointmentID string, at time.Time) error
	ConsumeLegacyCancelled(ctx context.Context, eventID, appointmentID string) error
	ConsumeLegacyCompleted(ctx context.Context, eventID, appointmentID string, at time.Time) error
}

// The use case service implements every consumer's port (compile-time proof).
var _ UseCases = (*usecases.Service)(nil)

// The legacy payloads are snake_case. The legacy `reason` of a cancellation is
// free text and is ignored: an imported cancellation is CARRIER_REQUEST (D-2).
type (
	legacyBookedData struct {
		AppointmentID string   `json:"appointment_id"`
		DoorCode      string   `json:"door_code"`
		Carrier       string   `json:"carrier"`
		WindowStart   string   `json:"window_start"`
		WindowEnd     string   `json:"window_end"`
		ASNNumbers    []string `json:"asn_numbers"`
	}
	legacyCheckedInData struct {
		AppointmentID string `json:"appointment_id"`
		CheckedInAt   string `json:"checked_in_at"`
	}
	legacyCancelledData struct {
		AppointmentID string `json:"appointment_id"`
	}
	legacyCompletedData struct {
		AppointmentID string `json:"appointment_id"`
		CompletedAt   string `json:"completed_at"`
	}
)

// LegacyHandlers maps inbound-receiving's appointment types to the importer.
// Events of one appointment share one key, hence one partition, so a Booked
// precedes its CheckedIn/Cancelled/Completed; a later event for an appointment
// that was never imported is ignored and counted by the use case.
func LegacyHandlers(uc LegacyUseCases, logger *slog.Logger) map[string]HandlerFunc {
	return map[string]HandlerFunc{
		TypeLegacyBooked: typed(logger, func(ctx context.Context, e ce.Event, d legacyBookedData) error {
			if err := require(e.Type(), map[string]string{"appointment_id": d.AppointmentID, "door_code": d.DoorCode}); err != nil {
				return err
			}
			start, err := whenRequired(e, "window_start", d.WindowStart)
			if err != nil {
				return err
			}
			end, err := whenRequired(e, "window_end", d.WindowEnd)
			if err != nil {
				return err
			}
			return uc.ConsumeLegacyBooked(ctx, e.ID(), usecases.LegacyBooking{
				AppointmentID: d.AppointmentID, DoorCode: d.DoorCode, Carrier: d.Carrier,
				WindowStart: start, WindowEnd: end, ASNNumbers: d.ASNNumbers,
			})
		}),
		TypeLegacyCheckedIn: typed(logger, func(ctx context.Context, e ce.Event, d legacyCheckedInData) error {
			if err := require(e.Type(), map[string]string{"appointment_id": d.AppointmentID}); err != nil {
				return err
			}
			at, err := when(e, "checked_in_at", d.CheckedInAt)
			if err != nil {
				return err
			}
			return uc.ConsumeLegacyCheckedIn(ctx, e.ID(), d.AppointmentID, at)
		}),
		TypeLegacyCancelled: typed(logger, func(ctx context.Context, e ce.Event, d legacyCancelledData) error {
			if err := require(e.Type(), map[string]string{"appointment_id": d.AppointmentID}); err != nil {
				return err
			}
			return uc.ConsumeLegacyCancelled(ctx, e.ID(), d.AppointmentID)
		}),
		TypeLegacyCompleted: typed(logger, func(ctx context.Context, e ce.Event, d legacyCompletedData) error {
			if err := require(e.Type(), map[string]string{"appointment_id": d.AppointmentID}); err != nil {
				return err
			}
			at, err := when(e, "completed_at", d.CompletedAt)
			if err != nil {
				return err
			}
			return uc.ConsumeLegacyCompleted(ctx, e.ID(), d.AppointmentID, at)
		}),
	}
}

// NewLegacyImportConsumer reads InboundReceivingTopic under groupID
// (DOCK_LEGACY_IMPORT_CONSUMER_GROUP). The importer is OFF when the group is
// unset: a blank groupID returns (nil, nil) and builds no reader, unlike every
// other consumer whose blank group is a boot error. Start skips a nil consumer.
func NewLegacyImportConsumer(tr Transport, groupID string, uc LegacyUseCases) (*Consumer, error) {
	return NewLegacyImportConsumerForTopic(tr, InboundReceivingTopic, groupID, uc)
}

// NewLegacyImportConsumerForTopic is NewLegacyImportConsumer on an explicit topic.
func NewLegacyImportConsumerForTopic(tr Transport, topic, groupID string, uc LegacyUseCases) (*Consumer, error) {
	if groupID == "" {
		return nil, nil // documented: the importer is OFF when its group env is unset
	}
	return newConsumer("legacy import consumer", EnvLegacyImportGroup, tr, topic, groupID, LegacyHandlers(uc, tr.Logger))
}
