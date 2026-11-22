package http

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// recordingHandler keeps every attribute value of every record.
type recordingHandler struct{ values *[]string }

func (h recordingHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h recordingHandler) WithAttrs([]slog.Attr) slog.Handler       { return h }
func (h recordingHandler) WithGroup(string) slog.Handler            { return h }
func (h recordingHandler) Handle(_ context.Context, r slog.Record) error {
	r.Attrs(func(a slog.Attr) bool {
		*h.values = append(*h.values, a.Value.String())
		return true
	})
	return nil
}

// CodeQL go/log-injection: an internal error's text can carry request data, so the
// "request failed" record must not let it start a forged log line.
func TestAnInternalErrorIsLoggedOnOneLine(t *testing.T) {
	var values []string
	prev := slog.Default()
	slog.SetDefault(slog.New(recordingHandler{values: &values}))
	t.Cleanup(func() { slog.SetDefault(prev) })

	r := httptest.NewRequest(http.MethodPost, "/resources", nil)
	writeError(httptest.NewRecorder(), r, errors.New("resource r-1\r\nlevel=INFO msg=forged"))

	if len(values) == 0 {
		t.Fatal("writeError logged nothing for an internal error")
	}
	for _, v := range values {
		if strings.ContainsAny(v, "\r\n") {
			t.Fatalf("logged attribute %q holds a line break", v)
		}
	}
}
