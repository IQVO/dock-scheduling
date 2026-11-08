package http

import "testing"

// The sanitizer must strip every CR and LF so a wire value cannot forge a log line.
func TestLogReplacerStripsLineBreaks(t *testing.T) {
	got := logReplacer.Replace("GET\r\nX-Forged: 1\n")
	if got != "GETX-Forged: 1" {
		t.Fatalf("logReplacer left line breaks: %q", got)
	}
}
