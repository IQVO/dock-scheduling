package kafka

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	ce "github.com/cloudevents/sdk-go/v2/event"

	"github.com/claudioed/dock-scheduling/internal/adapters/kafka/cloudevents"
	"github.com/claudioed/dock-scheduling/internal/analytics/adherence"
)

// AnalyticsTopic is this service's OWN analytics topic: the projector reads
// what the transactional outbox published there (ADR 0004, ADR 0006). The
// outbound adapter owns the same name; cmd/projector's test pins them equal.
const AnalyticsTopic = "warehouse.dock-scheduling.analytics"

// EnvAnalyticsGroup names the projector's consumer group. Like every group of
// this service it comes from the environment (the chart sets it), never from a
// literal.
const EnvAnalyticsGroup = "ANALYTICS_CONSUMER_GROUP"

// AnalyticsGroupFromEnv reads the projector's group; a blank one is a boot
// error naming the variable.
func AnalyticsGroupFromEnv(getenv func(string) string) (string, error) {
	group := strings.TrimSpace(getenv(EnvAnalyticsGroup))
	if group == "" {
		return "", fmt.Errorf("%w: set %s", ErrNoConsumerGroup, EnvAnalyticsGroup)
	}
	return group, nil
}

// entityAppointment is the `entity` segment of the type of every appointment
// event (type = com.warehouse.wms.dock-scheduling.dockappointment.<Event>).
const entityAppointment = "dockappointment"

// analyticsData is the union of the analytics payload fields the projection
// reads (hand-mirrored from apis/asyncapi.yaml, never imported from the domain).
// Unknown fields are ignored. Times stay text so that a malformed one is a
// poison message with a named field, not an opaque decode error.
type analyticsData struct {
	AppointmentID string  `json:"appointment_id"`
	SiteID        string  `json:"site_id"`
	CarrierSCAC   *string `json:"carrier_scac"`
	WindowStart   string  `json:"window_start"`
	WindowEnd     string  `json:"window_end"`
	Version       int64   `json:"version"`
	ArrivalStatus string  `json:"arrival_status"`
	CheckedInAt   string  `json:"checked_in_at"`
	CompletedAt   string  `json:"completed_at"`
	Reason        string  `json:"reason"`
}

// AnalyticsHandlers maps the FULL CloudEvents type of every folded event to a
// handler that decodes it, validates it and hands it to the projection. Every
// other type of the topic is ignored (the consumer finds no handler). The
// projection's Apply is idempotent per CloudEvents id, so the at-least-once
// delivery is safe.
//
// Outcomes: a payload that does not decode, an invalid event or one the store
// deterministically refuses is poison (dead-lettered, committed past); any
// other Apply error is transient and the SAME message is retried.
func AnalyticsHandlers(p adherence.Projection, logger *slog.Logger) map[string]HandlerFunc {
	logger = defaultLogger(logger)
	handlers := make(map[string]HandlerFunc, len(adherence.Kinds()))
	for _, kind := range adherence.Kinds() {
		handlers[cloudevents.Type(entityAppointment, string(kind))] = analyticsHandler(kind, p, logger)
	}
	return handlers
}

func analyticsHandler(kind adherence.Kind, p adherence.Projection, logger *slog.Logger) HandlerFunc {
	return func(ctx context.Context, e ce.Event) error {
		var d analyticsData
		if err := e.DataAs(&d); err != nil {
			return &deadLetterError{cause: invalidf("payload of %s does not decode: %v", e.Type(), err)}
		}
		event, err := toAdherenceEvent(kind, e, d)
		if err != nil {
			return &deadLetterError{cause: err}
		}
		applied, err := p.Apply(ctx, event)
		if err != nil {
			if isDeterministic(err) {
				return &deadLetterError{cause: err}
			}
			return fmt.Errorf("project %s %s: %w", kind, e.ID(), err)
		}
		logger.DebugContext(ctx, "analytics event projected", "type", e.Type(), "event_id", e.ID(), "subject", e.Subject(), "applied", applied)
		return nil
	}
}

// toAdherenceEvent maps a decoded CloudEvent of a folded kind to an
// adherence.Event and validates it. Every error is deterministic (the same
// bytes can never pass).
func toAdherenceEvent(kind adherence.Kind, e ce.Event, d analyticsData) (adherence.Event, error) {
	if d.AppointmentID != e.Subject() {
		return adherence.Event{}, invalidf("%s %s: appointment_id %q does not match subject %q", kind, e.ID(), d.AppointmentID, e.Subject())
	}
	out := adherence.Event{
		Kind: kind, EventID: e.ID(), At: e.Time().UTC(), AppointmentID: d.AppointmentID, SiteID: d.SiteID,
		Version: d.Version, ArrivalStatus: adherence.ArrivalStatus(d.ArrivalStatus), Reason: d.Reason,
	}
	if d.CarrierSCAC != nil {
		out.CarrierSCAC = *d.CarrierSCAC
	}
	var err error
	switch kind {
	case adherence.Booked, adherence.Rescheduled:
		if out.WindowStart, err = whenRequired(e, "window_start", d.WindowStart); err != nil {
			return out, err
		}
		out.WindowEnd, err = whenRequired(e, "window_end", d.WindowEnd)
	case adherence.NoShow:
		if strings.TrimSpace(d.WindowEnd) != "" {
			out.WindowEnd, err = when(e, "window_end", d.WindowEnd)
		}
	case adherence.CheckedIn:
		out.CheckedInAt, err = when(e, "checked_in_at", d.CheckedInAt)
	case adherence.Completed:
		out.CompletedAt, err = when(e, "completed_at", d.CompletedAt)
	case adherence.Cancelled:
	}
	if err != nil {
		return out, err
	}
	return out, out.Validate()
}

// NewAnalyticsConsumer reads AnalyticsTopic under groupID
// (ANALYTICS_CONSUMER_GROUP, never a literal) and folds every event into the
// projection. It does not dial.
func NewAnalyticsConsumer(tr Transport, groupID string, p adherence.Projection) (*Consumer, error) {
	return NewAnalyticsConsumerForTopic(tr, AnalyticsTopic, groupID, p)
}

// NewAnalyticsConsumerForTopic is NewAnalyticsConsumer on an explicit topic
// (the integration tests' unique topics).
func NewAnalyticsConsumerForTopic(tr Transport, topic, groupID string, p adherence.Projection) (*Consumer, error) {
	return newConsumer("analytics consumer", EnvAnalyticsGroup, tr, topic, groupID, AnalyticsHandlers(p, tr.Logger))
}
