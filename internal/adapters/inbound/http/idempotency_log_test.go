package http

import (
	"context"
	"errors"
	"testing"
)

// lhFailingUoW fails every unit of work; the error text carries a line break.
type lhFailingUoW struct{}

func (lhFailingUoW) Do(context.Context, func(context.Context) error) error {
	return errors.New(lhForged)
}

// A failure to store a client-error answer can carry request text (the key,
// the stored body); the warning must keep it on one line.
func TestLogHygiene_UnstoredClientErrorIsLoggedOnOneLine(t *testing.T) {
	values := lhRecordLogs(t)
	m := &idempotency{uow: lhFailingUoW{}}
	m.keep4xx(context.Background(), "key-1", "fp-1", &attempt{})
	lhAssertOneLine(t, *values, "an unstored client-error answer")
}
