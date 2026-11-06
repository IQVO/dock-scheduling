package outbox_test

import (
	"errors"
	"testing"

	"github.com/claudioed/dock-scheduling/internal/application/outbox"
)

func TestSendFailureErrorWrapsTheSendError(t *testing.T) {
	cause := errors.New("broker down")
	f := &outbox.SendFailureError{RowID: 7, Attempts: 2, Err: cause}
	if f.Error() != "broker down" || !errors.Is(f, cause) || f.Unwrap() != cause {
		t.Fatalf("SendFailureError = %v", f)
	}
}
