package usecases

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

// lhForged is a request-derived value that tries to start a forged log line.
const lhForged = "x-1\r\nlevel=INFO msg=forged"

// lhRecorder keeps the string form of every attribute value of every record.
type lhRecorder struct{ values *[]string }

func (h lhRecorder) Enabled(context.Context, slog.Level) bool { return true }
func (h lhRecorder) WithAttrs([]slog.Attr) slog.Handler       { return h }
func (h lhRecorder) WithGroup(string) slog.Handler            { return h }
func (h lhRecorder) Handle(_ context.Context, r slog.Record) error {
	r.Attrs(func(a slog.Attr) bool {
		*h.values = append(*h.values, a.Value.String())
		return true
	})
	return nil
}

// lhRecordLogs routes the default logger to a recorder for the rest of the test.
func lhRecordLogs(t *testing.T) *[]string {
	t.Helper()
	var values []string
	prev := slog.Default()
	slog.SetDefault(slog.New(lhRecorder{values: &values}))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &values
}

// lhAssertOneLine fails when nothing was logged or an attribute holds CR or LF.
func lhAssertOneLine(t *testing.T, values []string, what string) {
	t.Helper()
	if len(values) == 0 {
		t.Fatalf("%s logged nothing", what)
	}
	for _, v := range values {
		if strings.ContainsAny(v, "\r\n") {
			t.Fatalf("%s: logged attribute %q holds a line break", what, v)
		}
	}
}

// lhFailingUoW fails every unit of work; the error text carries a line break.
type lhFailingUoW struct{}

func (lhFailingUoW) Do(context.Context, func(context.Context) error) error {
	return errors.New(lhForged)
}

// A failure to store a client-error answer of an agent write can carry request
// text (the key, the stored detail); the warning must keep it on one line.
func TestLogHygiene_UnstoredClientErrorIsLoggedOnOneLine(t *testing.T) {
	values := lhRecordLogs(t)
	ix := Idempotency{UoW: lhFailingUoW{}}
	ix.keepFailure(context.Background(), "key-1", "fp-1", &attempt{})
	lhAssertOneLine(t, *values, "an unstored client-error answer")
}
