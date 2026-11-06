package main

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/claudioed/dock-scheduling/internal/config"
)

func quiet() *slog.Logger { return slog.New(slog.NewJSONHandler(io.Discard, nil)) }

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestLoadConfigDefaults(t *testing.T) {
	c, err := loadConfig(env(map[string]string{"ANALYTICS_DATABASE_URL": "postgres://u@h/db"}), quiet())
	if err != nil {
		t.Fatal(err)
	}
	want := reportsConfig{analyticsURL: "postgres://u@h/db", httpAddr: ":8092", drainDelay: config.DefaultShutdownDur}
	if c != want {
		t.Fatalf("config = %+v, want %+v", c, want)
	}
}

func TestLoadConfigEnvironmentOverrides(t *testing.T) {
	c, err := loadConfig(env(map[string]string{
		"ANALYTICS_DATABASE_URL": "postgres://u@h/db", "HTTP_ADDR": ":9999", "LOG_LEVEL": "debug", "SHUTDOWN_DRAIN_DELAY": "0",
	}), quiet())
	if err != nil {
		t.Fatal(err)
	}
	if c.httpAddr != ":9999" || c.logLevel != "debug" || c.drainDelay != 0 {
		t.Fatalf("config = %+v", c)
	}
}

// A missing database is a boot error naming the variable, before anything is
// dialled; the reports binary needs no Kafka.
func TestLoadConfigNamesTheMissingAnalyticsDatabase(t *testing.T) {
	for _, raw := range []string{"", "  "} {
		_, err := loadConfig(env(map[string]string{"ANALYTICS_DATABASE_URL": raw, "KAFKA_BROKERS": "k:9092"}), quiet())
		if err == nil || !strings.Contains(err.Error(), "ANALYTICS_DATABASE_URL") {
			t.Fatalf("blank %q: err = %v", raw, err)
		}
	}
}

func TestRunFailsFastOnMissingConfiguration(t *testing.T) {
	err := run(context.Background(), env(nil), nil)
	if err == nil || !strings.Contains(err.Error(), "ANALYTICS_DATABASE_URL") {
		t.Fatalf("run = %v", err)
	}
}

func TestRunRefusesAMalformedDatabaseURL(t *testing.T) {
	done := make(chan error, 1)
	go func() {
		done <- run(context.Background(), env(map[string]string{"ANALYTICS_DATABASE_URL": "://not a dsn"}), nil)
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "ANALYTICS_DATABASE_URL") {
			t.Fatalf("run = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a malformed DSN must fail the boot, not retry")
	}
}

func TestNewLoggerLevelMapping(t *testing.T) {
	ctx := context.Background()
	for level, enabledDebug := range map[string]bool{"debug": true, "info": false, "": false, "nonsense": false} {
		if got := newLogger(level).Enabled(ctx, slog.LevelDebug); got != enabledDebug {
			t.Errorf("level %q: debug enabled = %v, want %v", level, got, enabledDebug)
		}
	}
	if newLogger("warn").Enabled(ctx, slog.LevelInfo) || !newLogger("warning").Enabled(ctx, slog.LevelWarn) || newLogger("error").Enabled(ctx, slog.LevelWarn) {
		t.Error("warn/error level mapping is wrong")
	}
}
