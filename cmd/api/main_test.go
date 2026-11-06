package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestLoadConfigNamesEveryMissingVariable(t *testing.T) {
	_, err := loadConfig(env(nil), quiet())
	if err == nil || !strings.Contains(err.Error(), "DATABASE_URL") || !strings.Contains(err.Error(), "KAFKA_BROKERS") {
		t.Fatalf("a bare environment must name DATABASE_URL and KAFKA_BROKERS, got %v", err)
	}
	if _, err = loadConfig(env(map[string]string{"DATABASE_URL": "postgres://x"}), quiet()); err == nil ||
		!strings.Contains(err.Error(), "KAFKA_BROKERS") || strings.Contains(err.Error(), "DATABASE_URL") {
		t.Fatalf("only KAFKA_BROKERS is missing, got %v", err)
	}
	if _, err = loadConfig(env(map[string]string{"KAFKA_BROKERS": " , "}), quiet()); err == nil {
		t.Fatal("blank brokers are not brokers")
	}
	if _, err = loadConfig(env(map[string]string{"KAFKA_BROKERS": "k:9092"}), quiet()); err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Fatalf("only DATABASE_URL is missing, got %v", err)
	}
}

func TestLoadConfigDefaults(t *testing.T) {
	cfg, err := loadConfig(env(map[string]string{"DATABASE_URL": "postgres://x", "KAFKA_BROKERS": "k1:9092, k2:9092"}), quiet())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.httpAddr != ":8080" || cfg.migrationsURL != "postgres://x" || len(cfg.brokerList) != 2 || cfg.brokerList[1] != "k2:9092" {
		t.Fatalf("unexpected address, migrations URL or brokers: %+v", cfg)
	}
	if cfg.drainDelay != 5*time.Second || cfg.relayInterval != time.Second || cfg.batchSize != 100 || cfg.maxAttempts != 20 {
		t.Fatalf("unexpected tuning defaults: %+v", cfg)
	}
}

func TestLoadConfigOverrides(t *testing.T) {
	cfg, err := loadConfig(env(map[string]string{
		"DATABASE_URL": "postgres://pooled", "MIGRATIONS_DATABASE_URL": "postgres://direct", "KAFKA_BROKERS": "k:9092", "HTTP_ADDR": "127.0.0.1:9",
		"SHUTDOWN_DRAIN_DELAY": "0", "OUTBOX_RELAY_INTERVAL": "250ms", "OUTBOX_BATCH_SIZE": "7", "OUTBOX_MAX_ATTEMPTS": "0", "LOG_LEVEL": "debug",
	}), quiet())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.httpAddr != "127.0.0.1:9" || cfg.migrationsURL != "postgres://direct" || cfg.databaseURL != "postgres://pooled" || cfg.logLevel != "debug" {
		t.Fatalf("unexpected address, URLs or level: %+v", cfg)
	}
	if cfg.drainDelay != 0 || cfg.relayInterval != 250*time.Millisecond || cfg.batchSize != 7 || cfg.maxAttempts != 0 {
		t.Fatalf("unexpected tuning overrides: %+v", cfg)
	}
}

func TestRunFailsFastOnMissingConfiguration(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	t.Setenv("KAFKA_BROKERS", "")
	err := run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "DATABASE_URL") || !strings.Contains(err.Error(), "KAFKA_BROKERS") {
		t.Fatalf("run must refuse to boot naming both variables, got %v", err)
	}
}

func TestRunFailsOnAnUnreachableDatabase(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // the boot retry gives up at once instead of sleeping out its budget
	err := runWith(ctx, env(map[string]string{"DATABASE_URL": "postgres://nobody@127.0.0.1:1/none?sslmode=disable", "KAFKA_BROKERS": "127.0.0.1:1"}), nil)
	if err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Fatalf("an unreachable database is a boot error naming DATABASE_URL, got %v", err)
	}
}

func TestReadinessNamesTheFailingDependencies(t *testing.T) {
	g := newReadiness(func(context.Context) error { return errors.New("no postgres") }, []string{"127.0.0.1:1"})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	failing := g.Failing(ctx)
	if len(failing) != 2 || failing[0] != "postgres" || failing[1] != "kafka" {
		t.Fatalf("failing = %v, want [postgres kafka]", failing)
	}
}

func TestNewLoggerLevels(t *testing.T) {
	for level, want := range map[string]slog.Level{
		"debug": slog.LevelDebug, "DEBUG": slog.LevelDebug, "warn": slog.LevelWarn, "warning": slog.LevelWarn, "error": slog.LevelError, "": slog.LevelInfo, "bogus": slog.LevelInfo,
	} {
		l := newLogger(level)
		if !l.Enabled(context.Background(), want) || (want > slog.LevelDebug && l.Enabled(context.Background(), want-1)) {
			t.Errorf("newLogger(%q) does not sit at %v", level, want)
		}
	}
}
