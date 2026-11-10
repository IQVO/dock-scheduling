package kafka

import (
	"context"
	"log/slog"
	"time"

	ce "github.com/cloudevents/sdk-go/v2/event"
)

// InboundReceivingTopic is inbound-receiving's integration topic. The ASN
// consumer reads it.
const InboundReceivingTopic = "warehouse.inbound-receiving.events"

// The inbound-receiving ASN types the consumer acts on, byte-identical to its
// AsyncAPI (origin/develop:apis/asyncapi.yaml).
const (
	TypeASNRegistered = "com.warehouse.wms.inbound-receiving.asn.ASNRegistered"
	TypeASNCancelled  = "com.warehouse.wms.inbound-receiving.asn.ASNCancelled"
)

// AsnUseCases feed the AsnView and run CancelWhenAllAsnsCancelled.
// *usecases.Service implements it.
type AsnUseCases interface {
	ConsumeAsnRegistered(ctx context.Context, eventID, number string, expectedArrival time.Time) error
	ConsumeAsnCancelled(ctx context.Context, eventID, number string) error
}

// The inbound-receiving payloads are snake_case; `lines`, `supplier_ref` and
// `reason` are not relied on and are ignored.
type (
	asnRegisteredData struct {
		ASNNumber       string `json:"asn_number"`
		ExpectedArrival string `json:"expected_arrival"`
	}
	asnCancelledData struct {
		ASNNumber string `json:"asn_number"`
	}
)

// AsnHandlers maps inbound-receiving's ASN types to the use cases. The local
// copy is guarded by state, not by a version (the ASN events carry none): a
// CANCELLED ASN is sticky, so a late or replayed ASNRegistered never revives it.
func AsnHandlers(uc AsnUseCases, logger *slog.Logger) map[string]HandlerFunc {
	return map[string]HandlerFunc{
		TypeASNRegistered: typed(logger, func(ctx context.Context, e ce.Event, d asnRegisteredData) error {
			if err := require(e.Type(), map[string]string{"asn_number": d.ASNNumber}); err != nil {
				return err
			}
			arrival, err := when(e, "expected_arrival", d.ExpectedArrival)
			if err != nil {
				return err
			}
			return uc.ConsumeAsnRegistered(ctx, e.ID(), d.ASNNumber, arrival)
		}),
		TypeASNCancelled: typed(logger, func(ctx context.Context, e ce.Event, d asnCancelledData) error {
			if err := require(e.Type(), map[string]string{"asn_number": d.ASNNumber}); err != nil {
				return err
			}
			return uc.ConsumeAsnCancelled(ctx, e.ID(), d.ASNNumber)
		}),
	}
}

// NewAsnConsumer reads InboundReceivingTopic under groupID
// (ASN_CONSUMER_GROUP, never a literal). It does not dial.
func NewAsnConsumer(tr Transport, groupID string, uc AsnUseCases) (*Consumer, error) {
	return NewAsnConsumerForTopic(tr, InboundReceivingTopic, groupID, uc)
}

// NewAsnConsumerForTopic is NewAsnConsumer on an explicit topic.
func NewAsnConsumerForTopic(tr Transport, topic, groupID string, uc AsnUseCases) (*Consumer, error) {
	return newConsumer("asn consumer", EnvAsnGroup, tr, topic, groupID, AsnHandlers(uc, tr.Logger))
}
