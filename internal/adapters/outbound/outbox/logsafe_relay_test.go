package outbox_test

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	relay "github.com/claudioed/dock-scheduling/internal/adapters/outbound/outbox"
	"github.com/claudioed/dock-scheduling/internal/application/outbox"
)

// crlfFailingStore fails every drain on a row whose entity, event, aggregate
// id and send error carry CR/LF, already at the attempt cap.
type crlfFailingStore struct{ err error }

func (s crlfFailingStore) Drain(context.Context, int, int, func(context.Context, outbox.Row) error) (int, error) {
	return 0, s.err
}

// crlfNopHandler is never reached: the store fails before handing a row over.
type crlfNopHandler struct{}

func (crlfNopHandler) Handle(context.Context, outbox.Row) error { return nil }

// TestRelayLogsNoEventDerivedLineBreaks: the attempt-cap and pass-failed logs
// of the relay must carry no line break from the outbox row or the send error.
func TestRelayLogsNoEventDerivedLineBreaks(t *testing.T) {
	rec := newCRLFRecorder()
	store := crlfFailingStore{err: &outbox.SendFailureError{
		RowID: 1, Attempts: 3, Err: errors.New("broker refused" + crlfForged),
		Row: outbox.Row{ID: 1, Fact: outbox.Fact{
			EventID: "id" + crlfForged, Entity: "entity" + crlfForged,
			Event: "Evil" + crlfForged, AggregateID: "key" + crlfForged,
		}},
	}}
	r := relay.NewOutboxRelay(store, crlfNopHandler{}, slog.New(rec), relay.WithMaxAttempts(3), relay.WithInterval(time.Millisecond))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_ = r.Run(ctx)
	rec.assertNoLineBreaks(t, 2)
}
