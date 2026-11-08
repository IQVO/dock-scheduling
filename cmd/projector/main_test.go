package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	inboundhttp "github.com/claudioed/dock-scheduling/internal/adapters/inbound/http"
	inboundkafka "github.com/claudioed/dock-scheduling/internal/adapters/inbound/kafka"
	outboundkafka "github.com/claudioed/dock-scheduling/internal/adapters/outbound/kafka"
	"github.com/claudioed/dock-scheduling/internal/config"
)

func quiet() *slog.Logger { return slog.New(slog.NewJSONHandler(io.Discard, nil)) }

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestLoadConfigDefaults(t *testing.T) {
	c, err := loadConfig(env(map[string]string{
		"ANALYTICS_DATABASE_URL": "postgres://u@h/db", "KAFKA_BROKERS": "k1:9092, k2:9092,,", "ANALYTICS_CONSUMER_GROUP": "g",
	}), quiet())
	if err != nil {
		t.Fatal(err)
	}
	want := projectorConfig{
		analyticsURL: "postgres://u@h/db", brokers: []string{"k1:9092", "k2:9092"}, group: "g",
		adminAddr: ":8091", drainDelay: config.DefaultShutdownDur,
	}
	if !reflect.DeepEqual(c, want) {
		t.Fatalf("config = %+v, want %+v", c, want)
	}
}

func TestLoadConfigEnvironmentOverrides(t *testing.T) {
	c, err := loadConfig(env(map[string]string{
		"ANALYTICS_DATABASE_URL": "postgres://u@h/db", "KAFKA_BROKERS": "kafka:9092", "ANALYTICS_CONSUMER_GROUP": " g-from-env ",
		"ADMIN_ADDR": ":9999", "LOG_LEVEL": "debug", "SHUTDOWN_DRAIN_DELAY": "250ms",
	}), quiet())
	if err != nil {
		t.Fatal(err)
	}
	if c.group != "g-from-env" || c.adminAddr != ":9999" || c.logLevel != "debug" || c.drainDelay != 250*time.Millisecond || !reflect.DeepEqual(c.brokers, []string{"kafka:9092"}) {
		t.Fatalf("config = %+v", c)
	}
}

// Every missing variable is named in one boot error, before anything is dialled.
func TestLoadConfigNamesEveryMissingVariable(t *testing.T) {
	_, err := loadConfig(env(nil), quiet())
	if err == nil {
		t.Fatal("an empty environment must not boot")
	}
	for _, name := range []string{"ANALYTICS_DATABASE_URL", "KAFKA_BROKERS", "ANALYTICS_CONSUMER_GROUP"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("the boot error must name %s, got %v", name, err)
		}
	}
	if !errors.Is(err, inboundkafka.ErrNoConsumerGroup) {
		t.Errorf("the missing group is ErrNoConsumerGroup, got %v", err)
	}

	full := map[string]string{"ANALYTICS_DATABASE_URL": "postgres://u@h/db", "KAFKA_BROKERS": "k:9092", "ANALYTICS_CONSUMER_GROUP": "g"}
	for missing, others := range map[string][]string{
		"ANALYTICS_DATABASE_URL":   {"KAFKA_BROKERS", "ANALYTICS_CONSUMER_GROUP"},
		"KAFKA_BROKERS":            {"ANALYTICS_DATABASE_URL", "ANALYTICS_CONSUMER_GROUP"},
		"ANALYTICS_CONSUMER_GROUP": {"ANALYTICS_DATABASE_URL", "KAFKA_BROKERS"},
	} {
		e := map[string]string{}
		for k, v := range full {
			e[k] = v
		}
		e[missing] = " "
		_, err := loadConfig(env(e), quiet())
		if err == nil || !strings.Contains(err.Error(), missing) {
			t.Fatalf("blank %s: err = %v", missing, err)
		}
		for _, other := range others {
			if strings.Contains(err.Error(), other) {
				t.Errorf("blank %s: the error must not name %s, got %v", missing, other, err)
			}
		}
	}
}

func TestRunFailsFastOnMissingConfiguration(t *testing.T) {
	err := run(context.Background(), env(map[string]string{"KAFKA_BROKERS": "k:9092"}), nil)
	if err == nil || !strings.Contains(err.Error(), "ANALYTICS_DATABASE_URL") {
		t.Fatalf("run = %v", err)
	}
}

// The outbound adapter and the projector read the same topic.
func TestTheAnalyticsTopicIsTheOneTheServicePublishes(t *testing.T) {
	if inboundkafka.AnalyticsTopic != outboundkafka.TopicAnalytics {
		t.Fatalf("projector reads %q, the outbox publishes %q", inboundkafka.AnalyticsTopic, outboundkafka.TopicAnalytics)
	}
}

func adminGet(h http.Handler, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestAdminMuxHealthzIsLivenessAndReadyzFlipsOnShutdown(t *testing.T) {
	up := func(context.Context) error { return nil }
	gate := inboundhttp.NewReadiness(time.Nanosecond, time.Second, inboundhttp.Dependency{Name: "analytics-postgres", Check: up})
	mux := newAdminMux(gate)
	if rec := adminGet(mux, "/healthz"); rec.Code != 200 || rec.Body.String() != `{"status":"ok"}` {
		t.Fatalf("healthz = %d %s", rec.Code, rec.Body)
	}
	if rec := adminGet(mux, "/readyz"); rec.Code != 200 || rec.Body.String() != `{"status":"ready"}` {
		t.Fatalf("readyz = %d %s", rec.Code, rec.Body)
	}
	gate.SetNotReady()
	if rec := adminGet(mux, "/readyz"); rec.Code != 503 || rec.Body.String() != `{"status":"not_ready"}` {
		t.Fatalf("readyz during shutdown = %d %s", rec.Code, rec.Body)
	}
	if rec := adminGet(mux, "/healthz"); rec.Code != 200 {
		t.Fatalf("healthz must stay up during shutdown, got %d", rec.Code)
	}
}

// /readyz is real: a dead Kafka broker or analytical database is not ready and
// the body names it; /healthz is unaffected.
func TestAdminMuxReadyzReflectsTheDependencies(t *testing.T) {
	up := func(context.Context) error { return nil }
	down := func(context.Context) error { return errors.New("connection refused") }
	dead := []string{"127.0.0.1:1"}

	mux := newAdminMux(newReadiness(up, dead))
	if rec := adminGet(mux, "/readyz"); rec.Code != 503 || !strings.Contains(rec.Body.String(), `"failing":["kafka"]`) {
		t.Fatalf("readyz with Kafka down = %d %s, want 503 naming kafka", rec.Code, rec.Body)
	}
	rec := adminGet(newAdminMux(newReadiness(down, dead)), "/readyz")
	if rec.Code != 503 || !strings.Contains(rec.Body.String(), "analytics-postgres") || !strings.Contains(rec.Body.String(), "kafka") {
		t.Fatalf("readyz with everything down = %d %s", rec.Code, rec.Body)
	}
	if rec := adminGet(mux, "/healthz"); rec.Code != 200 {
		t.Fatalf("healthz = %d, liveness must not depend on dependencies", rec.Code)
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

func TestConsumerErrReportsAnErrorlessReturn(t *testing.T) {
	if err := consumerErr(inboundkafka.Start(context.Background(), nil)); err == nil {
		t.Fatal("a consumer that returned without an error is still an error to report")
	}
}
