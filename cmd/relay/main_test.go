package main

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	outboxrelay "github.com/claudioed/dock-scheduling/internal/adapters/outbound/outbox"
)

func quiet() *slog.Logger { return slog.New(slog.NewJSONHandler(io.Discard, nil)) }

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestLoadConfigNamesWhatIsMissing(t *testing.T) {
	_, err := loadConfig(env(nil), quiet())
	if err == nil || !strings.Contains(err.Error(), "DATABASE_URL") || !strings.Contains(err.Error(), "KAFKA_BROKERS") {
		t.Fatalf("both variables must be named, got %v", err)
	}
	if _, err = loadConfig(env(map[string]string{"DATABASE_URL": "postgres://x"}), quiet()); err == nil || !strings.Contains(err.Error(), "KAFKA_BROKERS") || strings.Contains(err.Error(), "DATABASE_URL") {
		t.Fatalf("only KAFKA_BROKERS is missing, got %v", err)
	}
	if _, err = loadConfig(env(map[string]string{"KAFKA_BROKERS": " "}), quiet()); err == nil {
		t.Fatal("blank brokers are missing brokers")
	}
}

func TestLoadConfigDefaultsAndOverrides(t *testing.T) {
	cfg, err := loadConfig(env(map[string]string{"DATABASE_URL": "postgres://x", "KAFKA_BROKERS": "k1:9092,k2:9092"}), quiet())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.databaseURL != "postgres://x" || cfg.brokers != "k1:9092,k2:9092" || cfg.interval != outboxrelay.DefaultInterval ||
		cfg.batchSize != outboxrelay.DefaultBatchSize || cfg.maxAttempts != outboxrelay.DefaultMaxAttempts {
		t.Fatalf("defaults = %+v", cfg)
	}
	cfg, err = loadConfig(env(map[string]string{
		"DATABASE_URL": "postgres://x", "KAFKA_BROKERS": "k:9092", "OUTBOX_RELAY_INTERVAL": "250ms", "OUTBOX_BATCH_SIZE": "7", "OUTBOX_MAX_ATTEMPTS": "0",
	}), quiet())
	if err != nil || cfg.interval != 250*time.Millisecond || cfg.batchSize != 7 || cfg.maxAttempts != 0 {
		t.Fatalf("overrides = %+v, %v", cfg, err)
	}
}

func TestParseRelayInterval(t *testing.T) {
	for raw, want := range map[string]time.Duration{
		"": outboxrelay.DefaultInterval, "2s": 2 * time.Second, "3": 3 * time.Second, "0.5": 500 * time.Millisecond,
		"nonsense": outboxrelay.DefaultInterval, "-1s": outboxrelay.DefaultInterval, "0": outboxrelay.DefaultInterval,
	} {
		if got := parseRelayInterval(raw, quiet()); got != want {
			t.Errorf("parseRelayInterval(%q) = %v, want %v", raw, got, want)
		}
	}
}

func TestParseBatchSize(t *testing.T) {
	for _, c := range []struct {
		raw  string
		want int
	}{{"", outboxrelay.DefaultBatchSize}, {"5", 5}, {" 9 ", 9}, {"0", outboxrelay.DefaultBatchSize}, {"-3", outboxrelay.DefaultBatchSize}, {"x", outboxrelay.DefaultBatchSize}} {
		if got := parseBatchSize(c.raw, quiet()); got != c.want {
			t.Errorf("parseBatchSize(%q) = %d, want %d", c.raw, got, c.want)
		}
	}
}

// 0 means unlimited; a typo or a negative number never removes the cap.
func TestOutboxMaxAttempts(t *testing.T) {
	for _, c := range []struct {
		raw  string
		want int
	}{{"", outboxrelay.DefaultMaxAttempts}, {"0", 0}, {"3", 3}, {" 7 ", 7}, {"-1", outboxrelay.DefaultMaxAttempts}, {"many", outboxrelay.DefaultMaxAttempts}} {
		if got := outboxMaxAttempts(c.raw, quiet()); got != c.want {
			t.Errorf("outboxMaxAttempts(%q) = %d, want %d", c.raw, got, c.want)
		}
	}
}

// A missing environment is a boot error before anything is dialled.
func TestRunFailsFastOnMissingConfiguration(t *testing.T) {
	err := run(context.Background(), env(nil), quiet())
	if err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Fatalf("run = %v", err)
	}
}
