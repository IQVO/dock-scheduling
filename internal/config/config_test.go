package config

import (
	"io"
	"log/slog"
	"testing"
	"time"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestShutdownDrain(t *testing.T) {
	for raw, want := range map[string]time.Duration{
		"": DefaultShutdownDur, "0": 0, "0s": 0, "250ms": 250 * time.Millisecond, "2": 2 * time.Second, "1.5": 1500 * time.Millisecond,
		"-1": DefaultShutdownDur, "soon": DefaultShutdownDur,
	} {
		if got := ShutdownDrain(raw, quiet()); got != want {
			t.Errorf("ShutdownDrain(%q) = %v, want %v", raw, got, want)
		}
	}
}

func TestOutboxKnobsFallBackToTheDefaults(t *testing.T) {
	if got := RelayInterval("x", quiet()); got != time.Second {
		t.Errorf("RelayInterval(x) = %v", got)
	}
	if got := BatchSize("0", quiet()); got != 100 {
		t.Errorf("BatchSize(0) = %d", got)
	}
	if got := MaxAttempts("-3", quiet()); got != 20 {
		t.Errorf("MaxAttempts(-3) = %d", got)
	}
	if got := MaxAttempts("0", quiet()); got != 0 {
		t.Errorf("MaxAttempts(0) = %d, want unlimited", got)
	}
}
