package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	inboundkafka "github.com/claudioed/dock-scheduling/internal/adapters/inbound/kafka"
	"github.com/claudioed/dock-scheduling/internal/application/usecases"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

// groupEnv is the four mandatory consumer groups.
var groupEnv = map[string]string{
	"YARD_CONSUMER_GROUP": "g-yard", "FACILITY_CONSUMER_GROUP": "g-facility", "ASN_CONSUMER_GROUP": "g-asn", "CPT_CONSUMER_GROUP": "g-cpt",
}

// with returns base plus the extra variables.
func with(base map[string]string, extra map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

func TestLoadConfigNamesEveryMissingVariable(t *testing.T) {
	_, err := loadConfig(env(nil), quiet())
	if err == nil {
		t.Fatal("a bare environment must not boot")
	}
	for _, name := range []string{"DATABASE_URL", "KAFKA_BROKERS", "YARD_CONSUMER_GROUP", "FACILITY_CONSUMER_GROUP", "ASN_CONSUMER_GROUP", "CPT_CONSUMER_GROUP"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("a bare environment must name %s, got %v", name, err)
		}
	}
	if strings.Contains(err.Error(), "DOCK_LEGACY_IMPORT_CONSUMER_GROUP") {
		t.Errorf("the legacy importer group is optional and must not be named, got %v", err)
	}
	if _, err = loadConfig(env(with(groupEnv, map[string]string{"DATABASE_URL": "postgres://x"})), quiet()); err == nil ||
		!strings.Contains(err.Error(), "KAFKA_BROKERS") || strings.Contains(err.Error(), "DATABASE_URL") || strings.Contains(err.Error(), "CONSUMER_GROUP") {
		t.Fatalf("only KAFKA_BROKERS is missing, got %v", err)
	}
	if _, err = loadConfig(env(with(groupEnv, map[string]string{"KAFKA_BROKERS": " , "})), quiet()); err == nil {
		t.Fatal("blank brokers are not brokers")
	}
	if _, err = loadConfig(env(with(groupEnv, map[string]string{"KAFKA_BROKERS": "k:9092"})), quiet()); err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Fatalf("only DATABASE_URL is missing, got %v", err)
	}
}

func TestLoadConfigNamesEachMissingConsumerGroup(t *testing.T) {
	for missing := range groupEnv {
		vars := with(groupEnv, map[string]string{"DATABASE_URL": "postgres://x", "KAFKA_BROKERS": "k:9092"})
		vars[missing] = "  "
		_, err := loadConfig(env(vars), quiet())
		if err == nil || !strings.Contains(err.Error(), missing) {
			t.Errorf("without %s the boot error must name it, got %v", missing, err)
		}
		for other := range groupEnv {
			if other != missing && err != nil && strings.Contains(err.Error(), other) {
				t.Errorf("without %s the boot error must not name %s, got %v", missing, other, err)
			}
		}
	}
}

func TestLoadConfigDefaults(t *testing.T) {
	cfg, err := loadConfig(env(with(groupEnv, map[string]string{"DATABASE_URL": "postgres://x", "KAFKA_BROKERS": "k1:9092, k2:9092"})), quiet())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.httpAddr != ":8080" || cfg.migrationsURL != "postgres://x" || len(cfg.brokerList) != 2 || cfg.brokerList[1] != "k2:9092" {
		t.Fatalf("unexpected address, migrations URL or brokers: %+v", cfg)
	}
	if cfg.drainDelay != 5*time.Second || cfg.relayInterval != time.Second || cfg.batchSize != 100 || cfg.maxAttempts != 20 {
		t.Fatalf("unexpected tuning defaults: %+v", cfg)
	}
	want := inboundkafka.Groups{Yard: "g-yard", Facility: "g-facility", Asn: "g-asn", Cpt: "g-cpt"}
	if cfg.groups != want {
		t.Fatalf("groups = %+v, want %+v (the legacy importer is off by default)", cfg.groups, want)
	}
}

func TestLoadConfigOverrides(t *testing.T) {
	cfg, err := loadConfig(env(with(groupEnv, map[string]string{
		"DATABASE_URL": "postgres://pooled", "MIGRATIONS_DATABASE_URL": "postgres://direct", "KAFKA_BROKERS": "k:9092", "HTTP_ADDR": "127.0.0.1:9",
		"SHUTDOWN_DRAIN_DELAY": "0", "OUTBOX_RELAY_INTERVAL": "250ms", "OUTBOX_BATCH_SIZE": "7", "OUTBOX_MAX_ATTEMPTS": "0", "LOG_LEVEL": "debug",
		"DOCK_LEGACY_IMPORT_CONSUMER_GROUP": "g-legacy",
	})), quiet())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.httpAddr != "127.0.0.1:9" || cfg.migrationsURL != "postgres://direct" || cfg.databaseURL != "postgres://pooled" || cfg.logLevel != "debug" {
		t.Fatalf("unexpected address, URLs or level: %+v", cfg)
	}
	if cfg.drainDelay != 0 || cfg.relayInterval != 250*time.Millisecond || cfg.batchSize != 7 || cfg.maxAttempts != 0 {
		t.Fatalf("unexpected tuning overrides: %+v", cfg)
	}
	if cfg.groups.LegacyImport != "g-legacy" {
		t.Fatalf("legacy import group = %q, want g-legacy", cfg.groups.LegacyImport)
	}
}

func TestBuildConsumersEnablesTheLegacyImporterOnlyWhenItsGroupIsSet(t *testing.T) {
	svc := usecases.New(usecases.Deps{})
	cfg := apiConfig{brokerList: []string{"127.0.0.1:1"}, groups: inboundkafka.Groups{Yard: "y", Facility: "f", Asn: "a", Cpt: "c"}}
	set, err := buildConsumers(cfg, svc, quiet())
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(set.names, ","); got != "yard consumer,facility consumer,asn consumer,cutoff consumer" {
		t.Fatalf("consumers = %q, want the four mandatory ones", got)
	}
	closeAll(set)
	cfg.groups.LegacyImport = "l"
	set, err = buildConsumers(cfg, svc, quiet())
	if err != nil {
		t.Fatal(err)
	}
	defer closeAll(set)
	if len(set.names) != 5 {
		t.Fatalf("consumers = %v, want five with the legacy importer", set.names)
	}
}

func TestBuildConsumersNamesTheMissingGroupVariable(t *testing.T) {
	cfg := apiConfig{brokerList: []string{"127.0.0.1:1"}, groups: inboundkafka.Groups{Yard: "y", Facility: "f", Asn: "a"}}
	_, err := buildConsumers(cfg, usecases.New(usecases.Deps{}), quiet())
	if err == nil || !strings.Contains(err.Error(), "CPT_CONSUMER_GROUP") {
		t.Fatalf("a blank group must be a boot error naming CPT_CONSUMER_GROUP, got %v", err)
	}
}

func closeAll(set *consumerSet) {
	for _, c := range set.consumers {
		_ = c.Close()
	}
}

func TestRunFailsFastOnMissingConfiguration(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	t.Setenv("KAFKA_BROKERS", "")
	for _, name := range []string{"YARD_CONSUMER_GROUP", "FACILITY_CONSUMER_GROUP", "ASN_CONSUMER_GROUP", "CPT_CONSUMER_GROUP"} {
		t.Setenv(name, "")
	}
	err := run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "DATABASE_URL") || !strings.Contains(err.Error(), "KAFKA_BROKERS") || !strings.Contains(err.Error(), "YARD_CONSUMER_GROUP") {
		t.Fatalf("run must refuse to boot naming the missing variables, got %v", err)
	}
}

func TestRunFailsOnAnUnreachableDatabase(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // the boot retry gives up at once instead of sleeping out its budget
	err := runWith(ctx, env(with(groupEnv, map[string]string{"DATABASE_URL": "postgres://nobody@127.0.0.1:1/none?sslmode=disable", "KAFKA_BROKERS": "127.0.0.1:1"})), nil)
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

// recorder logs the calls made on it, to pin the shutdown order.
type recorder struct {
	name  string
	calls *[]string
}

func (r recorder) stop()              { *r.calls = append(*r.calls, r.name+".stop") }
func (r recorder) await(*slog.Logger) { *r.calls = append(*r.calls, r.name+".await") }

func TestStopWorkersStopsTheConsumersBeforeTheRelay(t *testing.T) {
	var calls []string
	stopWorkers(quiet(), recorder{"consumers", &calls}, recorder{"relay", &calls})
	want := "consumers.stop,consumers.await,relay.stop,relay.await"
	if got := strings.Join(calls, ","); got != want {
		t.Fatalf("shutdown order = %s, want %s (consumer effects enqueue outbox rows: the relay drains last)", got, want)
	}
}
